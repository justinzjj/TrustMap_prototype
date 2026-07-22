#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT HUP INT TERM

if sh "$repo_root/scripts/replay-smoke.sh" --unexpected >"$test_root/smoke-args.log" 2>&1; then
    printf '%s\n' 'replay_smoke_test: smoke accepted an unexpected argument' >&2
    exit 1
fi
if sh "$repo_root/scripts/replay-full.sh" --unexpected >"$test_root/full-args.log" 2>&1; then
    printf '%s\n' 'replay_smoke_test: full accepted an unexpected argument' >&2
    exit 1
fi

TRACE_PATH="$repo_root/tests/fixtures/replay/planner-smoke-3chain.csv" \
RUN_ROOT="$test_root/run" \
SETTINGS="B0 B1 B2 B3" \
PROFILE=legacy-v4.1 \
    sh "$repo_root/scripts/replay-smoke.sh"

for setting in b0 b1 b2 b3; do
    test -f "$test_root/run/$setting/summary.json"
    test -f "$test_root/run/$setting/run_manifest.json"
done

# The fixture is deliberately planner-oriented: both TrustMap settings must
# exercise the path branch, while baseline-only settings cannot select it.
(
    cd "$repo_root"
    go run ./cmd/replaycheck \
        --run-root "$test_root/run" \
        --golden "$repo_root/tests/fixtures/replay/planner-smoke-golden.json"
)

printf '%s\n' 'replay_smoke_test: PASS'
