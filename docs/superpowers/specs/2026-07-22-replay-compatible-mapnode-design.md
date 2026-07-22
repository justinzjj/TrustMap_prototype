# Replay-Compatible MapNode Design

**Status:** Approved by the user on 2026-07-22 and ready for implementation.

## 1. Purpose

TrustMap Prototype must provide both:

1. a real, usable MapNode that observes Geth, validates chain evidence, maintains a
   durable evidence-backed `TrustView`, plans executable `DirectPlan` or
   `PathPlan`, and submits real Gateway transactions; and
2. a deterministic replay mode in the same Go MapNode codebase that reproduces
   the cost-estimation semantics of the original December 2025 B0--B3
   simulation over the 245,000-message, 21-chain trace.

Replay mode is an experimental evaluation mode. It estimates verification work
and does not submit one transaction per trace row. Live mode remains the source
of evidence that the contracts, proofs, P2P validation, and MapNode execution
loop work on real Geth EVM chains.

The two modes share domain types, configuration conventions, deterministic
planning foundations, persistence discipline, and result identities. They do
not share a trust boundary: replay-only intra-chain planning edges must never be
inserted into the live evidence-backed `TrustView` or treated as PathProof
membership witnesses.

## 2. Sources of Truth and Compatibility Baseline

The paper remains the mechanism-level source of truth. The following existing
files are read-only evaluation references:

- `TrustMap-ETH/Dune/baseline_sim_no_trustmap.py` for B0 and B1;
- `TrustMap-ETH/Dune/trustmap_sim.py` for B2;
- `TrustMap-ETH/Dune/trustmap_sim_checkpoint.py` for B3;
- `TrustMap-ETH/Dune/output_202512/msg.csv` for the full trace;
- `TrustMap-ETH/Dune/result_202512_v4_1_gas/` for golden outputs.

Neither the old repository nor the paper repository may be modified by this
work. The full trace remains a local, configurable input and is not copied into
Git unless the user separately decides its release policy.

The full input currently has:

- 245,000 data rows;
- 21 chain names;
- SHA-256
  `ae35b6fd51185822dcfd8e338b2af43735bb2141764feead9ec993633a232175`.

The primary exact-compatibility profile is:

```yaml
id: legacy-v4.1
directStepCost: 3000000
pathStepCost: 30000
trustRootUpdateCost: 110000
```

Its golden aggregate results include:

- B2 TrustMap selections: 111,058 / 245,000 (`0.4532979591836735`);
- B3 TrustMap selections: 91,829 / 245,000 (`0.37481224489795917`);
- final B2/B3 graph: 453,949 nodes, 1,152,851 unique directed
  adjacency edges, and 245,000 cross-edge additions.

A second sensitivity profile uses the current prototype calibration:

```yaml
id: prototype-calibrated
directStepCost: 3000096
pathStepCost: 30713
trustRootUpdateCost: 108582
```

The compatibility profile is the regression oracle. The calibrated profile is
reported separately and is not expected to reproduce every legacy decision.

## 3. One Codebase, Two Explicit Modes

The MapNode binary exposes two explicit operating modes:

```text
mapnode serve  --config <live-topology-generated-config>
mapnode replay --config <replay-config>
```

The existing invocation remains backward-compatible with `serve`. Replay mode
is implemented under `Mapnode/replay`; it is not a Python sidecar and does not
replace the live MapNode.

### 3.1 Live mode

Live mode keeps the existing rules:

- `TrustView` contains only chain-observable, validated evidence;
- a `TrustEdge` requires a successful canonical Gateway receipt,
  `DependencyRecorded`, exact TrustRoot observations, and membership material;
- P2P only discovers evidence and cannot authorize an edge;
- plans must be executable by the deployed Gateway;
- only a confirmed successful receipt plus `requestResolved` and the indexed
  event bundle enters `Confirmed`.

### 3.2 Replay mode

Replay mode:

- consumes trace metadata only;
- processes events in deterministic trace order with zero wall-clock waiting;
- uses logical block coordinates and a replay-only graph;
- estimates Direct and mixed-path costs;
- updates logical baseline/checkpoint state;
- adds a logical verified-dependency edge only after the current row is treated
  as successfully processed, matching the original simulator;
- writes durable, restartable experiment results;
- performs no per-row contract call and makes no light-client security claim.

## 4. Trace Preparation

### 4.1 Required and optional columns

Required input columns are:

```text
src_chain
dst_chain
src_block_number
dst_block_number
src_block_time
```

Compatibility metadata columns are optional and receive the same defaults as
the old scripts:

```text
bridge_name  -> ""
tx_count     -> 1
volume_usd   -> 0.0
```

### 4.2 Normalization

Preparation performs the legacy operations in the legacy order:

1. trim and lowercase source/destination chain names;
2. remove the literal suffix ` UTC` and parse `src_block_time` as UTC;
3. parse source/destination heights as exact non-negative integers;
4. parse optional numeric metadata;
5. reject or drop invalid rows according to an explicit compatibility policy;
6. sort the valid rows;
7. assign the prepared sequence index;
8. compute per-chain initial heights;
9. compute normalized heights and stable event identities.

The legacy sort keys are:

```text
src_block_time ASC
dst_chain ASC
dst_block_number ASC
src_chain ASC
src_block_number ASC
```

`original_row ASC` is added only as a final stable tie-breaker. The current full
trace has five two-row groups with equal legacy sort keys. Their source,
destination, and block coordinates are identical, so the tie-breaker stabilizes
metadata ordering without changing graph or cost semantics.

### 4.3 Initial and normalized heights

For every chain `c` in the prepared workload:

```text
initHeight[c] = min(
    all src_block_number where src_chain == c,
    all dst_block_number where dst_chain == c
)

normalizedHeight(c, h) = h - initHeight[c]
```

This is a per-chain translation, not rank compression. It preserves all
same-chain differences and therefore preserves Direct, checkpoint, and
intra-chain edge costs. Original heights remain the compatibility output and
identity reference; normalized heights are audit fields and internal compact
coordinates.

### 4.4 Event identity and run identity

Every event receives a domain-separated SHA-256 identity over:

- trace digest;
- original row number;
- prepared sequence number;
- canonical source/destination chain names;
- original source/destination heights;
- canonical source time.

A run identity binds:

- prepared trace digest and row count;
- replay implementation/schema versions;
- Git commit;
- setting B0/B1/B2/B3;
- cost-profile content and fingerprint;
- checkpoint configuration fingerprint;
- allowlist/limit selection;
- record/log intervals.

Resume fails closed if any identity-bound input changes.

## 5. B0--B3 Strategy Matrix

One prepared trace is shared by four isolated replay states:

| Setting | ReplayTrustView planning | Checkpoint-assisted Direct |
|---|---:|---:|
| B0 | disabled | disabled |
| B1 | disabled | enabled |
| B2 | enabled | disabled |
| B3 | enabled | enabled |

One matrix command may execute all settings, but it must never share mutable
baseline, graph, decision, or progress state across settings.

## 6. ReplayTrustView

Replay mode uses types whose names make the paper mechanisms and the security
boundary visible:

```text
ReplayBlock
ReplayBlockKey
ReplayTrustView
ReplayIntraChainEdge
ReplayVerifiedDependencyEdge
ReplayPath
```

It does not reuse live `trustview.TrustEdge` for ordinary chain ancestry.

### 6.1 Nodes

A replay node is identified by canonical chain name plus original height and
also records the normalized height. The same `(chain, originalHeight)` always
maps to the same node.

### 6.2 Edge kinds and weights

```text
ReplayIntraChainUpEdge:
    (higherHeight - lowerHeight) * directStepCost

ReplayIntraChainDownEdge:
    (higherHeight - lowerHeight) * pathStepCost

ReplayVerifiedDependencyEdge:
    pathStepCost
```

`path_len` is a reported node count and is not multiplied into the cost a
second time. The replay path cost is the exact sum of edge weights.

### 6.3 Ordered height insertion

Each chain maintains an online predecessor/successor index over heights that
have appeared at or before the current event. Future heights must not affect
the active graph.

When inserting `prev < current < next`:

1. delete `prev -> next` and `next -> prev` if present;
2. add the two directed `prev <-> current` edges with asymmetric weights;
3. add the two directed `current <-> next` edges with asymmetric weights.

The full workload is too large for repeated middle insertion into ordinary Go
slices. The implementation uses a tested deterministic ordered index with
predecessor/successor operations while keeping the active adjacency graph in
memory.

### 6.4 Cross-edge compatibility

After each processed event, replay adds:

```text
(dst_chain, dst_block_number)
    -> (src_chain, src_block_number)
```

with `pathStepCost`. As in the Python dictionary, the unique adjacency entry is
replaced if the same endpoints already exist. The compatibility counter
`edges_cross_added` increments once per processed row even when the adjacency
entry already existed.

## 7. Baseline and Direct Cost

Baseline state is keyed by verifier/destination chain then source/target chain:

```text
baseline[verifierChain][sourceChain]
```

It initializes lazily to `initHeight[sourceChain]` and advances monotonically:

```text
baselineAfter = max(baselineBefore, sourceHeight)
```

Without checkpointing:

```text
if sourceHeight >= baseline:
    directCost = (sourceHeight - baseline) * directStepCost
else:
    directCost = (baseline - sourceHeight) * pathStepCost
```

The value is estimated verification work. Replay does not cause the
`ExperimentalCostedDirectVerifier` to consume that many gas units.

## 8. Checkpoint Policy

Checkpoint periods are explicit configuration; replay has no hidden chain
defaults. For an upward request on a supported source chain:

```text
checkpointHeight = floor(sourceHeight / period) * period
directStart       = max(baseline, checkpointHeight)
directCost        = (sourceHeight - directStart) * directStepCost
```

For a historical/downward request, B3 preserves the current checkpoint
simulator rule and does not move the start forward with a checkpoint:

```text
directCost = (baseline - sourceHeight) * pathStepCost
```

The published v4.1 B1 configuration has 18 checkpoint-enabled workload chains.
The B3 implementation merged additional placeholder defaults and therefore
reported 27 configured keys, although only 18 intersect the 21-chain workload.
Replay preserves the effective decisions and reports both:

```text
checkpoint_configured_chains
checkpoint_effective_workload_chains
```

The misleading configured-key count is not used as the paper's workload
support count.

## 9. Replay Planner

For B2/B3 and a trace row `A@H2 -> B@H1` in the original notation:

```text
start = (B, H1)
goal  = (A, H2)
```

The event performs these operations in order:

1. activate the start and goal replay nodes and repair neighboring intra-chain
   edges;
2. read `baseline[B][A]`;
3. estimate Direct cost;
4. compute `cutoff = directCost - trustRootUpdateCost`;
5. if cutoff is positive, run Dijkstra over the graph as it exists before this
   event's cross edge;
6. choose replay TrustMap only when
   `pathCost + trustRootUpdateCost < directCost`;
7. record the decision and path audit;
8. update the baseline monotonically;
9. add the current event's replay verified-dependency edge;
10. commit progress and results.

Strict `<` is compatibility behavior and must not become `<=`.

B0/B1 preserve the legacy baseline-only implementation: they estimate Direct,
advance the baseline, and persist the event transition, but do not materialize
an unused `ReplayTrustView` adjacency graph. This keeps full baseline runs
bounded without changing any B0/B1 decision or output; graph construction and
the ordered event sequence above apply to the TrustMap-enabled B2/B3 settings.

### 9.1 Dijkstra compatibility

The implementation preserves:

- non-negative integer edge weights;
- Direct-derived cutoff;
- legacy target-chain low-height pruning;
- no replacement when the new distance equals the known distance;
- priority ordering by distance, canonical chain name, then numeric height;
- exact path reconstruction and per-segment audit.

All observed legacy costs are below `2^53`; Go nevertheless uses checked
`uint64` arithmetic so primary results are exact integers rather than floats.
The legacy CSV exporter renders compatible decimal forms where required.

## 10. Persistence and Recovery

Replay uses a separate SQLite/WAL database under the run directory. It never
opens or mutates a live MapNode database.

The replay schema stores at least:

```text
replay_runs
replay_events
replay_progress
replay_baselines
replay_decisions
replay_paths
replay_cross_edges
replay_snapshots
```

One event transaction persists:

- its unique completion identity;
- decision and compatibility fields;
- a domain-separated event-and-decision integrity digest;
- path and path-audit fields when selected;
- baseline before/after;
- the logical cross-edge addition;
- graph counters;
- the last completed sequence.

The in-memory ordered height index and adjacency graph are derived state.
Restart verifies the run identity, then deterministically rebuilds them by
applying completed events without rerunning plan selection. It resumes at the
first incomplete sequence. Exact replay of a completed event is idempotent;
conflicting content is fatal. Recovery verifies the independent integrity
digest, redundant scalar columns, derived path/cross-edge/snapshot rows, and
the semantic state transition before applying each committed decision.

CSV files are exported from committed database rows rather than appended as
the transaction authority. A crash cannot leave a half-committed decision.

## 11. Results and Compatibility Exports

Replay produces the legacy filenames:

```text
decisions.csv
paths.csv                 # B2/B3
summary.json
map_snapshots.csv         # when recordEvery > 0
init_heights.csv
checkpoint_periods.csv    # B1/B3
```

Legacy columns retain their names and meanings, including:

```text
idx, time, src_chain, dst_chain,
src_block_number, dst_block_number,
bridge_name, tx_count, volume_usd,
baseline_before, baseline_after,
direct_cost, trust_cost,
trustmap_update_cost, trust_cost_total,
decision, chosen_cost, saving,
path, path_len
```

Extended audit data is exported separately so existing analysis scripts do not
silently reinterpret columns:

```text
decisions_extended.csv
paths_extended.csv
run_manifest.json
progress.json
replay.db
```

Extended fields include:

```text
event_id, original_row,
normalized_src_height, normalized_dst_height,
path_hops, path_xchain_hops, path_height_steps,
path_jump_segments, path_cost_check, path_segments_json,
cost_profile_id, cost_profile_fingerprint
```

Snapshot compatibility preserves the legacy `i % recordEvery == 0` behavior,
including a snapshot after event index zero.

## 12. CLI, Configuration, and Scripts

Replay configuration is strict YAML and includes:

- input trace path and optional expected digest;
- result/run root;
- setting or matrix selection;
- cost profile;
- checkpoint periods;
- optional chain allowlist;
- optional maximum prepared event count;
- record/log intervals;
- durability settings with safe defaults.

Repository examples are:

```text
configs/replay/smoke-3chain.yaml
configs/replay/full-21chain.yaml
configs/replay/profiles/legacy-v4.1.yaml
configs/replay/profiles/prototype-calibrated.yaml
configs/replay/checkpoints-202512.yaml
```

Operator entry points are:

```text
scripts/replay-smoke.sh
scripts/replay-full.sh
```

Representative commands are:

```text
mapnode replay --config configs/replay/smoke-3chain.yaml --setting B2
mapnode replay --config configs/replay/full-21chain.yaml --setting all
```

`--setting all` executes the settings listed by the configuration; a concrete
setting selects only that member of the matrix. Resume and export deliberately
remain part of the same finite command in this prototype: rerunning an
identical configuration and run root validates and restores committed SQLite
state, completes any remaining events, and regenerates exports atomically.
Separate `resume` and `export` subcommands add operator surface without changing
the experiment semantics, so they are omitted.

The smoke configuration filters to events whose source and destination are both
inside the allowlist and then applies its event limit. Initial heights are
computed from that prepared smoke workload, matching the semantics of running
the old scripts on the same filtered input. Full mode has no filter or limit.

## 13. Testing and Acceptance

### 13.1 Unit and property gates

- trace normalization and sorting golden tests;
- original/normalized height-difference equivalence;
- predecessor/successor edge-splitting tests;
- upward/downward/cross-edge weight tests;
- monotonic baseline tests;
- B0/B1/B2/B3 checkpoint tests;
- Dijkstra cutoff, pruning, tie, and path reconstruction tests;
- no-future-edge tests;
- checked-arithmetic failure tests;
- event-transaction and restart-idempotence tests;
- deterministic export tests.

### 13.2 Legacy compatibility gates

A committed small fixture and expected B0--B3 results are independent of the
old repository. The implementation must match them row for row.

When the local full trace and golden outputs are available, the server/full
compatibility gate requires for `legacy-v4.1`:

```text
decision mismatches                 = 0
direct/chosen integer mismatches    = 0
aggregate cost mismatches           = 0
graph counter mismatches            = 0
B2 TrustMap rows                    = 111058
B3 TrustMap rows                    = 91829
```

Exact path strings are also compared. If a legacy equal-cost path is sensitive
to historical container ordering, acceptance still requires equal path cost,
edge-kind counts, and selected decision, and the exception must be enumerated
rather than silently ignored.

### 13.3 Scale and live gates

- CI runs unit tests and a bounded three-chain replay fixture;
- a restart test compares uninterrupted and resumed output digests;
- a local/manual full gate reads all 245,000 rows and all 21 chains;
- the full workload is not required in ordinary CI;
- existing real two-chain Direct and three-chain Path experiments remain green;
- replay additions must not weaken live database, P2P, Evidence, TrustRoot,
  TrustView, PathProof, or transaction-confirmation invariants.

## 14. Explicit Non-Goals

- replaying original message payloads or application semantics;
- mining local Geth to the original public-chain heights;
- treating normalized heights as physical Geth heights;
- submitting 245,000 real Gateway transactions for the primary trace replay;
- treating replay intra-chain edges as evidence-backed live TrustEdges;
- claiming estimated verification work is measured transaction gas;
- changing the old repository or paper content in this task;
- hiding differences between the legacy and calibrated profiles.

## 15. Planned Source Layout

The implementation is expected to add focused files under:

```text
Mapnode/replay/
  replay_event.go
  replay_trace.go
  replay_block.go
  replay_trust_view.go
  replay_intra_chain_edge.go
  replay_verified_dependency_edge.go
  replay_cost_profile.go
  replay_baseline.go
  replay_checkpoint_policy.go
  replay_planner.go
  replay_path.go
  replay_coordinator.go
  replay_repository.go
  replay_export.go

configs/replay/
scripts/replay-smoke.sh
scripts/replay-full.sh
tests/fixtures/replay/
```

Exact file splitting may change during the implementation plan, but key
mechanism names (`ReplayTrustView`, `ReplayIntraChainEdge`, baseline,
checkpoint, and cost profile) must remain visible in paths and exported Go
types.
