#!/bin/sh
set -eu

case ${CHAIN_ID-} in
    ''|*[!0-9]*|0)
        printf '%s\n' "geth-healthcheck: CHAIN_ID must be a positive decimal integer" >&2
        exit 1
        ;;
esac

ledger_script="if (eth.chainId() !== web3.toHex('$CHAIN_ID')) { throw new Error('chain ID mismatch'); } if (eth.getBlock('latest') === null) { throw new Error('latest block unavailable'); } true;"
http_script="if (String(net.version) !== '$CHAIN_ID') { throw new Error('network ID mismatch'); } $ledger_script"
http_result=$(geth attach --datadir /data --exec "$http_script" http://127.0.0.1:8545) || {
    printf '%s\n' "geth-healthcheck: HTTP RPC check failed" >&2
    exit 1
}
[ "$http_result" = true ] || {
    printf '%s\n' "geth-healthcheck: HTTP RPC returned an invalid ledger/network result" >&2
    exit 1
}

ws_result=$(geth attach --datadir /data --exec "$ledger_script" ws://127.0.0.1:8546) || {
    printf '%s\n' "geth-healthcheck: WS RPC check failed" >&2
    exit 1
}
[ "$ws_result" = true ] || {
    printf '%s\n' "geth-healthcheck: WS RPC returned an invalid ledger result" >&2
    exit 1
}
