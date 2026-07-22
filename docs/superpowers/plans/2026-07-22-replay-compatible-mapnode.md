# Replay-Compatible MapNode Implementation Plan

> **Execution rule:** implement each stage test-first, preserve the existing live
> MapNode trust boundary, and stop at the listed verification checkpoint before
> moving to the next stage.

**Goal:** Add a real `mapnode replay` mode that reproduces the legacy B0--B3
cost-estimation experiment while keeping `mapnode serve` and its evidence-backed
`TrustView` unchanged.

**Design source:**
`docs/superpowers/specs/2026-07-22-replay-compatible-mapnode-design.md`.
The paper and `TrustMap-ETH` remain read-only references.

**Implementation boundary:** replay code lives under `Mapnode/replay`, uses its
own SQLite database and `ReplayTrustView`, and never writes replay-only edges to
the live store. The existing invocation `mapnode --config ...` remains an alias
for `mapnode serve --config ...`.

## Stage 1: Compatibility foundation

Implement the input and configuration layer that makes the legacy experiment a
deterministic test oracle.

Primary files:

- `Mapnode/replay/replay_event.go`
- `Mapnode/replay/replay_trace.go`
- `Mapnode/replay/replay_block.go`
- `Mapnode/replay/replay_cost_profile.go`
- `Mapnode/replay/replay_checkpoint_policy.go`
- `Mapnode/replay/config.go`
- matching `_test.go` files
- `tests/fixtures/replay/msg-small.csv`
- `tests/fixtures/replay/checkpoint-periods-small.json`

Test-first requirements:

- parse the legacy CSV columns without rewriting the source file;
- preserve the original row number and original heights;
- normalize each chain by its minimum observed height, without rank
  compression;
- sort by the exact legacy key, with original row as the final stable tie-break;
- validate B0--B3, exact legacy and calibrated integer profiles, checkpoint
  policy, input hash and output directory isolation;
- reject overflow-prone, negative or internally inconsistent configurations.

Checkpoint:

```bash
go test ./Mapnode/replay -run 'Test(Trace|Config|CostProfile|Checkpoint)' -count=1
```

Commit: `feat: add deterministic replay compatibility foundation`

## Stage 2: ReplayTrustView and planning engine

Implement the replay-only graph, baseline state and the event transition. Use an
internal deterministic ordered height index so full traces do not require a new
network dependency.

Primary files:

- `Mapnode/replay/replay_trust_view.go`
- `Mapnode/replay/replay_intra_chain_edge.go`
- `Mapnode/replay/replay_verified_dependency_edge.go`
- `Mapnode/replay/replay_baseline.go`
- `Mapnode/replay/replay_path.go`
- `Mapnode/replay/replay_planner.go`
- `Mapnode/replay/replay_coordinator.go`
- matching `_test.go` files

Test-first requirements:

- create asymmetric intra-chain edges: upward `directStepCost`, downward
  `pathStepCost`, each multiplied by the height delta;
- accept ordinary same-chain relations such as `C:201 -> C:200`;
- add cross-chain dependency edges at `pathStepCost` only after deciding the
  current event;
- compute direct estimates from baseline height, including checkpoint starts;
- run deterministic Dijkstra over mixed intra-chain and dependency edges;
- select TrustMap only when `pathCost + trustRootUpdateCost < directCost`;
- update baselines monotonically after each decision;
- make B0/B1 disable TrustMap planning and B2/B3 enable it while keeping each
  run's state isolated;
- reproduce hand-calculated fixture decisions and path costs exactly.

Checkpoint:

```bash
go test ./Mapnode/replay -run 'Test(ReplayTrustView|Baseline|Planner|Coordinator)' -count=1
go test -race ./Mapnode/replay -count=1
```

Commit: `feat: implement replay TrustView and cost planner`

## Stage 3: Durable replay runs, exports and CLI

Add a replay-specific SQLite/WAL repository, restart recovery, legacy-compatible
exports, audit exports and the explicit CLI mode. Keep the live schema and live
startup path untouched.

Primary files:

- `Mapnode/replay/replay_repository.go`
- `Mapnode/replay/replay_export.go`
- `Mapnode/replay/run.go`
- matching `_test.go` files
- `cmd/mapnode/replay_command.go`
- updates to `cmd/mapnode/main.go` and command tests

Test-first requirements:

- initialize only a replay database beneath the configured run directory;
- atomically persist decision, baseline, cross-edge and progress state;
- resume after interruption without duplicating rows;
- rebuild the derived in-memory graph from completed rows deterministically;
- export the legacy filenames and columns plus separate extended audit files and
  `run_manifest.json`;
- report configured and effective checkpoint-chain counts independently;
- preserve `mapnode --config`, `mapnode serve --config` and healthcheck behavior;
- make `mapnode replay --config` finite, resumable and non-transactional with
  respect to Geth (no per-message RPC submission).

Checkpoint:

```bash
go test ./Mapnode/replay ./cmd/mapnode -count=1
go test -race ./Mapnode/replay ./cmd/mapnode -count=1
```

Commit: `feat: add durable mapnode replay command`

## Stage 4: Experimental entry points and end-to-end validation

Package repeatable small and full experiment entry points, document their
relationship to live validation, and compare the exact profile against the
read-only legacy golden results.

Primary files:

- `configs/replay/profiles/legacy-v4.1.yaml`
- `configs/replay/profiles/prototype-calibrated.yaml`
- `configs/replay/smoke-3chain.yaml`
- `configs/replay/full-21chain.yaml`
- `scripts/replay-smoke.sh`
- `scripts/replay-full.sh`
- `tests/integration/replay_smoke_test.sh`
- replay documentation under `docs/`
- focused updates to the root `README.md`

Validation requirements:

- smoke replay covers three chains and B0--B3 without Geth;
- existing live two-chain Direct and three-chain Path tests continue to prove
  the real contract/MapNode closed loop;
- full exact replay consumes the local 245,000-row, 21-chain input and matches
  the legacy aggregates, including B2 `111058` and B3 `91829` TrustMap
  selections;
- calibrated replay is emitted as a separate sensitivity result and is never
  labelled an exact reproduction;
- exported input hash, event count, chain count and final graph statistics are
  checked;
- scripts accept configuration overrides and do not hard-code a three-chain
  limit.

Final checkpoint:

```bash
go test ./... -count=1
go test -race ./Mapnode/... ./cmd/mapnode -count=1
go vet ./...
go build ./cmd/mapnode ./cmd/topologygen
bash tests/integration/replay_smoke_test.sh
# With the local full trace configured:
bash scripts/replay-full.sh --profile legacy-v4.1
```

Run a specification review and a code-quality review after implementation, fix
all material findings, re-run the final checkpoint, then commit the verified
documentation and integration surface.

Commit: `feat: complete replay experiment workflow`
