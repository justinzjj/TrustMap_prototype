# MapNode Source Layout and Phase Boundary

## Status

Approved on 2026-07-21. This document refines the repository layout of the
paper-faithful prototype without changing the TrustMap protocol or security
model defined in `2026-07-20-trustmap-prototype-design.md`.

## Problem statement

Phase 2 produced a MapNode bootstrap binary and container, but the user-created
`Mapnode/` source directory remained empty. The existing process validates its
configuration, deployment manifest, chain ID and deployed code hashes, then
serves a readiness endpoint. It does not yet implement the durable TrustView,
planning, proof construction, indexing, P2P or transaction execution described
by the paper.

Therefore:

- “MapNode image builds” means only that the Phase 2 bootstrap image builds;
- it must not be used to claim that a functional MapNode has been implemented;
- the functional MapNode starts with Phase 3 and its implementation lives under
  `Mapnode/`.

## Chosen layout

`cmd/mapnode` remains a thin executable entry point. All MapNode service code
lives under the repository's `Mapnode/` directory:

```text
Mapnode/
├── bootstrap/       # config, profile, manifest and runtime validation
├── registry/        # chain metadata and home-chain authority boundary
├── store/           # SQLite migrations, WAL setup and repositories
├── evidence/        # evidence IDs, lifecycle and validation-facing models
├── trustview/       # active graph and immutable snapshots
├── planner/         # bounded shortest path and DirectPlan fallback
├── proof/           # per-hop witness lookup and recursive PathProof assembly
├── coordinator/     # persistent request state machine
├── app/             # process composition
├── chain/           # Phase 4 RPC clients and chain registry adapters
├── indexer/         # Phase 4 subscriptions, backfill and durable cursor
├── reorg/           # Phase 4 rollback and degraded-state handling
├── p2p/             # Phase 4 GossipSub and bounded sync protocols
├── executor/        # Phase 4 nonce-safe transaction submission
└── api/             # Phase 4 health, status, TrustView and request APIs
```

The Phase 2 `internal/mapnodebootstrap` package is migrated to
`Mapnode/bootstrap`; it is not retained as a second implementation. Generic
cross-layer primitives such as exact uint256 domain types and Merkle hashing may
remain in `internal/domain` and `internal/proof` while MapNode-specific behavior
is implemented under `Mapnode/`.

The MapNode Dockerfile copies `Mapnode/` explicitly and builds only the thin
`cmd/mapnode` entry point plus its declared dependencies.

## Phase 3 boundary: durable MapNode core

Phase 3 is an in-process, persistent core. It does not yet subscribe to live
Geth events, open libp2p streams or broadcast transactions.

It must deliver:

1. deterministic evidence and request identifiers;
2. embedded, ordered SQLite migrations using `modernc.org/sqlite`, a pure-Go
   driver compatible with the repository's `CGO_ENABLED=0` container build;
3. WAL mode, foreign keys and transaction-backed repositories;
4. evidence transitions `candidate -> verified -> confirmed -> active`, with
   `candidate -> invalid` and no transition out of terminal states;
5. persistent TrustView nodes and active edges;
6. immutable graph snapshots used throughout one planning/proof attempt;
7. cost-bounded shortest-path planning over active edges only;
8. DirectPlan fallback with a persisted, enumerated reason whenever no usable
   path exists, proof material is missing, or path cost exceeds the calibrated
   direct cost;
9. recursive PathProof assembly and local verification using the existing
   Solidity-parity Merkle primitives;
10. an idempotent persistent request state machine and a coordinator that ties
    evidence, planning and proof construction together.

The Planner reads `directCost` only from the deployment-validated
`pow-spv-3m` profile. Callers and request payloads cannot supply or reduce that
cost.

## Phase 3 data flow

```text
Observed request/evidence
        |
        v
SQLite transaction -> evidence lifecycle -> active TrustView
        |                                      |
        |                                      v
        +--------------------------> immutable snapshot
                                               |
                                               v
                                      bounded Planner
                                       /            \
                              DirectPlan          PathPlan
                                  |                  |
                                  |                  v
                                  |          recursive Proof Builder
                                  +---------> persistent Coordinator state
```

Only active evidence may create an edge traversed by the Planner. A snapshot ID
is frozen before planning and reused for proof lookup; missing or stale proof
material causes a recorded fallback/replan outcome rather than an unverifiable
PathPlan.

## Phase 3 checkpoint

The checkpoint is a pure in-process, database-backed `C -> B -> A` case:

- active evidence produces the expected two-hop path;
- the same snapshot supplies every membership witness;
- the Go proof closes to the expected TrustRoot and matches Solidity encoding;
- path cost is compared with the deployment-calibrated `3,000,096` gas value;
- inactive or invalid evidence is never traversed;
- missing proof material and excessive path cost produce explicit DirectPlan
  fallback reasons;
- closing and reopening the database preserves evidence, graph, snapshots and
  request state.

Passing this checkpoint completes Phase 3 only. Live RPC, reorg handling, P2P,
transaction submission and operational APIs remain Phase 4 work.

## Testing and migration rules

- Every production behavior is introduced test-first.
- Storage tests use real temporary SQLite databases, not repository mocks.
- Planner and proof tests use deterministic graph and Merkle fixtures.
- Migration tests open both a new database and a database already at the latest
  schema version.
- The old `internal/mapnodebootstrap` package is deleted only after imports,
  tests and the Docker build use `Mapnode/bootstrap` successfully.
- Phase 3 ends with focused race tests, full `go test -race ./...`, `go vet`, a
  clean-clone Docker build and independent specification/code-quality reviews.
