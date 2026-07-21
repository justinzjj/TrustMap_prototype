# Durable MapNode Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the database-backed Phase 3 MapNode core under `Mapnode/`, ending with a deterministic in-process `C -> B -> A` planning and recursive-proof checkpoint.

**Architecture:** `cmd/mapnode` remains a thin executable. `Mapnode/bootstrap` owns Phase 2 startup validation; `Mapnode/store` owns SQLite transactions and immutable snapshots; evidence, TrustView, Planner, Proof Builder and Coordinator are independent packages joined by small interfaces. Phase 3 does not subscribe to Geth, open libp2p connections or broadcast transactions.

**Tech Stack:** Go 1.24.10, go-ethereum 1.17.3, `modernc.org/sqlite v1.45.0` (latest release in the checked version range whose module declares Go 1.24 compatibility), SQLite WAL/foreign keys, existing Solidity-parity `internal/domain` and `internal/proof`, Docker/CGO-disabled build.

---

### Task 1: Move the bootstrap skeleton into the real MapNode source root

**Files:**
- Create: `Mapnode/bootstrap/bootstrap.go`
- Create: `Mapnode/bootstrap/runtime.go`
- Create: `Mapnode/bootstrap/bootstrap_test.go`
- Modify: `cmd/mapnode/main.go`
- Modify: `cmd/mapnode/main_test.go`
- Modify: `docker/mapnode.Dockerfile`
- Modify: `tests/integration/container_build_test.sh`
- Delete: `internal/mapnodebootstrap/bootstrap.go`
- Delete: `internal/mapnodebootstrap/runtime.go`
- Delete: `internal/mapnodebootstrap/bootstrap_test.go`

- [ ] Add failing import/build and container-contract tests requiring the bootstrap package under `Mapnode/bootstrap`, requiring Docker to copy `Mapnode/`, and rejecting the legacy `internal/mapnodebootstrap` path.
- [ ] Run `go test ./cmd/mapnode ./Mapnode/bootstrap` and the skip-build container test; record RED because the new package does not exist.
- [ ] Move the existing strict config/profile/manifest/runtime validation without changing its behavior; update the thin command entry point and Docker build context.
- [ ] Remove the legacy package after every import and test uses `Mapnode/bootstrap`; do not retain wrappers or duplicate validation logic.
- [ ] Run focused tests, `go test ./...`, `go vet ./...`, the skip-build integration test and a clean MapNode image build with the already-supported local proxy overrides.
- [ ] Commit as `refactor: move bootstrap into MapNode source root`.

### Task 2: Add deterministic identifiers, evidence lifecycle and SQLite repository

**Files:**
- Create: `Mapnode/evidence/id.go`
- Create: `Mapnode/evidence/state.go`
- Create: `Mapnode/evidence/evidence_test.go`
- Create: `Mapnode/coordinator/state.go`
- Create: `Mapnode/coordinator/state_test.go`
- Create: `Mapnode/store/db.go`
- Create: `Mapnode/store/migrations.go`
- Create: `Mapnode/store/repository.go`
- Create: `Mapnode/store/store_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] Write failing tests for an evidence ID computed from canonical chain ID, contract address, block number/hash, transaction hash/index, log index and payload digest; the same normalized locator must always produce the same Keccak ID and any field change must change it.
- [ ] Write failing tests matching the Gateway request ID formula `keccak256(abi.encode(homeChainId, gateway, requester, nonce, sourceChainId, sourceHeight, sourceBlockHash))`.
- [ ] Write failing table tests for the only legal evidence transitions: `candidate -> verified -> confirmed -> active` and `candidate -> invalid`; repeated identical transitions are idempotent, all skips/backward/terminal transitions fail.
- [ ] Write failing request-state tests for the Phase 3 subset: `Observed -> EvidenceReady -> Planned -> ProofReady`, plus `Retryable -> Replanned`, `Retryable -> DirectFallback`, and deterministic invalid input to `Rejected`.
- [ ] Write failing real-database tests requiring embedded ordered migrations, `journal_mode=WAL`, `foreign_keys=ON`, idempotent reopen, atomic rollback, and persistence after close/reopen.
- [ ] Implement the pure-Go SQLite connection, the complete Phase 3 schema, and transaction-backed repositories for evidence, requests and request transition history. Later tasks add typed TrustView/snapshot/witness repository methods against the already-migrated tables. Store 256-bit IDs/heights and hashes losslessly as fixed-size blobs; never coerce them through signed SQLite integers.
- [ ] Enforce uniqueness for evidence ID, request ID, node key and edge ID at the schema boundary.
- [ ] Keep `Mapnode/coordinator` independent of the concrete store: coordinator declares its request repository interface using coordinator-owned state types; `Mapnode/store` imports those types and satisfies the interface. The coordinator package must not import `Mapnode/store`.
- [ ] Run `go test -race ./Mapnode/evidence ./Mapnode/coordinator ./Mapnode/store -count=1`, full Go tests, vet and a CGO-disabled MapNode build.
- [ ] Commit as `feat: add durable MapNode evidence store`.

### Task 3: Implement immutable TrustView snapshots and calibrated planning

**Files:**
- Create: `Mapnode/registry/registry.go`
- Create: `Mapnode/registry/registry_test.go`
- Create: `Mapnode/trustview/types.go`
- Create: `Mapnode/trustview/service.go`
- Create: `Mapnode/trustview/trustview_test.go`
- Create: `Mapnode/planner/planner.go`
- Create: `Mapnode/planner/planner_test.go`
- Modify: `Mapnode/store/repository.go`
- Modify: `Mapnode/store/store_test.go`

- [ ] Write failing registry tests requiring exactly one home chain, transactions allowed only on that entry, and `directCost=3_000_096` accepted only from a deployment-validated `pow-spv-3m / 3 / 3 / 4497` profile. Custom or mismatched profiles expose no calibrated direct cost.
- [ ] Write failing repository/service tests showing only active evidence produces active TrustView edges, duplicate merges are idempotent, and a created snapshot never changes after later graph updates.
- [ ] Require an active evidence record before an edge can be activated; enforce this in the transaction-backed repository rather than trusting callers.
- [ ] Define the graph node key as `(chainId, height, blockHash)` and persist TrustRoot, evidence state and first-observed time. Define directed inter-chain edges from the recording-chain node to the verified source-chain node and retain the evidence/witness locator.
- [ ] Write failing Planner tests for deterministic, cost-bounded Dijkstra over one immutable snapshot: stable tie-breaking, active edges only, no cycles in the result, and no mutation of the snapshot.
- [ ] Make the Planner obtain `directCost` from the validated registry rather than a request argument. Return `PathPlan` only when a path exists and `hopCount * pathStepCost <= directCost`; otherwise return `DirectPlan` with one of `uncalibrated_profile`, `no_path`, or `path_cost_exceeds_direct`.
- [ ] Persist the selected plan, snapshot ID, path cost, direct cost and fallback reason with the request so restart cannot silently change the decision.
- [ ] Run focused race tests, full Go tests and vet.
- [ ] Commit as `feat: add persistent TrustView planner`.

### Task 4: Build recursive PathProofs and the Phase 3 coordinator checkpoint

**Files:**
- Create: `Mapnode/proof/types.go`
- Create: `Mapnode/proof/builder.go`
- Create: `Mapnode/proof/builder_test.go`
- Create: `Mapnode/coordinator/coordinator.go`
- Create: `Mapnode/coordinator/coordinator_test.go`
- Create: `Mapnode/app/app.go`
- Create: `Mapnode/app/app_test.go`
- Create: `Mapnode/phase3_checkpoint_test.go`
- Modify: `cmd/mapnode/main.go`
- Modify: `Mapnode/store/repository.go`
- Modify: `Mapnode/store/store_test.go`
- Modify: `docker/mapnode.Dockerfile`
- Modify: `tests/integration/container_build_test.sh`

- [ ] Write failing Proof Builder tests that freeze one snapshot, reverse the Planner's home-to-source edge path into Solidity verification order, load every witness from that same snapshot, and produce `baseTrustRoot`, ordered block hashes and membership witnesses.
- [ ] Require local `internal/proof.VerifyPath` success against the expected home TrustRoot before returning a PathProof. Missing witness yields `proof_material_missing`; a changed expected home root yields `stale_snapshot`; neither may return a PathPlan payload.
- [ ] Write failing Coordinator tests for idempotent request observation, transactional state/plan persistence, PathPlan proof construction, DirectPlan fallback persistence and restart recovery from the last committed state.
- [ ] Implement `Mapnode/app` composition so normal startup opens/migrates the configured SQLite database, constructs registry/store/TrustView/Planner/Proof/Coordinator services, performs existing runtime validation and only then reports ready. Do not add live indexers, P2P workers or executors in Phase 3.
- [ ] Add the real-database `C -> B -> A` checkpoint: two active evidence edges select a two-hop path; witnesses reconstruct `A -> B -> C` to the expected home root; inactive evidence is ignored; missing witness and excessive path cost persist explicit DirectPlan fallbacks; close/reopen preserves evidence, graph, snapshot and request decision.
- [ ] Update the MapNode Docker build to copy every Phase 3 package and prove a clean CGO-disabled image build. Keep the official proxy/image defaults and use local overrides only in the verification command.
- [ ] Run `go test -race ./internal/domain ./internal/proof ./Mapnode/... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, container static/behavior tests, the full three-image Docker build, `git diff --check`, and a tracked-secret/runtime scan.
- [ ] Request independent specification and code-quality reviews. Reviewers must confirm Phase 3 does not trust inactive evidence, accept requester-supplied direct cost, mix snapshot versions, or claim Phase 4 functionality.
- [ ] Commit as `feat: complete durable MapNode core` plus narrowly scoped review-fix commits if required.

### Phase 3 checkpoint

Phase 3 is complete only when all four tasks and their reviews pass, `Mapnode/` contains the functional persistent core, the legacy bootstrap package is gone, and the database-backed `C -> B -> A` checkpoint passes after restart. Passing this checkpoint authorizes Phase 4; it does not itself demonstrate live Geth indexing, P2P propagation or transaction execution.
