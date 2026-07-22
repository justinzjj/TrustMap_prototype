# Replay experiments

`mapnode replay` is the experiment-facing mode of the same MapNode binary. It
replays historical cross-chain messages through a replay-only `ReplayTrustView`
and estimates Direct and TrustMap costs. It does **not** submit one transaction
per trace row, does not populate the live evidence-backed `TrustView`, and does
not require Geth. The live `mapnode serve` mode remains the executable check of
the Gateway, signature, TrustRoot and PathProof verification closed loop.

## Experiment settings and profiles

Every setting has isolated state and its own SQLite/WAL database:

| Setting | TrustMap planner | Checkpoint-assisted Direct |
| --- | --- | --- |
| B0 | no | no |
| B1 | no | yes |
| B2 | yes | no |
| B3 | yes | yes |

The exact `legacy-v4.1` profile uses `3,000,000` gas per Direct height step,
`30,000` gas per Path edge/height step, and `110,000` gas per TrustRoot update.
It is the reproduction profile. `prototype-calibrated` uses this repository's
measured values (`3,000,096`, `30,713`, and `108,582`) and is reported only as a
sensitivity analysis. Neither replay profile claims to execute SPV.

## Three-chain smoke replay

The smoke trace is a separate six-row planner fixture; the Stage 1 parser
fixture is intentionally unchanged. It runs B0--B3 and its golden check proves
that both B2 and B3 actually select a TrustMap path.

```sh
./scripts/replay-smoke.sh
./tests/integration/replay_smoke_test.sh
```

Optional environment overrides are `TRACE_PATH`, `RUN_ROOT`, `SETTINGS`
(space- or comma-separated), `PROFILE`, and `MAPNODE_BIN`. Both paths are
resolved before a temporary config is passed to MapNode. For example:

```sh
RUN_ROOT=/tmp/trustmap-smoke SETTINGS='B2,B3' \
PROFILE=prototype-calibrated ./scripts/replay-smoke.sh
```

## Full 245,000-message replay

By default the full script reads the local, read-only legacy trace from the
sibling path `../TrustMap-ETH/Dune/output_202512/msg.csv`. It never writes to
`TrustMap-ETH`. Override it with `TRACE_PATH` or `--trace` when the dataset is
stored elsewhere.

```sh
./scripts/replay-full.sh --profile legacy-v4.1

# Equivalent environment-driven invocation
TRACE_PATH=/data/msg.csv RUN_ROOT=/data/runs \
SETTINGS='B0 B1 B2 B3' PROFILE=legacy-v4.1 \
./scripts/replay-full.sh
```

The exact default run verifies the input SHA-256 digest, 245,000 prepared rows,
21 chains, every row's Direct/chosen cost and decision, every selected path and
path cost, final graph statistics, aggregate costs, and TrustMap selection
counts B2=`111058` and B3=`91829`. `--no-golden` is available for intentional
subsets or custom traces. Custom trace overrides do not silently reuse the
legacy digest; set `EXPECTED_DIGEST` when a fixed custom input is required.
The equivalent flag is `--expected-digest SHA256`; `--run-root` and
`--settings` are also available alongside their environment forms.

Use a fresh `RUN_ROOT` when changing trace, profile, settings, checkpoint
policy, or snapshot cadence. Reusing the same directory with the same inputs
resumes deterministically; incompatible run identity is rejected.

For direct binary use, `mapnode replay --config CONFIG --setting B2` selects a
single setting and `--setting all` retains the configured matrix. The finite
command performs resume and export automatically; there is no separate export
subcommand.

## Configuration and output

The checked-in templates are under `configs/replay/`, including exact and
calibrated profiles, B1/B3 checkpoint maps, smoke/full configs, and the full
legacy golden result.

MapNode requires absolute `input_trace` and `run_root` paths. Each setting
exports legacy-compatible `decisions.csv`, `summary.json`, initialization and
checkpoint files, plus `decisions_extended.csv`, `run_manifest.json`,
`progress.json`, and replay-only `replay.db`. TrustMap settings additionally
export paths and graph snapshots. The manifest records input hash, profile,
configured/effective checkpoint counts, chain count and durable progress.

The artifact checker can also be run explicitly. It checks both published
aggregates and row-level semantic digests:

```sh
go run ./cmd/replaycheck \
  --run-root /absolute/path/to/run \
  --golden configs/replay/golden/legacy-v4.1-full.json
```

## Run the packaged Docker binary

The existing MapNode image already contains both `serve` and `replay`. A replay
container needs only the binary, read-only config/input mounts, and a writable
output mount; no Geth network is needed. Paths inside the YAML must match the
container paths.

```sh
docker build -f docker/mapnode.Dockerfile -t trustmap-mapnode .
docker run --rm --entrypoint /mapnode \
  -v "$PWD/runtime/replay.yaml:/config/replay.yaml:ro" \
  -v "/data/traces:/input:ro" \
  -v "$PWD/runtime/replay-output:/output" \
  trustmap-mapnode replay --config /config/replay.yaml
```

In that example, the config must use `/input/...` for `input_trace` and
`/output` for `run_root`.
