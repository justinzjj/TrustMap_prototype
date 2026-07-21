#!/bin/sh
set -eu

datadir=/data
genesis=/runtime/genesis.json
source_keystore=/run/secrets/deployer-keystore.json
password_file=/run/secrets/deployer-password
marker=$datadir/.trustmap-genesis-initialized
chaindata=$datadir/geth/chaindata

die() {
    printf '%s\n' "geth-entrypoint: $*" >&2
    exit 1
}

is_positive_decimal() {
    case $1 in
        ''|*[!0-9]*|0) return 1 ;;
        *) return 0 ;;
    esac
}

is_address_legacy_unused() {
    case $1 in
        0x[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]) return 0 ;;
        *) return 1 ;;
    esac
}

is_address() {
    case $1 in
        0x*) address_hex=${1#0x} ;;
        *) return 1 ;;
    esac
    [ "${#address_hex}" -eq 40 ] || return 1
    case $address_hex in *[!0-9a-fA-F]*) return 1 ;; esac
    return 0
}

: "${CHAIN_ID:?CHAIN_ID is required}"
: "${BLOCK_PERIOD:?BLOCK_PERIOD is required}"
: "${DEPLOYER_ADDRESS:?DEPLOYER_ADDRESS is required}"
: "${HTTP_VHOSTS:?HTTP_VHOSTS is required}"
is_positive_decimal "$CHAIN_ID" || die "CHAIN_ID must be a positive decimal integer"
is_positive_decimal "$BLOCK_PERIOD" || die "BLOCK_PERIOD must be a positive decimal integer"
is_address "$DEPLOYER_ADDRESS" || die "DEPLOYER_ADDRESS must be a 20-byte hex address"
case $HTTP_VHOSTS in
    *[!0-9A-Za-z.,_-]*) die "HTTP_VHOSTS contains invalid characters" ;;
esac

for file in "$genesis" "$source_keystore" "$password_file"; do
    [ -f "$file" ] && [ -r "$file" ] || die "required runtime file is missing or unreadable: $file"
done
command -v geth >/dev/null 2>&1 || die "geth executable not found"

mkdir -p "$datadir/keystore"
if [ -f "$marker" ]; then
    [ -d "$chaindata" ] || die "initialization marker exists but chaindata is missing"
else
    [ ! -e "$chaindata" ] || die "chaindata exists without the initialization marker"
    keystore_tmp=$datadir/keystore/.deployer-keystore.json.tmp.$$
    trap 'rm -f "$keystore_tmp"' EXIT HUP INT TERM
    cp "$source_keystore" "$keystore_tmp"
    chmod 600 "$keystore_tmp"
    mv -f "$keystore_tmp" "$datadir/keystore/deployer-keystore.json"
    geth --datadir "$datadir" init "$genesis"
    marker_tmp=$datadir/.trustmap-genesis-initialized.tmp.$$
    : >"$marker_tmp"
    chmod 600 "$marker_tmp"
    mv -f "$marker_tmp" "$marker"
    trap - EXIT HUP INT TERM
fi

exec geth \
    --datadir "$datadir" \
    --dev \
    --dev.period "$BLOCK_PERIOD" \
    --networkid "$CHAIN_ID" \
    --miner.etherbase "$DEPLOYER_ADDRESS" \
    --password "$password_file" \
    --http \
    --http.addr 0.0.0.0 \
    --http.port 8545 \
    --http.api eth,net,web3 \
    --http.vhosts "$HTTP_VHOSTS" \
    --ws \
    --ws.addr 0.0.0.0 \
    --ws.port 8546 \
    --ws.api eth,net,web3 \
    --ipcdisable \
    --nodiscover \
    --maxpeers 0
