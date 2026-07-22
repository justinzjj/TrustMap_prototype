#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$script_dir/live-common.sh"

prepare_live_topology "$repo_root/configs/topology.yaml" live-three-chain
wait_ready chain-a 18547
wait_ready chain-b 18647
wait_ready chain-c 18747

confirmed_tuple chain-a 2
a_height=$CONFIRMED_HEIGHT
a_hash=$CONFIRMED_HASH

submit_request chain-b 31337 "$a_height" "$a_hash" seed-b-to-a
seed_b_request=$LAST_REQUEST_ID
wait_request_confirmed 18647 "$seed_b_request" direct
seed_b_tx=$EXECUTOR_TX
receipt_for chain-b "$seed_b_tx"
b_height=$(printf '%s' "$RECEIPT" | djq chain-b -er '.blockNumber' | xargs printf '%d')
b_hash=$(printf '%s' "$RECEIPT" | djq chain-b -er '.blockHash')

# The B->A evidence must already be durable at C before the serialized C
# worker observes the next two requests.
wait_trust_edge 18747 31338 31337 "$a_height" "$a_hash"

submit_request chain-c 31338 "$b_height" "$b_hash" seed-c-to-b
seed_c_request=$LAST_REQUEST_ID
# Queue C->A immediately behind C->B. The single serialized worker completes
# C->B first, then observes C->A at that exact confirmed home block boundary.
submit_request chain-c 31337 "$a_height" "$a_hash" three-chain-path
path_request=$LAST_REQUEST_ID

wait_request_confirmed 18747 "$seed_c_request" direct
seed_c_tx=$EXECUTOR_TX
wait_trust_edge 18747 31339 31338 "$b_height" "$b_hash"
wait_request_confirmed 18747 "$path_request" path
path_status=$REQUEST_STATUS
path_tx=$EXECUTOR_TX
receipt_for chain-c "$path_tx"

hop_count=$(printf '%s' "$path_status" | djq chain-c -er '.hop_count')
fallback_reason=$(printf '%s' "$path_status" | djq chain-c -r '.fallback_reason // empty')
[ "$hop_count" = 2 ] || fail "planner selected $hop_count hops, expected 2"
[ -z "$fallback_reason" ] || fail "path unexpectedly used DirectFallback: $fallback_reason"
assert_receipt_topic chain-c "$RECEIPT" '0x068d0b8a6ae7f5a6b9ec3c158eb348081e3e4ce74b6d9d5294584f3bf6ec103f' PathVerificationSucceeded
assert_receipt_topic chain-c "$RECEIPT" '0x26fe4056692e22cb6515b58329b35b50d71b7605c18b7bcaf3bacdf8297245f5' DependencyRecorded
assert_receipt_topic chain-c "$RECEIPT" '0x9e14f05723283ecafaae8f806553da1d3df582379f2502195bc237b3902da290' RequestResolved
assert_path_hop_count chain-c "$RECEIPT" 2
assert_request_resolved chain-c "$path_request"
wait_trust_edge 18547 31339 31337 "$a_height" "$a_hash"

printf '%s\n' \
    "LIVE_PATH_OK chain_ids=31337,31338,31339 seed_b_request=$seed_b_request seed_b_tx=$seed_b_tx seed_c_request=$seed_c_request seed_c_tx=$seed_c_tx path_request=$path_request path_tx=$path_tx plan=path hops=2 source_height=$a_height source_hash=$a_hash"
