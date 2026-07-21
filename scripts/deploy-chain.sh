#!/bin/sh
set -eu

die() {
    printf '%s\n' "deploy-chain: $*" >&2
    exit 1
}

require_positive_decimal() {
    case $2 in
        ''|*[!0-9]*|0) die "$1 must be a positive decimal integer" ;;
    esac
}

require_address_legacy_unused() {
    value=$2
    case $value in
        0x[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]) ;;
        *) die "$1 is not a 20-byte hex address" ;;
    esac
}

require_address() {
    case $2 in
        0x*) address_hex=${2#0x} ;;
        *) die "$1 is not a 20-byte hex address" ;;
    esac
    [ "${#address_hex}" -eq 40 ] || die "$1 is not a 20-byte hex address"
    case $address_hex in *[!0-9a-fA-F]*) die "$1 is not a 20-byte hex address" ;; esac
}

for name in RPC_URL CHAIN_ID MERKLE_DEPTH AUTHORIZED_SIGNERS_FILE DEPLOYER_KEYSTORE DEPLOYER_PASSWORD_FILE MANIFEST_PATH; do
    eval "value=\${$name-}"
    [ -n "$value" ] || die "$name is required"
done
require_positive_decimal CHAIN_ID "$CHAIN_ID"
require_positive_decimal MERKLE_DEPTH "$MERKLE_DEPTH"
[ "$MERKLE_DEPTH" -le 32 ] || die "MERKLE_DEPTH must not exceed 32"
for file in "$AUTHORIZED_SIGNERS_FILE" "$DEPLOYER_KEYSTORE" "$DEPLOYER_PASSWORD_FILE"; do
    [ -f "$file" ] && [ -r "$file" ] || die "required runtime file is missing or unreadable: $file"
done
for command in forge cast jq; do
    command -v "$command" >/dev/null 2>&1 || die "$command executable not found"
done

jq -e '
  . as $profile |
  type == "object" and
  ((keys | sort) == (["authorized_signers","contract_name","hash_rounds","measured_direct_cost_gas","profile_id","signature_checks","version"] | sort)) and
  .version == 1 and
  .contract_name == "ExperimentalCostedDirectVerifier" and
  (.profile_id | type == "string" and length > 0) and
  (.authorized_signers | type == "array" and length > 0 and length <= 4096) and
  (all(.authorized_signers[];
    type == "object" and
    ((keys | sort) == (["address","keystore_file","password_file"] | sort)) and
    (.address | type == "string" and test("^0x[0-9a-fA-F]{40}$")) and
    (.keystore_file | type == "string" and length > 0) and
    (.password_file | type == "string" and length > 0))) and
  (.signature_checks | type == "number" and floor == . and . > 0 and . <= 4096 and . <= ($profile.authorized_signers | length)) and
  (.hash_rounds | type == "number" and floor == . and . >= 0 and . <= 16384) and
  (.measured_direct_cost_gas == null or (.measured_direct_cost_gas | type == "number" and floor == . and . > 0))
' "$AUTHORIZED_SIGNERS_FILE" >/dev/null || die "invalid direct verifier profile"

profile_id=$(jq -r '.profile_id' "$AUTHORIZED_SIGNERS_FILE")
signature_checks=$(jq -r '.signature_checks' "$AUTHORIZED_SIGNERS_FILE")
hash_rounds=$(jq -r '.hash_rounds' "$AUTHORIZED_SIGNERS_FILE")
authorized_signers=$(jq -c '[.authorized_signers[].address]' "$AUTHORIZED_SIGNERS_FILE")
authorized_signers_arg=$(jq -r '[.authorized_signers[].address] | "[" + join(",") + "]"' "$AUTHORIZED_SIGNERS_FILE")
signer_count=$(jq -r '.authorized_signers | length' "$AUTHORIZED_SIGNERS_FILE")

index=0
while [ "$index" -lt "$signer_count" ]; do
    address=$(jq -r ".authorized_signers[$index].address" "$AUTHORIZED_SIGNERS_FILE")
    keystore_file=$(jq -r ".authorized_signers[$index].keystore_file" "$AUTHORIZED_SIGNERS_FILE")
    password_file=$(jq -r ".authorized_signers[$index].password_file" "$AUTHORIZED_SIGNERS_FILE")
    require_address "authorized_signers[$index].address" "$address"
    [ -f "$keystore_file" ] && [ -r "$keystore_file" ] || die "authorized_signers[$index].keystore_file is missing or unreadable"
    [ -f "$password_file" ] && [ -r "$password_file" ] || die "authorized_signers[$index].password_file is missing or unreadable"
    keystore_address=$(cast wallet address --keystore "$keystore_file" --password-file "$password_file") || die "authorized_signers[$index] keystore could not be unlocked"
    [ "$(printf '%s' "$keystore_address" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$address" | tr '[:upper:]' '[:lower:]')" ] || die "authorized_signers[$index] keystore address does not match profile"
    index=$((index + 1))
done
[ "$(jq -r '[.authorized_signers[].address | ascii_downcase] | unique | length' "$AUTHORIZED_SIGNERS_FILE")" -eq "$signer_count" ] || die "authorized signer addresses must be unique"

rpc_chain_id=$(cast chain-id --rpc-url "$RPC_URL")
[ "$rpc_chain_id" = "$CHAIN_ID" ] || die "RPC chain ID $rpc_chain_id does not match CHAIN_ID $CHAIN_ID"

deploy_contract() {
    contract=$1
    shift
    forge create \
        --root /workspace/contracts \
        --rpc-url "$RPC_URL" \
        --chain "$CHAIN_ID" \
        --keystore "$DEPLOYER_KEYSTORE" \
        --password-file "$DEPLOYER_PASSWORD_FILE" \
        --broadcast \
        --json \
        "$contract" \
        --constructor-args "$@"
}

verifier_result=$(deploy_contract \
    src/ExperimentalCostedDirectVerifier.sol:ExperimentalCostedDirectVerifier \
    "$authorized_signers_arg" "$signature_checks" "$hash_rounds")
direct_verifier=$(printf '%s' "$verifier_result" | jq -er '.deployedTo | select(type == "string" and test("^0x[0-9a-fA-F]{40}$"))') || die "forge did not return a valid direct verifier address"
verifier_tx=$(printf '%s' "$verifier_result" | jq -er '.transactionHash | select(type == "string" and test("^0x[0-9a-fA-F]{64}$"))') || die "forge did not return a valid direct verifier transaction hash"

zero_root=0x0000000000000000000000000000000000000000000000000000000000000000
gateway_result=$(deploy_contract \
    src/TrustMapGateway.sol:TrustMapGateway \
    "$MERKLE_DEPTH" "$direct_verifier" "$zero_root")
gateway=$(printf '%s' "$gateway_result" | jq -er '.deployedTo | select(type == "string" and test("^0x[0-9a-fA-F]{40}$"))') || die "forge did not return a valid gateway address"
gateway_tx=$(printf '%s' "$gateway_result" | jq -er '.transactionHash | select(type == "string" and test("^0x[0-9a-fA-F]{64}$"))') || die "forge did not return a valid gateway transaction hash"

bind_result=$(cast send \
    --rpc-url "$RPC_URL" \
    --keystore "$DEPLOYER_KEYSTORE" \
    --password-file "$DEPLOYER_PASSWORD_FILE" \
    --json \
    "$direct_verifier" 'bindGateway(address)' "$gateway")
bind_tx=$(printf '%s' "$bind_result" | jq -er '.transactionHash | select(type == "string" and test("^0x[0-9a-fA-F]{64}$"))') || die "bindGateway did not return a valid transaction hash"

gateway_verifier=$(cast call --rpc-url "$RPC_URL" "$gateway" 'directVerifier()(address)')
verifier_gateway=$(cast call --rpc-url "$RPC_URL" "$direct_verifier" 'gateway()(address)')
[ "$(printf '%s' "$gateway_verifier" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$direct_verifier" | tr '[:upper:]' '[:lower:]')" ] || die "gateway.directVerifier does not match deployment"
[ "$(printf '%s' "$verifier_gateway" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$gateway" | tr '[:upper:]' '[:lower:]')" ] || die "directVerifier.gateway does not match deployment"

gateway_code=$(cast code --rpc-url "$RPC_URL" "$gateway")
verifier_code=$(cast code --rpc-url "$RPC_URL" "$direct_verifier")
[ "$gateway_code" != 0x ] && [ -n "$gateway_code" ] || die "gateway has no deployed code"
[ "$verifier_code" != 0x ] && [ -n "$verifier_code" ] || die "direct verifier has no deployed code"
gateway_code_hash=$(cast keccak "$gateway_code")
verifier_code_hash=$(cast keccak "$verifier_code")
deployment_block=$(cast block-number --rpc-url "$RPC_URL")
require_positive_decimal deploymentBlock "$deployment_block"

manifest_dir=$(dirname -- "$MANIFEST_PATH")
[ -d "$manifest_dir" ] || die "manifest directory does not exist: $manifest_dir"
manifest_tmp=$manifest_dir/.deployment-manifest.tmp.$$
trap 'rm -f "$manifest_tmp"' EXIT HUP INT TERM
jq -n \
    --arg chain_id "$CHAIN_ID" \
    --arg gateway "$gateway" \
    --arg direct_verifier "$direct_verifier" \
    --arg gateway_code_hash "$gateway_code_hash" \
    --arg verifier_code_hash "$verifier_code_hash" \
    --arg gateway_tx "$gateway_tx" \
    --arg verifier_tx "$verifier_tx" \
    --arg bind_tx "$bind_tx" \
    --arg profile_id "$profile_id" \
    --argjson authorized_signers "$authorized_signers" \
    --argjson signature_checks "$signature_checks" \
    --argjson hash_rounds "$hash_rounds" \
    --argjson deployment_block "$deployment_block" \
    '{
      version: 1,
      status: "deployed",
      chainId: $chain_id,
      deploymentBlock: $deployment_block,
      gateway: $gateway,
      directVerifier: $direct_verifier,
      profileId: $profile_id,
      authorizedSigners: $authorized_signers,
      signatureChecks: $signature_checks,
      hashRounds: $hash_rounds,
      codeHashes: {gateway: $gateway_code_hash, directVerifier: $verifier_code_hash},
      transactions: {gateway: $gateway_tx, directVerifier: $verifier_tx, bindGateway: $bind_tx}
    }' >"$manifest_tmp"
chmod 0644 "$manifest_tmp"
mv -f "$manifest_tmp" "$MANIFEST_PATH"
trap - EXIT HUP INT TERM
printf '%s\n' "deployment completed for chain $CHAIN_ID"
