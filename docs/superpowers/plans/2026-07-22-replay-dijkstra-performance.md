# Replay Dijkstra Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove per-search distance/predecessor map allocation from replay
Dijkstra while preserving every legacy path, decision, cost, cutoff, pruning,
and output digest.

**Architecture:** `ReplayTrustView` assigns stable internal integer IDs to
canonical block keys while retaining key-based adjacency as the graph
authority. `ShortestPath` obtains a private pooled scratch object whose
generation-tagged slices reuse distance and predecessor state without clearing
the full graph. Queue ordering and reconstruction continue to use canonical
keys, so IDs cannot affect observable ordering.

**Tech Stack:** Go 1.24, `container/heap`, `sync.Pool`, generation-tagged slices,
existing replay integration/golden tooling.

---

## File map

- `Mapnode/replay/replay_trust_view.go`: graph mutation, stable key/ID tables,
  and graph counters.
- `Mapnode/replay/replay_dijkstra.go`: queue, reusable scratch, shortest path,
  and path reconstruction.
- `Mapnode/replay/replay_dijkstra_test.go`: legacy reference implementation,
  semantic equivalence, generation, concurrency, and allocation tests.
- `Mapnode/replay/replay_trust_view_test.go`: activation/ID invariants that
  belong to graph mutation rather than search.
- `98-材料/实验记录/2026-07-22-full-replay.md`: ignored performance evidence.

### Task 1: Freeze the pre-optimization search semantics

**Files:**
- Create: `Mapnode/replay/replay_dijkstra_test.go`
- Modify: `Mapnode/replay/replay_trust_view_test.go`

- [ ] **Step 1: Add a test-only legacy reference Dijkstra**

Copy the current map-based algorithm into a test helper named
`referenceReplayShortestPath`. Keep its map distances, map predecessors,
`(cost, chain, height)` heap comparator, strict `candidate < known`, checked
addition, cutoff handling, target-chain pruning, and key-based reconstruction.

- [ ] **Step 2: Add deterministic comparison fixtures**

Build fixed mixed-chain graphs covering same-chain up/down edges, verified
dependencies, equal-cost ties, unreachable goals, exact cutoff, over-cutoff,
target-chain pruning, start equal to goal, and checked-add overflow. Compare
`ReplayPath` node-by-node and segment-by-segment plus found/error state.

- [ ] **Step 3: Add generated mixed-graph comparison**

Use a fixed PRNG seed and only deterministic graph construction. For each
query compare the production path against the reference path exactly, not only
total cost. Keep graph size small enough for normal unit tests.

- [ ] **Step 4: Run the tests before production changes**

```sh
go test ./Mapnode/replay -run 'TestReplayDijkstraReference|TestReplayDijkstraGenerated' -count=1
```

Expected: PASS, proving the reference describes current behavior before the
optimization. Commit: `test: freeze replay Dijkstra semantics`.

### Task 2: Add stable internal node IDs

**Files:**
- Modify: `Mapnode/replay/replay_trust_view.go`
- Modify: `Mapnode/replay/replay_trust_view_test.go`

- [ ] **Step 1: Write failing stable-ID invariants**

Assert canonical duplicate activation reuses the ID, new canonical keys receive
monotonically increasing IDs, split insertion does not change prior IDs, failed
activation caused by cost overflow publishes no key/ID/node, and every
production adjacency endpoint resolves to its canonical ID.

Expected RED: `ReplayTrustView` has no key-to-ID or ID-to-key tables.

- [ ] **Step 2: Add the ID tables**

Use an internal integer ID type and these fields:

```go
nodeIDs  map[ReplayBlockKey]replayNodeID
nodeKeys []ReplayBlockKey
```

Initialize them in `NewReplayTrustView`. Assign an ID only in the committed
activation section after all fallible edge-cost calculations succeed. Never
reuse IDs. Preserve `nodes`, key-based adjacency, `NodeCount`, edge counters,
and all persisted/exported structures exactly.

- [ ] **Step 3: Run focused and package tests**

```sh
go test ./Mapnode/replay -run 'StableNodeID|SplitsIntraChain|CachedEdgeCount' -count=1
go test ./Mapnode/replay -count=1
```

Expected: PASS. Commit: `perf: assign stable replay node IDs`.

### Task 3: Replace per-search maps with reusable generation state

**Files:**
- Create: `Mapnode/replay/replay_dijkstra.go`
- Modify: `Mapnode/replay/replay_trust_view.go`
- Modify: `Mapnode/replay/replay_dijkstra_test.go`

- [ ] **Step 1: Write failing scratch-state tests**

Require a scratch object with reusable distance/predecessor/marker slices and
heap backing storage. Test stale entries are invisible in the next generation,
growth preserves current state, generation wrap clears both marker arrays and
restarts at one, and active concurrent searches never share scratch.

- [ ] **Step 2: Implement isolated pooled scratch**

Add a `sync.Pool` to `ReplayTrustView`. Each scratch contains:

```go
generation            uint64
distances              []uint64
distanceGenerations    []uint64
predecessors           []replayNodeID
predecessorGenerations []uint64
queue                  replayPriorityQueue
```

On begin, grow slices to `len(nodeKeys)`, advance a non-zero generation, clear
markers only on `math.MaxUint64` wrap, and reset the queue length while retaining
capacity. Put scratch back with `defer` on every success/error path.

- [ ] **Step 3: Move and optimize Dijkstra**

Move queue/search/reconstruction code from `replay_trust_view.go` into
`replay_dijkstra.go`. Queue items carry node ID, canonical key, and cost; `Less`
remains exactly `(cost, chain, height)`. Use IDs only to index scratch. Iterate
the existing key adjacency, map neighbour keys through the stable table, and
preserve all old conditions in the same order: stale queue check, over-cutoff
return, current-node pruning, goal detection, checked addition, candidate cutoff,
neighbour pruning, and strict relaxation.

Reconstruct predecessor IDs through `nodeKeys`, then read kind/weight from the
existing key adjacency. Preserve start-equals-goal behavior before any ID
lookup. Missing internal IDs return explicit invariant errors rather than
silently changing the result.

- [ ] **Step 4: Run semantic, race, and allocation gates**

```sh
go test ./Mapnode/replay -run 'ReplayDijkstra|ShortestPath|TargetChain' -count=1
go test -race ./Mapnode/replay -run 'ReplayDijkstraConcurrent' -count=1
go test ./Mapnode/replay -count=1
```

The reference comparison must remain exact. Add an allocation comparison after
warming the pool and require the optimized search to allocate fewer objects
than the test-only reference on a fixed graph; do not assert a machine-specific
absolute count. Commit: `perf: reuse replay Dijkstra search state`.

### Task 4: Repository and 30k semantic gate

**Files:**
- Modify only if verification exposes a defect in the files above.
- Update ignored: `98-材料/实验记录/2026-07-22-full-replay.md`

- [ ] **Step 1: Run repository verification**

```sh
go test ./... -count=1
go test -race ./Mapnode/replay -count=1
go vet ./...
git diff --check
```

- [ ] **Step 2: Build one fixed optimized binary**

Build `./cmd/mapnode` under ignored `runtime/bin/`, record commit and binary
SHA-256, and use that same binary for the probe.

- [ ] **Step 3: Run the identical B2 30,000-event probe**

Use the repository-owned canonical trace, the same legacy profile/configuration
and first-30,000 subset procedure as baseline `47a59d2`. Record `/usr/bin/time
-v`, output directory, row counts, graph counts, and digests.

Required semantic values:

```text
decision digest = a9e38c664c4960533d760692f394f4dea1627c3342a9ea65d3600eada6bb5ffc
path digest     = ad918ad9802fdea1940801b8b5fe7ef71f88b9fd77c622356fe1e27b63435f61
decision rows   = 30000
path rows       = 13251
graph           = 54687 nodes / 139336 edges / 30000 cross additions
```

Any semantic mismatch rejects the optimization. Wall time, CPU time and peak
RSS are recorded for reproducibility but are not acceptance criteria. There is
no minimum speedup requirement and measured performance does not block the full
run.

- [ ] **Step 4: Record the checkpoint**

Append commit/binary/input hashes, exact command, time/RSS, output paths,
digests, and the semantic pass/fail decision to the ignored experiment record.
Only a semantically passing probe authorizes the sequential B0--B3 full run.
