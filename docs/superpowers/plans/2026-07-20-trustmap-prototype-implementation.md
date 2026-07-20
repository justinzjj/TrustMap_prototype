# TrustMap Prototype Implementation Plan

> **For agentic workers:** Use subagent-driven development for independent workstreams. Each phase ends with a verification and user review checkpoint before the next phase expands scope.

**Goal:** Implement the approved TrustMap prototype as a paper-faithful, Dockerized, real MapNode system with configurable 2–21 Geth EVM chains.

**Architecture:** Solidity contracts provide the TrustRoot and final acceptance boundary. One Go MapNode per chain maintains a persistent local TrustView, validates chain-observable evidence, plans DirectPlan or PathPlan, constructs proofs, communicates over P2P and submits only to its home chain. A topology generator renders the multi-chain Docker environment.

**Baseline:** Go 1.24.10, go-ethereum/Geth 1.17.3, Foundry 1.4.4, SQLite WAL, go-libp2p, Docker Compose 2.27.0.

---

## Phase 1: Repository foundation and public contracts

### Deliverables

- Go module, build/test commands, `.gitignore` and generated-runtime boundary.
- Foundry project.
- Paper-faithful leaf hashing, lazy TrustRoot update and in-chain anchor.
- Recursive PathProof verifier using explicit membership witnesses.
- `IDirectVerifier` and development attestation verifier.
- Atomic request Gateway with DirectPlan and PathPlan entry points.
- Shared Go/Solidity Merkle vectors.

### Parallel allocation

- Contract subagent: Solidity implementation and Foundry tests.
- Foundation subagent: Go module, repository guardrails and shared domain types.
- Root agent: public ABI/type review, cross-language proof vectors and integration.

### Verification

```bash
forge fmt --root contracts --check
forge test --root contracts -vv
go test ./...
git diff --check
```

### Checkpoint 1

Review contract ABI, events, leaf format, lazy-update behavior and recursive proof vectors before MapNode code depends on them.

## Phase 2: Configurable Geth topology and Docker skeleton

### Deliverables

- Strict topology schema and `trustmapctl topology validate/render`.
- Default three-chain configuration.
- 21-chain configuration example.
- Per-chain Chain ID and block-period overrides.
- Geth 1.17.3 developer-mode services on one Docker bridge network.
- One MapNode service and one deployment job per chain.
- Runtime identity/key generation with no committed secrets.
- Static Compose validation without starting 21 chains.

### Parallel allocation

- Topology subagent: schema, renderer and 3/21-chain tests.
- Container subagent: Geth/MapNode images, health checks and runtime bootstrap.
- Root agent: security review of generated keys, genesis allocation and service dependencies.

### Verification

```bash
go test ./internal/config ./internal/topology ./internal/cli
go run ./cmd/trustmapctl topology validate --file configs/topology-21.yaml
docker compose -f runtime/generated/compose.yaml config --quiet
bash tests/integration/container_build_test.sh
```

### Checkpoint 2

Review generated three-chain and 21-chain Compose models, especially block times, one-to-one MapNode mapping, Docker DNS and secret handling.

## Phase 3: Durable MapNode core

### Deliverables

- Domain identifiers and deterministic evidence/request IDs.
- SQLite migrations and WAL-backed repository.
- Evidence lifecycle: candidate, verified, confirmed, active or invalid.
- Persistent TrustView nodes, edges and frontier estimates.
- Immutable graph snapshots and cost-bounded shortest-path planner.
- Go Merkle mirror and recursive PathProof builder matching Solidity.
- Persistent request state machine and DirectPlan fallback reasons.

### Parallel allocation

- Storage/evidence subagent: schema, repository and evidence transitions.
- Graph/planner subagent: TrustView snapshots and cost-aware path search.
- Proof subagent: Merkle mirror, witness construction and Solidity parity.
- Root agent: coordinator state machine and integration review.

### Verification

```bash
go test ./internal/domain ./internal/store/... ./internal/trustview ./internal/planner ./internal/proof ./internal/coordinator -race
go vet ./...
git diff --check
```

### Checkpoint 3

Review a pure in-process `C -> B -> A` planning/proof case before connecting live RPC or P2P.

## Phase 4: Geth events, P2P, API and transaction execution

### Deliverables

- Home-chain WS subscription with HTTP log backfill and durable cursor.
- Provisional event handling and reorg rollback.
- Remote evidence validation against canonical blocks, receipts and logs.
- GossipSub evidence propagation with persistent inbox/outbox.
- Bounded evidence/proof request-response protocols.
- Nonce-safe home-chain transaction executor and stale-proof classification.
- MapNode HTTP health, status, TrustView and request APIs.
- Operational metrics and structured logs.

### Parallel allocation

- Chain subagent: registry, Indexer, backfill and reorg manager.
- P2P subagent: envelope, GossipSub, sync streams and inbox/outbox.
- Execution/API subagent: nonce manager, Executor, HTTP and metrics.
- Root agent: Evidence Validator and full MapNode process composition.

### Verification

```bash
go test -race ./internal/chain ./internal/indexer ./internal/reorg ./internal/evidence ./internal/p2p ./internal/executor ./internal/api ./internal/app
go vet ./...
git diff --check
```

### Checkpoint 4

Review evidence trust boundary: peer-signed false updates must remain candidates or become invalid and must never create active edges.

## Phase 5: End-to-end DirectPlan and PathPlan

### Deliverables

- Deterministic contract deployment manifests.
- Two-chain DirectPlan workflow.
- Three-chain `B -> A`, `C -> B`, then `C -> A` recursive PathPlan workflow.
- Proof preflight against the current TrustRoot.
- Restart, WS disconnect, P2P partition and stale-proof recovery tests.
- 21-chain static topology acceptance.

### Execution order

1. Bring up two chains and validate DirectPlan.
2. Expand the same harness to three chains.
3. Validate recursive PathPlan.
4. Inject recovery and adversarial cases.
5. Re-run from clean runtime twice to detect state leakage.

### Verification

```bash
TRUSTMAP_E2E=1 go test ./tests/integration -run TestDirectPlanE2E -count=2 -v -timeout 20m
TRUSTMAP_E2E=1 go test ./tests/integration -run TestRecursivePathPlanE2E -count=2 -v -timeout 30m
TRUSTMAP_E2E=1 go test ./tests/integration -run 'Test(Recovery|Adversarial)' -v -timeout 30m
```

### Checkpoint 5

Inspect real receipts, emitted dependency events, TrustRoot changes, chosen paths and MapNode databases before declaring the prototype workflow complete.

## Phase 6: Open-source handoff and final audit

### Deliverables

- README quickstart for three chains and configuration example for 21 chains.
- Explicit warning that the development DirectVerifier is not a light client and the repository is not an asset bridge.
- Architecture, P2P protocol, operations and security documentation.
- Completed `docs/paper-traceability.md` mapping every normative mechanism to code and tests.
- Clean project memory and reproducible next-step instructions.
- Secret/runtime scan and final Git audit.

### Verification

```bash
make check
git grep -nE '(BEGIN (EC )?PRIVATE KEY|"private_key"[[:space:]]*:)' -- ':!docs/**' ':!README.md'
git status --short
git diff --check origin/main..HEAD
```

### Final checkpoint

Review the paper-traceability matrix and the limitations stated in README before publishing or pushing the implementation branch.
