#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime_dir=
compose_file=

fail() {
    printf '%s\n' "live TrustMap: $*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

dc() {
    docker compose -f "$compose_file" "$@"
}

dcast() {
    chain=$1
    shift
    dc run --rm -T --no-deps --entrypoint cast "deploy-$chain" "$@"
}

djq() {
    chain=$1
    shift
    dc run --rm -T --no-deps --entrypoint jq "deploy-$chain" "$@"
}

rpc_url() {
    printf 'http://geth-%s:8545\n' "$1"
}

gateway_for() {
    djq "$1" -er '.gateway' /output/gateway-manifest.json
}

run_cleanup_bounded() {
    if command -v timeout >/dev/null 2>&1; then
        timeout -k 5 30 "$@"
        return
    fi
    "$@" &
    cleanup_pid=$!
    (
        sleep 30
        kill -TERM "$cleanup_pid" 2>/dev/null || exit 0
        sleep 5
        kill -KILL "$cleanup_pid" 2>/dev/null || true
    ) &
    watchdog_pid=$!
    wait "$cleanup_pid"
    command_status=$?
    kill "$watchdog_pid" 2>/dev/null || true
    wait "$watchdog_pid" 2>/dev/null || true
    return "$command_status"
}

cleanup_live_topology() {
    status=$?
    trap - EXIT HUP INT TERM
    if [ -n "$compose_file" ] && [ -f "$compose_file" ]; then
        run_cleanup_bounded docker compose -f "$compose_file" logs --no-color >"$runtime_dir/compose.log" 2>&1 || true
        run_cleanup_bounded docker compose -f "$compose_file" down --volumes --remove-orphans >/dev/null 2>&1 || true
    fi
    if [ "$status" -ne 0 ]; then
        printf '%s\n' "live TrustMap: failed; compose logs retained at $runtime_dir/compose.log" >&2
    fi
    exit "$status"
}

prepare_live_topology() {
    topology_file=$1
    label=$2
    require_command docker
    require_command curl
    require_command go
    mkdir -p "$repo_root/runtime"
    runtime_dir="$repo_root/runtime/$label-$(date +%Y%m%d%H%M%S)-$$"
    compose_file="$runtime_dir/compose.yaml"
    (
        cd "$repo_root"
        go run ./cmd/trustmapctl topology render --file "$topology_file" --output "$runtime_dir"
    )
    docker compose -f "$compose_file" config --quiet
    trap cleanup_live_topology EXIT
    trap 'exit 130' HUP INT TERM
    if [ "${LIVE_SKIP_BUILD:-0}" != 1 ]; then
        live_goproxy=${GOPROXY:-https://proxy.golang.org,direct}
        live_distroless=${DISTROLESS_IMAGE:-gcr.io/distroless/static-debian12:nonroot}
        dc build --build-arg "GOPROXY=$live_goproxy" --build-arg "DISTROLESS_IMAGE=$live_distroless"
    fi
    dc up --detach --no-build
}

wait_ready() {
    name=$1
    port=$2
    deadline=$(( $(date +%s) + 300 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if curl --fail --silent --show-error --max-time 3 "http://127.0.0.1:$port/health/ready" >/dev/null 2>&1; then
            printf '%s\n' "live TrustMap: $name is ready" >&2
            return 0
        fi
        sleep 1
    done
    fail "$name did not become ready within 300 seconds"
}

confirmed_tuple() {
    chain=$1
    confirmations=$2
    head=$(dcast "$chain" block-number --rpc-url "$(rpc_url "$chain")")
    case $head in ''|*[!0-9]*) fail "invalid $chain block number: $head" ;; esac
    [ "$head" -gt "$confirmations" ] || fail "$chain has no confirmed block yet"
    CONFIRMED_HEIGHT=$((head - confirmations))
    CONFIRMED_HASH=$(dcast "$chain" block "$CONFIRMED_HEIGHT" --rpc-url "$(rpc_url "$chain")" --field hash)
    case $CONFIRMED_HASH in 0x????????????????????????????????????????????????????????????????) ;; *) fail "invalid $chain confirmed hash: $CONFIRMED_HASH" ;; esac
}

submit_request() {
    home_chain=$1
    source_chain_id=$2
    source_height=$3
    source_hash=$4
    label=$5
    gateway=$(gateway_for "$home_chain")
    printf '%s\n' "live TrustMap: submitting $label" >&2
    send_result=$(dcast "$home_chain" send \
        --rpc-url "$(rpc_url "$home_chain")" \
        --keystore /run/secrets/deployer-keystore.json \
        --password-file /run/secrets/deployer-password \
        --json "$gateway" \
        'requestVerification(uint256,uint256,bytes32)' \
        "$source_chain_id" "$source_height" "$source_hash")
    LAST_REQUEST_TX=$(printf '%s' "$send_result" | djq "$home_chain" -er '.transactionHash')
    request_receipt=$(dcast "$home_chain" receipt "$LAST_REQUEST_TX" --rpc-url "$(rpc_url "$home_chain")" --json)
    LAST_REQUEST_ID=$(printf '%s' "$request_receipt" | djq "$home_chain" -er \
        --arg topic '0x6e4486cba7e09fd23bd6d6edc0fc893ff7f02d9266d863047e298f7b132ee71a' \
        '.logs[] | select((.topics[0] | ascii_downcase) == ($topic | ascii_downcase)) | .topics[1]')
    case $LAST_REQUEST_ID in 0x????????????????????????????????????????????????????????????????) ;; *) fail "requestVerification emitted no request ID" ;; esac
}

wait_request_confirmed() {
    api_port=$1
    request_id=$2
    expected_plan=$3
    deadline=$(( $(date +%s) + 300 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        body=$(curl --silent --show-error --max-time 3 "http://127.0.0.1:$api_port/v1/requests/$request_id" 2>/dev/null || true)
        if [ -n "$body" ]; then
            state=$(printf '%s' "$body" | djq chain-a -r '.execution_state // empty' 2>/dev/null || true)
            plan=$(printf '%s' "$body" | djq chain-a -r '.plan_type // empty' 2>/dev/null || true)
            if [ "$state" = confirmed ] && [ "$plan" = "$expected_plan" ]; then
                REQUEST_STATUS=$body
                EXECUTOR_TX=$(printf '%s' "$body" | djq chain-a -er '.tx_hash')
                return 0
            fi
        fi
        sleep 1
    done
    fail "request $request_id did not reach confirmed/$expected_plan within 300 seconds"
}

receipt_for() {
    chain=$1
    tx_hash=$2
    RECEIPT=$(dcast "$chain" receipt "$tx_hash" --rpc-url "$(rpc_url "$chain")" --json)
}

assert_receipt_topic() {
    chain=$1
    receipt=$2
    topic=$3
    label=$4
    printf '%s' "$receipt" | djq "$chain" -e --arg topic "$topic" \
        'any(.logs[]?.topics[0]; ascii_downcase == ($topic | ascii_downcase))' >/dev/null || fail "$label event missing from receipt"
}

assert_request_resolved() {
    chain=$1
    request_id=$2
    gateway=$(gateway_for "$chain")
    resolved=$(dcast "$chain" call --rpc-url "$(rpc_url "$chain")" "$gateway" 'requestResolved(bytes32)(bool)' "$request_id")
    [ "$resolved" = true ] || fail "Gateway requestResolved is not true for $request_id"
}

wait_trust_edge() {
    api_port=$1
    from_chain_id=$2
    to_chain_id=$3
    to_height=$4
    to_hash=$5
    deadline=$(( $(date +%s) + 300 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        body=$(curl --silent --show-error --max-time 3 "http://127.0.0.1:$api_port/v1/trustview" 2>/dev/null || true)
        if [ -n "$body" ] && printf '%s' "$body" | djq chain-a -e \
            --arg from "$from_chain_id" --arg to "$to_chain_id" --arg height "$to_height" --arg hash "$to_hash" '
                . as $view |
                any($view.edges[]?; . as $edge |
                    (($view.nodes[] | select(.node_id == $edge.from_node_id) | .chain_id) == $from) and
                    (($view.nodes[] | select(.node_id == $edge.to_node_id) | .chain_id) == $to) and
                    (($view.nodes[] | select(.node_id == $edge.to_node_id) | .height) == $height) and
                    (($view.nodes[] | select(.node_id == $edge.to_node_id) | .block_hash | ascii_downcase) == ($hash | ascii_downcase)))
            ' >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    fail "TrustView did not acquire edge $from_chain_id->$to_chain_id for $to_height/$to_hash"
}

assert_path_hop_count() {
    chain=$1
    receipt=$2
    expected=$3
    hop_count=$(printf '%s' "$receipt" | djq "$chain" -er \
        --arg topic '0x068d0b8a6ae7f5a6b9ec3c158eb348081e3e4ce74b6d9d5294584f3bf6ec103f' '
            .logs[] | select((.topics[0] | ascii_downcase) == ($topic | ascii_downcase)) |
            .data[194:258] | sub("^0+"; "") | if . == "" then "0" else . end
        ')
    [ "$hop_count" = "$(printf '%x' "$expected")" ] || fail "PathVerificationSucceeded hop count is 0x$hop_count, expected $expected"
}
