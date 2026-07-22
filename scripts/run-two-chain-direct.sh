#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$script_dir/live-common.sh"

prepare_live_topology "$repo_root/configs/topology-2.yaml" live-two-chain
wait_ready chain-a 17547
wait_ready chain-b 17647

confirmed_tuple chain-a 2
source_height=$CONFIRMED_HEIGHT
source_hash=$CONFIRMED_HASH
submit_request chain-b 31337 "$source_height" "$source_hash" two-chain-direct
request_id=$LAST_REQUEST_ID
wait_request_confirmed 17647 "$request_id" direct
receipt_for chain-b "$EXECUTOR_TX"

assert_receipt_topic chain-b "$RECEIPT" '0xf823b7a644bb08196389f4340810ad3d39e780aa8e99daf540a4622416ab28ba' DirectVerificationSucceeded
assert_receipt_topic chain-b "$RECEIPT" '0x26fe4056692e22cb6515b58329b35b50d71b7605c18b7bcaf3bacdf8297245f5' DependencyRecorded
assert_receipt_topic chain-b "$RECEIPT" '0x9e14f05723283ecafaae8f806553da1d3df582379f2502195bc237b3902da290' RequestResolved
assert_request_resolved chain-b "$request_id"
wait_trust_edge 17547 31338 31337 "$source_height" "$source_hash"

printf '%s\n' \
    "LIVE_DIRECT_OK chain_ids=31337,31338 request_id=$request_id tx_hash=$EXECUTOR_TX plan=direct hops=0 source_height=$source_height source_hash=$source_hash"
