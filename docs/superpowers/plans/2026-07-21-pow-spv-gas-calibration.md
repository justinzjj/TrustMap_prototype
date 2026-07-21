# PoW-SPV Gas Calibration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the default experimental DirectVerifier consume approximately 3,000,000 gas for one successful `TrustMapGateway.verifyDirectAndRecord`, without claiming or implementing PoW SPV security.

**Architecture:** Finalize the already-approved `ExperimentalCostedDirectVerifier` interface with ordered, distinct authorized signers and deployment-fixed signature/hash work. Calibrate the default `pow-spv-3m` profile primarily through `hashRounds`; propagate the fixed profile and measured cost through topology output and deployment manifests so future Planner code consumes an observed cost rather than a guess.

**Tech Stack:** Solidity 0.8.30, Foundry 1.4.4, Cancun EVM, Go 1.24.10, YAML/JSON topology profiles, POSIX shell deployment tests.

---

### Task 1: Finalize the experimental costed verifier

**Files:**
- Create: `contracts/src/ExperimentalCostedDirectVerifier.sol`
- Create: `contracts/test/ExperimentalCostedDirectVerifier.t.sol`
- Delete: `contracts/src/ExperimentalAttestationDirectVerifier.sol`
- Delete: `contracts/test/ExperimentalAttestationDirectVerifier.t.sol`
- Modify: `contracts/test/utils/TestBase.sol`

- [ ] Write failing Foundry tests that require constructor-fixed `authorizedSigners[]`, `signatureChecks`, and `hashRounds`; three distinct valid signatures; rejection of duplicate/zero/wrong signers; exact proof length; full verifier/Gateway/home/source context binding; and one-time bidirectional Gateway binding.
- [ ] Run `forge test --root contracts --match-contract ExperimentalCostedDirectVerifierTest -vv` and confirm RED because the final contract/API does not exist.
- [ ] Implement `ExperimentalCostedDirectVerifier` with `DirectProof = abi.encode(sourceTrustRoot, signatures[])`, ordered signer checks, chained Keccak work, low-s ECDSA recovery, immutable scalar parameters, and no TrustRoot setter or light-client claim.
- [ ] Run the focused verifier and Gateway suites and confirm GREEN.
- [ ] Commit only the verifier rename/API closure as `feat: finalize experimental costed verifier`.

### Task 2: Lock the 3M Gas calibration test

**Files:**
- Modify: `contracts/test/ExperimentalCostedDirectVerifier.t.sol`
- Modify: `contracts/src/ExperimentalCostedDirectVerifier.sol` only if the calibrated upper bound requires changing `MAX_HASH_ROUNDS`

- [ ] Add a deterministic test measuring only one successful `TrustMapGateway.verifyDirectAndRecord` for a fresh dependency, excluding `requestVerification`; assert `2_700_000 <= gasUsed && gasUsed <= 3_300_000`.
- [ ] Set the initial candidate to three authorized signers, three signature checks, and `4544` hash rounds. Run the focused calibration test and confirm RED if the final multi-signer implementation shifts outside the interval or misses the intended center.
- [ ] Use the same test harness to probe nearby round counts in small increments, then fix the single round count whose measured result is closest to 3,000,000 gas. Do not tune by increasing signature calldata.
- [ ] Replace the probe with a fixed regression assertion and emit/log the measured gas for reproducibility. Run the test twice with `--no-match-test` omitted and confirm stable in-range results.
- [ ] Commit the calibration guard as `test: calibrate direct verification to PoW SPV cost`.

### Task 3: Propagate the calibrated profile

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/topology/render.go`
- Modify: `internal/topology/render_test.go`
- Modify: `internal/mapnodebootstrap/bootstrap.go`
- Modify: `internal/mapnodebootstrap/bootstrap_test.go`
- Modify: `configs/topology.yaml`
- Modify: `configs/topology-21.yaml`
- Modify: `scripts/deploy-chain.sh`
- Modify: `tests/integration/container_build_test.sh`

- [ ] Write failing Go and shell behavior tests requiring the reserved default profile `pow-spv-3m`, exactly three signers/checks, the calibrated fixed `hashRounds`, and a non-null measured direct cost copied unchanged into the deployment manifest.
- [ ] Run the focused Go and shell tests and confirm RED against the current `committee-3`, four-round, null-cost output.
- [ ] Update defaults and example topologies. Make the renderer attach the calibrated measured cost only when all reserved profile parameters match; reject attempts to reuse `pow-spv-3m` with altered parameters.
- [ ] Copy `measured_direct_cost_gas` into the deployment manifest and require MapNode bootstrap to cross-check it against the profile. Custom profiles remain allowed but uncalibrated and must not masquerade as `pow-spv-3m`.
- [ ] Render the 3-chain and 21-chain examples and confirm every chain has the same reserved profile parameters while preserving per-chain block-time overrides.
- [ ] Commit propagation as `feat: publish calibrated PoW SPV cost profile`.

### Task 4: Verification and paper-boundary review

**Files:**
- Modify only documentation if verification exposes an ambiguity; do not add real SPV logic.

- [ ] Run `forge fmt --root contracts --check` and `forge test --root contracts -vv`.
- [ ] Run `go test -race ./... -count=1`, `go vet ./...`, and `TRUSTMAP_SKIP_DOCKER_BUILD=1 sh tests/integration/container_build_test.sh`.
- [ ] Render both examples and run `docker compose ... config --quiet`; do not alter Docker proxy/network settings being handled in the other window.
- [ ] Run `git diff --check` and scan tracked files for runtime secrets.
- [ ] Request independent specification and code-quality reviews. Reviewers must confirm the 3M number is a cost calibration only, the security comments remain explicit, and Planner-facing cost cannot be supplied by a request submitter.
- [ ] Commit any review-only correction, then report the measured Gas, fixed profile parameters, test evidence, and remaining limitations.
