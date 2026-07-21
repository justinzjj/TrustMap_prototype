# Experiment-First Live MapNode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` task by task. Every production change follows TDD and receives independent specification review followed by code-quality review.

**Goal:** Run the paper's DirectPlan and `C -> B -> A` PathPlan against real Geth-hosted contracts with one real MapNode per chain, while omitting production features that do not change experimental verification.

**Architecture:** Phase 3 remains the durable core. Phase 4 adds thin live adapters: a static multi-chain RPC catalog, confirmation-only HTTP indexer, fail-closed canonical cursor, minimal `DependencyRecorded` GossipSub, RPC evidence validator, and a single serialized home-chain executor. P2P only discovers evidence; Geth and Gateway remain authoritative.

**Tech Stack:** Go 1.24, go-ethereum, go-libp2p GossipSub, modernc SQLite, Solidity/Foundry, Geth v1.17.3 developer chains, Docker Compose.

**Scope note:** The user explicitly requested a few major stages rather than a minute-by-minute plan. Each task below is a complete checkpoint. Implementers must still report RED/GREEN TDD evidence and make coherent commits.

---

## Global Invariants

- Do not modify `TrustMap-ETH` or the paper repository.
- Preserve all Phase 1–3 migration checksums and semantics.
- Keep paper names visible: `TrustRootObservation`, `TrustRootUpdated`, `DependencyRecorded`, `DependencyEvidenceEnvelope`, `TrustView`, `PathProof`, `DirectPlan`, and `PathPlan`.
- Never activate an edge from a P2P message alone.
- Never allow request input to select Merkle depth, direct cost, signers, signature checks, or hash rounds.
- Prefer explicit experiment limitations over incomplete production machinery.
- Repository proxy and image defaults remain official; local commands may override them.

## Task 1: Live Chain Catalog, TrustRootObservation, and Canonical Cursor

**Outcome:** Every MapNode can resolve all configured chains read-only, verify a remote deployment lazily, observe a historical confirmed TrustRoot, and advance a restart-safe cursor. A cursor mismatch persists degraded state and blocks planning/submission.

**Primary files:**

- `Mapnode/chain/chain_registry.go`
- `Mapnode/chain/canonical_block.go`
- `Mapnode/chain/gateway_client.go`
- `Mapnode/chain/trust_root_reader.go`
- `Mapnode/chainabi/gateway_calls.go`
- `Mapnode/chainabi/gateway_events.go`
- `Mapnode/trustview/trust_root_observation.go`
- `Mapnode/reorg/canonical_cursor.go`
- `Mapnode/store/live_chain_repository.go`
- `Mapnode/store/canonical_cursor_repository.go`
- `Mapnode/store/trust_root_observation_repository.go`
- new immutable migration in `Mapnode/store/migrations.go`
- catalog changes in `Mapnode/bootstrap`, `internal/config`, `internal/topology`, and `Mapnode/app`

**Required behavior:**

- rendered configuration lists every chain's HTTP RPC, deployment manifest, confirmations, and home marker;
- exactly one entry is home and only it carries signing authority;
- ABI selectors and event topics match Solidity golden values;
- historical `currentTrustRoot()` observation binds chain ID, height, canonical block hash, Gateway code hash, TrustRoot, confirmations, and content ID;
- an observation may create a TrustNode but no TrustEdge;
- cursor advancement is monotonic and atomic with block identity;
- a persisted height/hash mismatch records degraded state and fails closed;
- 3- and 21-chain rendering remains deterministic.

**Checkpoint:** focused race tests for chain/ABI/reorg/store/topology/app, full vet, and commit `feat: add live TrustRoot observation foundation`.

## Task 2: Confirmed Gateway Indexer and Dependency Materialization

**Outcome:** A real confirmation-only HTTP indexer turns home Gateway logs into durable requests and active dependency edges. It reconstructs the exact MembershipWitness from a successful verification receipt before calling the existing TrustView repository.

**Primary files:**

- `Mapnode/indexer/confirmed_event_indexer.go`
- `Mapnode/indexer/verification_requested.go`
- `Mapnode/indexer/trust_root_updated.go`
- `Mapnode/indexer/dependency_recorded.go`
- `Mapnode/indexer/request_resolved.go`
- `Mapnode/indexer/verification_receipt.go`
- `Mapnode/store/indexer_repository.go`
- event extensions in `Mapnode/chainabi/gateway_events.go`
- worker composition in `Mapnode/app/app.go`

**Required behavior:**

- calculate `safeHead = head - confirmations` and never materialize newer logs;
- scan bounded HTTP ranges from the durable cursor;
- verify canonical headers and sort by block, transaction, and log index;
- decode only the configured Gateway and supported exact events;
- persist `VerificationRequested` with the exact Gateway RequestID;
- bind `DependencyRecorded`, `TrustRootUpdated`, and `RequestResolved` from one successful receipt;
- reconstruct and locally verify the MembershipWitness against the receipt home TrustRoot;
- drive Evidence through candidate, verified, confirmed, and active before merging TrustEdge;
- commit one safe block plus cursor atomically and replay idempotently;
- reject wrong-chain/code, failed receipts, wrong digest, incomplete bundles, and unsupported multiple verification updates in one experiment block.

**Checkpoint:** focused JSON-RPC/receipt tests, full race tests, and commit `feat: index confirmed Gateway evidence`.

## Task 3: Minimal Dependency Evidence Gossip and Remote RPC Validation

**Outcome:** MapNodes use real libp2p GossipSub to discover dependency evidence. Duplicate or fabricated messages remain inert; only remote RPC validation can activate TrustEdge.

**Primary files:**

- `Mapnode/p2p/dependency_evidence_envelope.go`
- `Mapnode/p2p/dependency_evidence_gossip.go`
- `Mapnode/p2p/bootstrap_peers.go`
- `Mapnode/evidence/remote_dependency_validator.go`
- `Mapnode/store/evidence_inbox_repository.go`
- `Mapnode/store/evidence_outbox_repository.go`
- next immutable migration in `Mapnode/store/migrations.go`
- publishing hook in `Mapnode/indexer/dependency_recorded.go`
- worker composition in `Mapnode/app/app.go`

**Required behavior:**

- load persistent topology-generated Ed25519 identity and strict bootstrap peers;
- publish a versioned deterministic envelope only after local confirmed evidence is durable;
- persist inbox/outbox message IDs for at-least-once delivery and deduplication;
- store received envelopes only as candidate discovery input;
- fetch and validate remote canonical block, receipt, exact log, code hash, confirmations, payload digest, request/dependency fields, TrustRoot update, RequestResolved root, and witness;
- activate Evidence and merge TrustEdge only after RPC validation;
- record invalid reason and origin peer for fabricated messages;
- omit proof streams, peer scoring, dynamic discovery, and global blacklists.

**Checkpoint:** focused race tests including two-host loopback GossipSub, full race tests, and commit `feat: gossip verifiable dependency evidence`.

## Task 4: Serialized Direct/Path Executor and Live Experiment Checkpoint

**Outcome:** MapNode consumes confirmed requests, chooses the calibrated plan, submits a real direct or path transaction, survives restart around broadcast/receipt, and exposes minimal experiment status. Docker proves `B -> A` DirectPlan and `C -> B -> A` PathPlan.

**Primary files:**

- `Mapnode/chainabi/direct_proof.go`
- `Mapnode/executor/direct_plan_executor.go`
- `Mapnode/executor/path_plan_executor.go`
- `Mapnode/executor/transaction_submission.go`
- `Mapnode/executor/request_worker.go`
- `Mapnode/store/transaction_repository.go`
- `Mapnode/api/health.go`
- `Mapnode/api/request_status.go`
- `Mapnode/api/trust_view.go`
- Phase 4 state changes in `Mapnode/coordinator`, `Mapnode/store`, `Mapnode/app`, and `cmd/mapnode`
- `scripts/run-two-chain-direct.sh`
- `scripts/run-three-chain-path.sh`
- `tests/integration/live_two_chain_test.sh`
- `tests/integration/live_three_chain_test.sh`
- paper traceability and README experiment limitations

**Required behavior:**

- one worker owns the home account and pending nonce;
- wait for a block boundary if the current block already contains a TrustRoot update;
- DirectPlan reads historical source TrustRoot, calls deployed `attestationDigest`, creates ordered real signatures, and ABI-encodes the direct proof;
- PathPlan uses immutable snapshot-bound PathProof in Solidity order;
- persist signed raw transaction and nonce before broadcast;
- restart checks transaction, receipt, and `requestResolved(requestId)` before rebroadcast;
- `Submitted` is not success; only a successful confirmed receipt and resolved request enter `Confirmed`;
- stale Home TrustRoot enters retry/replan;
- expose only ready/degraded state, request status, transaction hash, and TrustView inspection;
- two-chain Docker test proves real DirectPlan and propagated verified edge;
- three-chain Docker test proves a two-hop PathPlan was selected and accepted by Gateway;
- 21-chain topology remains static-only and valid.

**Final verification:** full race and vet, CGO-disabled CLI builds, Foundry tests, container tests, two- and three-chain live scripts, migration upgrades, `git diff --check`, and a clean worktree.

**Checkpoint commits:** `feat: execute live TrustMap verification plans`, `test: prove live three-chain TrustMap path`, plus review-fix commits when required.

## Review Protocol

For each task:

1. one implementation agent follows TDD and commits;
2. an independent specification agent checks the paper and this plan;
3. after specification approval, an independent quality agent checks persistence, crash windows, ordering, identities, and tests;
4. the implementation agent fixes findings and both reviewers recheck as needed;
5. the main agent runs the checkpoint commands before moving on.

After Task 4, a final cross-component review confirms that no P2P or RPC shortcut can create an accepted dependency and records exact experiment commands, commits, limitations, and the Phase 5 starting point in local project memory.

