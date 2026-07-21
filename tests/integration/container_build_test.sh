#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
GETH_IMAGE=${GETH_IMAGE:-ethereum/client-go:v1.17.3}
GO_IMAGE=${GO_IMAGE:-golang:1.24.10-bookworm}
DISTROLESS_IMAGE=${DISTROLESS_IMAGE:-gcr.io/distroless/static-debian12:nonroot}
FOUNDRY_IMAGE=${FOUNDRY_IMAGE:-ghcr.io/foundry-rs/foundry:v1.4.4}
JQ_IMAGE=${JQ_IMAGE:-ghcr.io/jqlang/jq:1.8.1}

fail() {
    printf '%s\n' "container_build_test: $*" >&2
    exit 1
}

assert_contains() {
    grep -F -- "$2" "$1" >/dev/null || fail "$1 does not contain: $2"
}

reject_latest() {
    case $2 in
        latest|*:latest) fail "$1 must not use latest" ;;
    esac
}

for path in \
    .dockerignore \
    docker/geth.Dockerfile docker/geth-entrypoint.sh docker/geth-healthcheck.sh \
    docker/deployer.Dockerfile scripts/deploy-chain.sh \
    docker/mapnode.Dockerfile cmd/mapnode/main.go internal/mapnodebootstrap/bootstrap.go
do
    [ -f "$repo_root/$path" ] || fail "missing $path"
done

assert_contains "$repo_root/.dockerignore" 'runtime'
assert_contains "$repo_root/.dockerignore" 'contracts/out'

for item in \
    "GETH_IMAGE=$GETH_IMAGE" "GO_IMAGE=$GO_IMAGE" \
    "DISTROLESS_IMAGE=$DISTROLESS_IMAGE" "FOUNDRY_IMAGE=$FOUNDRY_IMAGE" "JQ_IMAGE=$JQ_IMAGE"
do
    name=${item%%=*}
    value=${item#*=}
    reject_latest "$name" "$value"
done

assert_contains "$repo_root/docker/geth.Dockerfile" 'ARG GETH_IMAGE=ethereum/client-go:v1.17.3'
assert_contains "$repo_root/docker/geth.Dockerfile" 'FROM ${GETH_IMAGE}'
assert_contains "$repo_root/docker/geth-entrypoint.sh" 'datadir=/data'
assert_contains "$repo_root/docker/geth-entrypoint.sh" 'genesis=/runtime/genesis.json'
assert_contains "$repo_root/docker/geth-entrypoint.sh" 'source_keystore=/run/secrets/deployer-keystore.json'
assert_contains "$repo_root/docker/geth-entrypoint.sh" 'password_file=/run/secrets/deployer-password'
assert_contains "$repo_root/docker/geth-entrypoint.sh" '--http.api eth,net,web3'
assert_contains "$repo_root/docker/geth-entrypoint.sh" '--http.vhosts "$HTTP_VHOSTS"'
assert_contains "$repo_root/docker/geth-entrypoint.sh" '--ws.api eth,net,web3'
assert_contains "$repo_root/docker/geth-entrypoint.sh" '--ipcdisable'
assert_contains "$repo_root/docker/geth-entrypoint.sh" '--nodiscover'
assert_contains "$repo_root/docker/geth-healthcheck.sh" 'String(net.version)'
assert_contains "$repo_root/docker/geth-healthcheck.sh" 'ws://127.0.0.1:8546'
if grep -E -- '--unlock|--allow-insecure-unlock' "$repo_root/docker/geth-entrypoint.sh" >/dev/null; then
    fail "geth entrypoint must rely on dev-mode account handling"
fi
assert_contains "$repo_root/docker/mapnode.Dockerfile" 'ARG GO_IMAGE=golang:1.24.10-bookworm'
assert_contains "$repo_root/docker/mapnode.Dockerfile" 'ARG DISTROLESS_IMAGE=gcr.io/distroless/static-debian12:nonroot'
assert_contains "$repo_root/docker/mapnode.Dockerfile" 'CGO_ENABLED=0'
assert_contains "$repo_root/docker/mapnode.Dockerfile" 'USER nonroot:nonroot'
assert_contains "$repo_root/docker/mapnode.Dockerfile" 'COPY --from=build --chown=nonroot:nonroot /out/runtime /runtime'
assert_contains "$repo_root/docker/mapnode.Dockerfile" 'CMD ["/mapnode", "--healthcheck", "http://127.0.0.1:8080/health/ready"]'
assert_contains "$repo_root/docker/deployer.Dockerfile" 'ARG FOUNDRY_IMAGE=ghcr.io/foundry-rs/foundry:v1.4.4'
assert_contains "$repo_root/scripts/deploy-chain.sh" 'ExperimentalCostedDirectVerifier'
assert_contains "$repo_root/scripts/deploy-chain.sh" 'mv -f "$manifest_tmp" "$MANIFEST_PATH"'

for script in docker/geth-entrypoint.sh docker/geth-healthcheck.sh scripts/deploy-chain.sh; do
    sh -n "$repo_root/$script"
    assert_contains "$repo_root/$script" 'set -eu'
    if grep -F 'set -x' "$repo_root/$script" >/dev/null; then
        fail "$script must not enable shell tracing"
    fi
    if grep -E '(^|[^[:alnum:]_])sleep([[:space:]]|$)' "$repo_root/$script" >/dev/null; then
        fail "$script must not use fixed sleeps"
    fi
done
sh -n "$repo_root/tests/integration/container_build_test.sh"
assert_contains "$repo_root/tests/integration/container_build_test.sh" 'set -eu'

test_tmp=$(mktemp -d)
trap 'rm -rf "$test_tmp"' EXIT HUP INT TERM
fake_bin=$test_tmp/bin
mkdir -p "$fake_bin"

cat >"$fake_bin/geth" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$FAKE_GETH_LOG"
case " $* " in
    *' init '*) mkdir -p "$FAKE_GETH_DATADIR/geth/chaindata" ;;
esac
exit 0
EOF
chmod 0755 "$fake_bin/geth"

if PATH="$fake_bin:$PATH" sh "$repo_root/docker/geth-entrypoint.sh" >"$test_tmp/missing.out" 2>&1; then
    fail "geth entrypoint accepted missing environment"
fi
[ ! -s "$test_tmp/geth.log" ] || fail "geth was called before environment validation"

case_dir=$test_tmp/geth-case
mkdir -p "$case_dir/data" "$case_dir/runtime" "$case_dir/secrets"
printf '%s\n' '{}' >"$case_dir/runtime/genesis.json"
printf '%s\n' '{}' >"$case_dir/secrets/deployer-keystore.json"
printf '%s\n' 'test-only-password-sentinel' >"$case_dir/secrets/deployer-password"
sed \
    -e "s|^datadir=/data$|datadir=$case_dir/data|" \
    -e "s|^genesis=/runtime/genesis.json$|genesis=$case_dir/runtime/genesis.json|" \
    -e "s|^source_keystore=/run/secrets/deployer-keystore.json$|source_keystore=$case_dir/secrets/deployer-keystore.json|" \
    -e "s|^password_file=/run/secrets/deployer-password$|password_file=$case_dir/secrets/deployer-password|" \
    "$repo_root/docker/geth-entrypoint.sh" >"$case_dir/entrypoint.sh"
chmod 0755 "$case_dir/entrypoint.sh"
export FAKE_GETH_LOG=$test_tmp/geth.log
export FAKE_GETH_DATADIR=$case_dir/data
PATH="$fake_bin:$PATH" CHAIN_ID=10001 BLOCK_PERIOD=3 HTTP_VHOSTS=geth-chain-a,localhost DEPLOYER_ADDRESS=0x1111111111111111111111111111111111111111 sh "$case_dir/entrypoint.sh" >"$test_tmp/geth-first.out" 2>&1
PATH="$fake_bin:$PATH" CHAIN_ID=10001 BLOCK_PERIOD=3 HTTP_VHOSTS=geth-chain-a,localhost DEPLOYER_ADDRESS=0x1111111111111111111111111111111111111111 sh "$case_dir/entrypoint.sh" >"$test_tmp/geth-second.out" 2>&1
[ "$(grep -c ' init ' "$test_tmp/geth.log")" -eq 1 ] || fail "geth init did not run exactly once"
if grep -F 'test-only-password-sentinel' "$test_tmp/geth-first.out" "$test_tmp/geth-second.out" "$test_tmp/geth.log" >/dev/null; then
    fail "geth scripts exposed the password"
fi
rm -rf "$case_dir/data/geth/chaindata"
if PATH="$fake_bin:$PATH" CHAIN_ID=10001 BLOCK_PERIOD=3 HTTP_VHOSTS=geth-chain-a,localhost DEPLOYER_ADDRESS=0x1111111111111111111111111111111111111111 sh "$case_dir/entrypoint.sh" >"$test_tmp/geth-corrupt.out" 2>&1; then
    fail "geth entrypoint accepted marker without chaindata"
fi

cat >"$fake_bin/geth" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$FAKE_HEALTH_LOG"
case ${FAKE_HEALTH_MISMATCH-0} in
    1) printf '%s\n' 'Error: chain ID mismatch'; exit 0 ;;
esac
printf '%s\n' 'true'
EOF
chmod 0755 "$fake_bin/geth"
export FAKE_HEALTH_LOG=$test_tmp/health.log
PATH="$fake_bin:$PATH" CHAIN_ID=10001 sh "$repo_root/docker/geth-healthcheck.sh"
[ "$(wc -l <"$FAKE_HEALTH_LOG")" -eq 2 ] || fail "geth healthcheck did not test both transports"
grep -F 'http://127.0.0.1:8545' "$FAKE_HEALTH_LOG" >/dev/null || fail "geth healthcheck did not test HTTP RPC"
grep -F 'ws://127.0.0.1:8546' "$FAKE_HEALTH_LOG" >/dev/null || fail "geth healthcheck did not test WS RPC"
if PATH="$fake_bin:$PATH" CHAIN_ID=10001 FAKE_HEALTH_MISMATCH=1 sh "$repo_root/docker/geth-healthcheck.sh" >"$test_tmp/health-mismatch.out" 2>&1; then
    fail "geth healthcheck accepted a chain mismatch"
fi

cat >"$fake_bin/forge" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$FAKE_DEPLOY_LOG"
case " $* " in
    *ExperimentalCostedDirectVerifier*)
        printf '%s\n' '{"deployedTo":"0x1111111111111111111111111111111111111111","transactionHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
        ;;
    *TrustMapGateway*)
        printf '%s\n' '{"deployedTo":"0x2222222222222222222222222222222222222222","transactionHash":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}'
        ;;
    *) exit 1 ;;
esac
EOF
cat >"$fake_bin/cast" <<'EOF'
#!/bin/sh
set -eu
command=$1
shift
case $command in
    chain-id) printf '%s\n' "${FAKE_RPC_CHAIN_ID:-10001}" ;;
    wallet)
        [ "$1" = address ] || exit 1
        case " $* " in
            *signer-1.json*) printf '%s\n' '0x3333333333333333333333333333333333333333' ;;
            *signer-2.json*) printf '%s\n' '0x4444444444444444444444444444444444444444' ;;
            *signer-3.json*) printf '%s\n' '0x5555555555555555555555555555555555555555' ;;
            *) exit 1 ;;
        esac
        ;;
    send) printf '%s\n' '{"transactionHash":"0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}' ;;
    call)
        case " $* " in
            *' directVerifier()(address) '*) printf '%s\n' '0x1111111111111111111111111111111111111111' ;;
            *' gateway()(address) '*) printf '%s\n' '0x2222222222222222222222222222222222222222' ;;
            *) exit 1 ;;
        esac
        ;;
    code) printf '%s\n' '0x6001' ;;
    keccak) printf '%s\n' '0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' ;;
    block-number) printf '%s\n' '7' ;;
    *) exit 1 ;;
esac
EOF
chmod 0755 "$fake_bin/forge" "$fake_bin/cast"
ln -s "$(command -v jq)" "$fake_bin/jq"

deploy_case=$test_tmp/deploy-case
mkdir -p "$deploy_case"
for file in deployer.json deployer-password signer-1.json signer-password-1 signer-2.json signer-password-2 signer-3.json signer-password-3; do
    printf '%s\n' 'test-only-deploy-secret' >"$deploy_case/$file"
done
cat >"$deploy_case/profile.json" <<EOF
{
  "version": 1,
  "profile_id": "local-cost-2x3",
  "contract_name": "ExperimentalCostedDirectVerifier",
  "authorized_signers": [
    {"address":"0x3333333333333333333333333333333333333333","keystore_file":"$deploy_case/signer-1.json","password_file":"$deploy_case/signer-password-1"},
    {"address":"0x4444444444444444444444444444444444444444","keystore_file":"$deploy_case/signer-2.json","password_file":"$deploy_case/signer-password-2"}
  ],
  "signature_checks": 2,
  "hash_rounds": 3,
  "measured_direct_cost_gas": null
}
EOF
export FAKE_DEPLOY_LOG=$test_tmp/deploy.log
manifest=$deploy_case/deployment.json
if PATH="$fake_bin:$PATH" sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-missing.out" 2>&1; then
    fail "deploy script accepted missing environment"
fi
[ ! -e "$manifest" ] || fail "missing-env deployment wrote a manifest"
if PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10002 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$deploy_case/profile.json" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-mismatch.out" 2>&1; then
    fail "deploy script accepted RPC chain mismatch"
fi
[ ! -e "$manifest" ] || fail "chain-mismatch deployment wrote a manifest"
mismatched_profile=$deploy_case/profile-mismatched.json
jq '(.authorized_signers[0].address) = "0x5555555555555555555555555555555555555555"' "$deploy_case/profile.json" >"$mismatched_profile"
if PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10001 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$mismatched_profile" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-signer-mismatch.out" 2>&1; then
    fail "deploy script accepted a signer keystore/address mismatch"
fi
[ ! -e "$manifest" ] || fail "signer-mismatch deployment wrote a manifest"

custom_cost_profile=$deploy_case/profile-custom-cost.json
jq '.measured_direct_cost_gas = 3000096' "$deploy_case/profile.json" >"$custom_cost_profile"
if PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10001 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$custom_cost_profile" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-custom-cost.out" 2>&1; then
    fail "deploy script accepted measured cost on a custom profile"
fi
[ ! -e "$manifest" ] || fail "invalid custom-cost deployment wrote a manifest"

cat >"$deploy_case/profile-reserved.json" <<EOF
{
  "version": 1,
  "profile_id": "pow-spv-3m",
  "contract_name": "ExperimentalCostedDirectVerifier",
  "authorized_signers": [
    {"address":"0x3333333333333333333333333333333333333333","keystore_file":"$deploy_case/signer-1.json","password_file":"$deploy_case/signer-password-1"},
    {"address":"0x4444444444444444444444444444444444444444","keystore_file":"$deploy_case/signer-2.json","password_file":"$deploy_case/signer-password-2"},
    {"address":"0x5555555555555555555555555555555555555555","keystore_file":"$deploy_case/signer-3.json","password_file":"$deploy_case/signer-password-3"}
  ],
  "signature_checks": 3,
  "hash_rounds": 4497,
  "measured_direct_cost_gas": 3000096
}
EOF
reserved_bad_rounds=$deploy_case/profile-reserved-bad-rounds.json
jq '.hash_rounds = 4496' "$deploy_case/profile-reserved.json" >"$reserved_bad_rounds"
if PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10001 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$reserved_bad_rounds" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-reserved-rounds.out" 2>&1; then
    fail "deploy script accepted altered reserved hash rounds"
fi
[ ! -e "$manifest" ] || fail "invalid reserved-rounds deployment wrote a manifest"

reserved_bad_cost=$deploy_case/profile-reserved-bad-cost.json
jq '.measured_direct_cost_gas = 3000095' "$deploy_case/profile-reserved.json" >"$reserved_bad_cost"
if PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10001 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$reserved_bad_cost" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-reserved-cost.out" 2>&1; then
    fail "deploy script accepted altered reserved measured cost"
fi
[ ! -e "$manifest" ] || fail "invalid reserved-cost deployment wrote a manifest"

: >"$FAKE_DEPLOY_LOG"
PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10001 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$deploy_case/profile.json" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-success.out" 2>&1
jq -e '.status == "deployed" and .chainId == "10001" and .profileId == "local-cost-2x3" and .signatureChecks == 2 and .hashRounds == 3 and .measuredDirectCostGas == null and (.authorizedSigners | length) == 2' "$manifest" >/dev/null || fail "deployment manifest content is invalid"
[ "$(stat -c '%a' "$manifest")" = 644 ] || fail "deployment manifest mode is not 0644"
if grep -R -F 'test-only-deploy-secret' "$test_tmp" --exclude='deployer-password' --exclude='signer-password-*' --exclude='*.json' >/dev/null; then
    fail "deploy script exposed a secret"
fi

rm -f "$manifest"
PATH="$fake_bin:$PATH" RPC_URL=http://geth:8545 CHAIN_ID=10001 MERKLE_DEPTH=8 \
    AUTHORIZED_SIGNERS_FILE="$deploy_case/profile-reserved.json" DEPLOYER_KEYSTORE="$deploy_case/deployer.json" \
    DEPLOYER_PASSWORD_FILE="$deploy_case/deployer-password" MANIFEST_PATH="$manifest" \
    sh "$repo_root/scripts/deploy-chain.sh" >"$test_tmp/deploy-reserved-success.out" 2>&1
jq -e '.profileId == "pow-spv-3m" and (.authorizedSigners | length) == 3 and .signatureChecks == 3 and .hashRounds == 4497 and .measuredDirectCostGas == 3000096' "$manifest" >/dev/null || fail "reserved deployment manifest calibration is invalid"

if [ "${TRUSTMAP_SKIP_DOCKER_BUILD:-0}" = 1 ]; then
    printf '%s\n' 'container_build_test: static and behavior checks passed; Docker builds skipped by request'
    exit 0
fi
command -v docker >/dev/null 2>&1 || fail "docker is required (set TRUSTMAP_SKIP_DOCKER_BUILD=1 only in environments without Docker)"

build_failed=0
if ! docker build --build-arg "GETH_IMAGE=$GETH_IMAGE" -f "$repo_root/docker/geth.Dockerfile" -t trustmap-test-geth "$repo_root"; then
    build_failed=1
fi
if ! docker build \
    --build-arg "GO_IMAGE=$GO_IMAGE" \
    --build-arg "DISTROLESS_IMAGE=$DISTROLESS_IMAGE" \
    -f "$repo_root/docker/mapnode.Dockerfile" -t trustmap-test-mapnode "$repo_root"; then
    build_failed=1
fi
if ! docker build \
    --build-arg "FOUNDRY_IMAGE=$FOUNDRY_IMAGE" \
    --build-arg "JQ_IMAGE=$JQ_IMAGE" \
    -f "$repo_root/docker/deployer.Dockerfile" -t trustmap-test-deployer "$repo_root"; then
    build_failed=1
fi
[ "$build_failed" -eq 0 ] || fail "one or more Docker builds failed"

printf '%s\n' 'container_build_test: static, behavior, and Docker build checks passed'
