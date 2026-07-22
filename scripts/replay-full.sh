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
default_trace=$repo_root/../TrustMap-ETH/Dune/output_202512/msg.csv
legacy_digest=ae35b6fd51185822dcfd8e338b2af43735bb2141764feead9ec993633a232175
trace_was_overridden=0
if [ "${TRACE_PATH+x}" = x ]; then trace_was_overridden=1; fi
trace_path=${TRACE_PATH:-$default_trace}
profile=${PROFILE:-legacy-v4.1}
settings=${SETTINGS:-B0 B1 B2 B3}
run_root=${RUN_ROOT:-}
check_golden=${CHECK_GOLDEN:-auto}
expected_digest=${EXPECTED_DIGEST:-}

while [ "$#" -gt 0 ]; do
    case $1 in
        --trace) [ "$#" -ge 2 ] || { printf '%s\n' 'replay-full: --trace needs a value' >&2; exit 2; }; trace_path=$2; trace_was_overridden=1; shift 2 ;;
        --profile) [ "$#" -ge 2 ] || { printf '%s\n' 'replay-full: --profile needs a value' >&2; exit 2; }; profile=$2; shift 2 ;;
        --settings) [ "$#" -ge 2 ] || { printf '%s\n' 'replay-full: --settings needs a value' >&2; exit 2; }; settings=$2; shift 2 ;;
        --run-root) [ "$#" -ge 2 ] || { printf '%s\n' 'replay-full: --run-root needs a value' >&2; exit 2; }; run_root=$2; shift 2 ;;
        --expected-digest) [ "$#" -ge 2 ] || { printf '%s\n' 'replay-full: --expected-digest needs a value' >&2; exit 2; }; expected_digest=$2; shift 2 ;;
        --no-golden) check_golden=0; shift ;;
        --help)
            printf '%s\n' 'usage: replay-full.sh [--trace PATH] [--profile legacy-v4.1|prototype-calibrated] [--settings "B0 B1 B2 B3"] [--run-root DIR] [--expected-digest SHA256] [--no-golden]'
            exit 0 ;;
        *) printf '%s\n' "replay-full: unknown argument: $1" >&2; exit 2 ;;
    esac
done
settings=$(printf '%s' "$settings" | tr ',;' '  ')

case $trace_path in
    /*) ;;
    *) trace_path=$(CDPATH= cd -- "$(dirname -- "$trace_path")" && pwd)/$(basename -- "$trace_path") ;;
esac
test -f "$trace_path" || { printf '%s\n' "replay-full: trace not found: $trace_path" >&2; exit 1; }

if [ -n "$run_root" ]; then
    case $run_root in
        /*) ;;
        *) mkdir -p "$run_root"; run_root=$(CDPATH= cd -- "$run_root" && pwd) ;;
    esac
    mkdir -p "$run_root"
else
    mkdir -p "$repo_root/runtime"
    run_root=$(mktemp -d "$repo_root/runtime/replay-full-XXXXXXXX")
fi
yaml_quote "$trace_path" >/dev/null || { printf '%s\n' 'replay-full: trace path contains a line break' >&2; exit 1; }
yaml_quote "$run_root" >/dev/null || { printf '%s\n' 'replay-full: run root contains a line break' >&2; exit 1; }

case $profile in
    legacy-v4.1) direct=3000000; path=30000; update=110000 ;;
    prototype-calibrated) direct=3000096; path=30713; update=108582 ;;
    *) printf '%s\n' "replay-full: unknown PROFILE: $profile" >&2; exit 1 ;;
esac

if [ "$trace_was_overridden" -eq 0 ] && [ -z "$expected_digest" ]; then
    expected_digest=$legacy_digest
fi

config=$(mktemp "$run_root/.full-config-XXXXXXXX.yaml")
trap 'rm -f "$config"' EXIT HUP INT TERM
{
    printf '%s\n' 'version: 1'
    printf '%s' 'input_trace: '; yaml_quote "$trace_path"; printf '\n'
    if [ -n "$expected_digest" ]; then printf 'expected_digest: %s\n' "$expected_digest"; fi
    printf '%s' 'run_root: '; yaml_quote "$run_root"; printf '\n'
    printf '%s\n' 'settings:'
    for setting in $settings; do
        case $setting in B0|B1|B2|B3) ;; *) printf '%s\n' "replay-full: invalid SETTINGS item: $setting" >&2; exit 1 ;; esac
        printf '  - %s\n' "$setting"
    done
    printf '%s\n' 'invalid_row_policy: reject' 'cost_profile:'
    printf '%s' '  id: '; yaml_quote "$profile"; printf '\n'
    printf '  direct_step_cost: %s\n  path_step_cost: %s\n  trust_root_update_cost: %s\n' "$direct" "$path" "$update"
    printf '%s\n' 'checkpoint_periods_by_setting:' '  B1:'
    sed 's/^/    /' "$repo_root/configs/replay/checkpoints/legacy-b1.yaml"
    printf '%s\n' '  B3:'
    sed 's/^/    /' "$repo_root/configs/replay/checkpoints/legacy-b3.yaml"
    printf '%s\n' 'record_every: 1000' 'log_every: 1000' 'durability:' '  synchronous: normal'
} >"$config"

if [ -n "${MAPNODE_BIN:-}" ]; then
    "$MAPNODE_BIN" replay --config "$config"
else
    (cd "$repo_root" && go run ./cmd/mapnode replay --config "$config")
fi

if [ "$check_golden" = auto ]; then
    check_golden=0
    if [ "$profile" = legacy-v4.1 ] && [ "$expected_digest" = "$legacy_digest" ] && [ "$settings" = 'B0 B1 B2 B3' ]; then
        check_golden=1
    fi
fi
if [ "$check_golden" = 1 ]; then
    (cd "$repo_root" && go run ./cmd/replaycheck --run-root "$run_root" --golden configs/replay/golden/legacy-v4.1-full.json)
fi

case $profile in
    prototype-calibrated) label='sensitivity (not exact legacy reproduction)' ;;
    *) label='exact legacy-v4.1 parameters' ;;
esac
printf '%s\n' "replay-full: profile=$profile [$label] settings=$settings run_root=$run_root"
