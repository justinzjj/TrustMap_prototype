# Experiment-First Live MapNode Design

## Status

Approved by the user on 2026-07-21. This document narrows Phase 4 of the
repository-wide TrustMap design to the smallest real multi-Geth implementation
needed for the paper experiments. The paper remains normative. This refinement
may reduce operational sophistication, but it must not weaken any verification
condition or let off-chain messages become a trust root.

## Goal

Replace the Phase 3 in-process checkpoint with a live experimental loop:

```text
Geth event -> confirmed Evidence -> TrustView -> DirectPlan/PathPlan
           -> PathProof or costed direct proof -> home Gateway transaction
           -> receipt and new DependencyRecorded evidence -> minimal P2P gossip
```

The default runtime proves this loop with three independent Geth chains and one
MapNode per chain. Topology generation remains configurable for 2 through 21
chains, but only two- and three-chain live experiments are required.

## Non-Negotiable Paper Boundaries

- A P2P message is only a discovery hint. It cannot create active Evidence,
  TrustNode, TrustEdge, TrustView state, a Plan, or a PathProof by itself.
- Every remote dependency must be revalidated against a canonical confirmed
  Geth block, successful receipt, configured Gateway address, exact event ABI,
  and deterministic payload digest.
- `TrustRootObservation` may establish a TrustNode after historical RPC and
  canonical-block validation. It can never establish a TrustEdge.
- Only a verified `DependencyRecorded` event may establish a cross-chain
  TrustEdge.
- Direct verification continues to use
  `ExperimentalCostedDirectVerifier`. It is an experiment cost simulator with
  real context-bound signatures, not a light client.
- The deployment-fixed direct-verifier profile and `merkleDepth` remain trusted
  configuration. A request cannot lower either cost.
- The final result is accepted only by `TrustMapGateway` on the home chain.

## Selected Approach

The selected approach is a real but deliberately small distributed prototype:

- HTTP JSON-RPC polling instead of a WebSocket subscription plus reconnect
  subsystem;
- confirmation-only event processing instead of provisional state and automatic
  rollback;
- real libp2p GossipSub carrying dependency Evidence locators only;
- static topology-based remote chain discovery;
- one serialized home-chain transaction worker;
- minimal health, request-status, and TrustView inspection APIs;
- explicit fail-closed degraded state instead of production-grade deep-reorg
  repair.

Scanning every chain centrally was rejected because it removes the paper's P2P
Evidence propagation behavior. A production-grade WS/reorg/transaction/P2P
stack was rejected because it does not affect the target experiment result.

## Package Layout and Mechanism Names

Paper mechanisms must remain visible in package paths, filenames, and exported
types where practical:

```text
Mapnode/
├── chain/
│   ├── chain_registry.go
│   ├── canonical_block.go
│   ├── gateway_client.go
│   └── trust_root_reader.go
├── chainabi/
│   ├── gateway_events.go
│   ├── gateway_calls.go
│   └── direct_proof.go
├── indexer/
│   ├── confirmed_event_indexer.go
│   ├── verification_request.go
│   ├── trust_root_update.go
│   └── dependency_recorded.go
├── reorg/
│   └── canonical_cursor.go
├── p2p/
│   ├── dependency_evidence_envelope.go
│   └── dependency_evidence_gossip.go
├── executor/
│   ├── direct_plan_executor.go
│   ├── path_plan_executor.go
│   ├── transaction_submission.go
│   └── request_worker.go
└── api/
    ├── health.go
    ├── request_status.go
    └── trust_view.go
```

Existing `Mapnode/trustview`, `Mapnode/planner`, `Mapnode/proof`,
`Mapnode/coordinator`, and `Mapnode/store` remain the durable core rather than
being reimplemented in live adapters.

## Static Chain Registry

The topology renderer gives each MapNode a read-only catalog for all chains:

- stable name and uint256 Chain ID;
- HTTP RPC URL;
- Gateway and DirectVerifier deployment manifest path or resolved address;
- deployment block, code hashes, confirmation depth, and Merkle depth;
- whether the entry is the MapNode's home chain.

Only the home entry has the transaction signer and direct-proof signer files.
Remote entries are read-only. Startup verifies the home deployment as Phase 3
already does; a remote entry is verified lazily before its first observation or
Evidence validation.

## Chain ABI and Historical TrustRoot Observation

`Mapnode/chainabi` contains the minimal checked ABI needed by MapNode. It must
match Solidity selectors, event topics, indexed fields, and tuple layouts in
tests. Generated Foundry artifacts under `contracts/out` are not runtime
dependencies.

For a request source `(chainId, height, blockHash)`, `TrustRootReader`:

1. reads the canonical block at `height` and matches `blockHash`;
2. requires the block to be at least the configured confirmation depth behind
   the current head;
3. verifies chain ID, Gateway address, and deployed code hash;
4. calls `currentTrustRoot()` at the historical block tag;
5. persists a content-bound `TrustRootObservation` and an active TrustNode.

This observation does not create an edge. It gives Planner a real target node
for the initial DirectPlan and subsequent PathPlan requests.

## Confirmed Event Indexer

The experiment Indexer uses HTTP polling:

1. load the durable per-chain cursor;
2. read the current head;
3. set `safeHead = head - confirmations`;
4. fetch Gateway logs from `cursor + 1` through `safeHead` in bounded ranges;
5. load canonical block headers and sort logs by
   `(blockNumber, transactionIndex, logIndex)`;
6. decode and validate events;
7. commit Evidence, derived state, and the new cursor atomically per block.

The required events are `VerificationRequested`, `TrustRootUpdated`,
`DependencyRecorded`, and `RequestResolved`. A verification receipt binds the
dependency event, TrustRoot update, final home TrustRoot, leaf index, request,
and source tuple before the corresponding MembershipWitness and TrustEdge are
created.

The executor is the only automatic verifier transaction sender for its home
chain and submits at most one verification transaction per block. This avoids
multiple intermediate TrustRoots for the same `(chainId,height,blockHash)` in
the target experiment. The limitation is explicit and is not presented as a
general throughput solution.

## Reorg Policy

Only confirmed blocks are materialized. `CanonicalCursor` stores processed
height and hash. Before advancing, the Indexer rechecks the persisted hash.

- If it still matches, indexing continues.
- If it differs, MapNode enters a persisted degraded state and stops planning
  and submitting transactions.
- The experiment prototype does not automatically find a common ancestor or
  roll back a deep reorg.

Geth developer-mode experiments do not intentionally create reorgs. Detection
and fail-closed behavior preserve correctness without adding unrelated recovery
machinery.

## Minimal Dependency Evidence Gossip

The real libp2p host uses the topology-generated persistent Ed25519 identity and
bootstrap multiaddresses. One versioned GossipSub topic carries only a
`DependencyEvidenceEnvelope` containing:

- protocol version and deterministic message ID;
- origin peer ID and observation timestamp;
- recording chain ID, Gateway address, block number and block hash;
- transaction hash, transaction index, log index, and payload digest.

The receiver deduplicates by message ID, stores the envelope as candidate input,
and performs remote RPC validation. Peer identity is used only for provenance.
There is no proof-material stream, peer scoring, network-wide blacklist,
dynamic discovery, or complex rate control in this phase.

## Remote Dependency Evidence Validation

For every received locator, the validator checks:

- configured chain ID and Gateway code hash;
- canonical block number and hash;
- confirmation depth;
- successful transaction receipt;
- receipt block hash and transaction index;
- exact Gateway address and `DependencyRecorded` topic/data decoding;
- request ID, dependency key, source tuple, source TrustRoot, and leaf index;
- deterministic payload digest;
- matching `TrustRootUpdated` and `RequestResolved` receipt logs;
- locally reconstructed MembershipWitness closes to the recorded home
  TrustRoot.

Only after all checks pass does the existing Evidence lifecycle reach `active`
and `MergeActiveTrustEdge` run.

## Live Request Worker and Executor

`VerificationRequested` is the primary request entry. The worker:

1. obtains confirmed home and source TrustRoot observations;
2. creates an immutable TrustViewSnapshot;
3. runs the existing calibrated Planner;
4. builds and locally verifies a PathProof for PathPlan;
5. constructs a direct proof for DirectPlan;
6. serializes one transaction through the home account;
7. persists the signed raw transaction, nonce, hash, receipt, and status;
8. waits for the configured confirmation depth before marking the request
   confirmed.

For DirectPlan, MapNode calls the deployed verifier's `attestationDigest`, signs
the returned digest with the ordered deployment-authorized experimental signer
keys, ABI-encodes `(sourceTrustRoot, signatures[])`, and calls
`verifyDirectAndRecord`.

For PathPlan, MapNode calls `verifyPathAndRecord` with the persisted
snapshot-bound PathProof.

The transaction worker is single-threaded. It uses the pending nonce and
persists the signed raw transaction before broadcast. Restart recovery checks
the saved receipt, transaction, and `requestResolved(requestId)` state before
rebroadcasting. Fee replacement and parallel nonce allocation are out of scope.

Phase 4 extends the durable request lifecycle with `Submitted` and `Confirmed`.
Transient RPC failure can enter `Retryable`; stale Home TrustRoot must replan.
No transition may treat transaction broadcast as verification success.

## Minimal API and Experiment Driver

The operational API exposes only:

- liveness/readiness/degraded state;
- request state and transaction hash;
- current TrustView nodes, edges, and revision.

An experiment script, not the API, submits `requestVerification` transactions.
This keeps the on-chain event as the primary entry and avoids a second nonce
control path.

## Durable Schema Additions

A new append-only migration adds the minimum live tables:

- chain catalog and lazy validation status;
- canonical block cursor and degraded state;
- `trust_root_observations`;
- P2P inbox/outbox deduplication rows;
- transaction submissions and receipts;
- Phase 4 request transitions.

Existing migrations are immutable. New tables use strict length/type checks,
foreign keys, content identities, and restart tests. Derived rows must bind back
to exact Evidence, request, snapshot, plan, and proof identities.

## Required Experiments

### Two-chain direct checkpoint

1. Start A and B plus their MapNodes.
2. Submit a B Gateway request for a confirmed A block.
3. B observes A's historical TrustRoot.
4. Planner chooses DirectPlan and the experimental signatures are verified on
   chain.
5. B indexes its new DependencyRecorded event and gossips the locator.
6. A validates the locator through B RPC before accepting the edge.

### Three-chain path checkpoint

1. Establish `B -> A` by DirectPlan.
2. Establish `C -> B` by DirectPlan.
3. Wait until C has RPC-validated the gossiped `B -> A` Evidence.
4. Request A from C.
5. Planner chooses `C -> B -> A`.
6. Builder produces `base=A.TrustRoot`, `hashes=[A,B]`, and
   `witnesses=[wBA,wCB]`.
7. C Gateway accepts `verifyPathAndRecord`, resolves the request, and emits a
   new dependency.

### Recovery checkpoint

- restart after indexing but before planning;
- restart after raw transaction persistence but before receipt observation;
- duplicate P2P delivery;
- fabricated P2P locator with a valid peer identity;
- historical Home TrustRoot changed before submission;
- canonical cursor hash mismatch enters degraded state.

## Explicitly Deferred

- WebSocket subscriptions and reconnect logic;
- provisional event state and automatic reorg rollback;
- deep-reorg repair;
- proof-material P2P streams;
- peer scoring and production denial-of-service controls;
- dynamic chain or peer discovery;
- multiple concurrent home-chain transactions;
- replacement transactions and advanced fee policy;
- a general user-facing request API;
- live startup of all 21 chains.

These omissions affect availability, throughput, or operations. They must not be
used to bypass Evidence verification or change measured contract verification
costs.

## Quality Gates

- ABI selectors/topics and event decoding match Solidity golden values.
- TrustRootObservation cannot create a TrustEdge.
- unordered, unconfirmed, wrong-chain, wrong-code, reverted-receipt, and forged
  P2P Evidence are rejected.
- cursor and transaction recovery are restart-safe and idempotent.
- Planner still uses the deployment-matched measured direct cost.
- Direct and Path transactions are accepted by real Geth-hosted contracts.
- the three-chain experiment proves the path was used rather than direct
  fallback.
- `go test -race ./...`, `go vet ./...`, CGO-disabled builds, container tests,
  and migration upgrade tests pass.
- 21-chain topology remains statically valid without launching 42 containers.

