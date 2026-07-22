#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
yaml_cr=$(printf '\r')
yaml_quote() {
    yaml_value=$1
    case $yaml_value in
        *'
'*|*"$yaml_cr"*) return 1 ;;
    esac
    yaml_value=$(printf '%s' "$yaml_value" | sed "s/'/''/g")
    printf "'%s'" "$yaml_value"
}
[ "$#" -eq 0 ] || { printf '%s\n' 'replay-smoke: use TRACE_PATH, RUN_ROOT, SETTINGS, PROFILE, or MAPNODE_BIN environment overrides; positional arguments are not accepted' >&2; exit 2; }
trace_path=${TRACE_PATH:-$repo_root/tests/fixtures/replay/planner-smoke-3chain.csv}
profile=${PROFILE:-legacy-v4.1}
settings=$(printf '%s' "${SETTINGS:-B0 B1 B2 B3}" | tr ',;' '  ')

case $trace_path in
    /*) ;;
    *) trace_path=$(CDPATH= cd -- "$(dirname -- "$trace_path")" && pwd)/$(basename -- "$trace_path") ;;
esac
test -f "$trace_path" || { printf '%s\n' "replay-smoke: trace not found: $trace_path" >&2; exit 1; }

if [ -n "${RUN_ROOT:-}" ]; then
    case $RUN_ROOT in
        /*) run_root=$RUN_ROOT ;;
        *) mkdir -p "$RUN_ROOT"; run_root=$(CDPATH= cd -- "$RUN_ROOT" && pwd) ;;
    esac
    mkdir -p "$run_root"
else
    mkdir -p "$repo_root/runtime"
    run_root=$(mktemp -d "$repo_root/runtime/replay-smoke-XXXXXXXX")
fi
yaml_quote "$trace_path" >/dev/null || { printf '%s\n' 'replay-smoke: trace path contains a line break' >&2; exit 1; }
yaml_quote "$run_root" >/dev/null || { printf '%s\n' 'replay-smoke: run root contains a line break' >&2; exit 1; }

case $profile in
    legacy-v4.1) direct=3000000; path=30000; update=110000 ;;
    prototype-calibrated) direct=3000096; path=30713; update=108582 ;;
    *) printf '%s\n' "replay-smoke: unknown PROFILE: $profile" >&2; exit 1 ;;
esac

config=$(mktemp "$run_root/.smoke-config-XXXXXXXX.yaml")
trap 'rm -f "$config"' EXIT HUP INT TERM
{
    printf '%s\n' 'version: 1'
    printf '%s' 'input_trace: '; yaml_quote "$trace_path"; printf '\n'
    printf '%s' 'run_root: '; yaml_quote "$run_root"; printf '\n'
    printf '%s\n' 'settings:'
    for setting in $settings; do
        case $setting in B0|B1|B2|B3) ;; *) printf '%s\n' "replay-smoke: invalid SETTINGS item: $setting" >&2; exit 1 ;; esac
        printf '  - %s\n' "$setting"
    done
    printf '%s\n' 'invalid_row_policy: reject' 'cost_profile:'
    printf '%s' '  id: '; yaml_quote "$profile"; printf '\n'
    printf '  direct_step_cost: %s\n  path_step_cost: %s\n  trust_root_update_cost: %s\n' "$direct" "$path" "$update"
    printf '%s\n' 'checkpoint_periods:' '  a: 10000' '  b: 10000' '  c: 10000' 'record_every: 1' 'log_every: 1' 'durability:' '  synchronous: full'
} >"$config"

if [ -n "${MAPNODE_BIN:-}" ]; then
    "$MAPNODE_BIN" replay --config "$config"
else
    (cd "$repo_root" && go run ./cmd/mapnode replay --config "$config")
fi
printf '%s\n' "replay-smoke: profile=$profile settings=$settings run_root=$run_root"
