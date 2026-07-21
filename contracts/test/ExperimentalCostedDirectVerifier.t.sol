// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {ExperimentalCostedDirectVerifier} from "../src/ExperimentalCostedDirectVerifier.sol";
import {IDirectVerifier} from "../src/IDirectVerifier.sol";
import {TrustMapGateway} from "../src/TrustMapGateway.sol";
import {TestBase} from "./utils/TestBase.sol";

contract ExperimentalCostedDirectVerifierTest is TestBase {
    bytes32 private constant TEST_ATTESTATION_TYPEHASH = keccak256(
        "TrustMapExperimentalCostedDirect(address verifier,address gateway,uint256 homeChainId,uint256 sourceChainId,uint256 sourceHeight,bytes32 sourceBlockHash,bytes32 sourceTrustRoot)"
    );
    uint256 private constant SIGNER_KEY_0 = 0xa11ce;
    uint256 private constant SIGNER_KEY_1 = 0xb0b;
    uint256 private constant SIGNER_KEY_2 = 0xcafe;
    uint256 private constant UNAUTHORIZED_KEY = 0xdead;
    uint256 private constant SOURCE_CHAIN = 10006;
    uint256 private constant SOURCE_HEIGHT = 700;
    uint32 private constant SIGNATURE_CHECKS = 3;
    uint32 private constant HASH_ROUNDS = 4;
    uint32 private constant POW_SPV_CALIBRATION_HASH_ROUNDS = 4497;
    uint256 private constant POW_SPV_MIN_GAS = 2_700_000;
    uint256 private constant POW_SPV_MAX_GAS = 3_300_000;
    bytes32 private constant SOURCE_BLOCK_HASH = bytes32(uint256(0x701));
    bytes32 private constant SOURCE_ROOT = bytes32(uint256(0x702));

    ExperimentalCostedDirectVerifier private verifier;
    TrustMapGateway private gateway;

    event log_named_uint(string key, uint256 value);

    function setUp() public {
        vm.roll(200);
        vm.setBlockhash(199, bytes32(uint256(0x199)));
        (verifier, gateway) = _newBoundPairWithProfile(SIGNATURE_CHECKS, HASH_ROUNDS);
    }

    function testOrderedDistinctSignaturesVerifyStoredRequestContext() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);

        (bytes32 key, bool recorded) = gateway.verifyDirectAndRecord(requestId, _proof(verifier, gateway));

        assertTrue(recorded);
        assertEq(key, gateway.computeDependencyKey(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT));
        assertTrue(gateway.requestResolved(requestId));
    }

    function testCostProfileAndAuthorizedSignerOrderAreFixedAtDeployment() public {
        assertEq(verifier.signatureChecks(), SIGNATURE_CHECKS);
        assertEq(verifier.hashRounds(), HASH_ROUNDS);
        assertEq(verifier.authorizedSignerCount(), 3);
        assertEq(verifier.authorizedSigners(0), vm.addr(SIGNER_KEY_0));
        assertEq(verifier.authorizedSigners(1), vm.addr(SIGNER_KEY_1));
        assertEq(verifier.authorizedSigners(2), vm.addr(SIGNER_KEY_2));
        assertTrue(verifier.isAuthorizedSigner(vm.addr(SIGNER_KEY_0)));
        assertTrue(verifier.isAuthorizedSigner(vm.addr(SIGNER_KEY_1)));
        assertTrue(verifier.isAuthorizedSigner(vm.addr(SIGNER_KEY_2)));
    }

    function testRejectsEmptyAuthorizedSignerSet() public {
        address[] memory signers = new address[](0);
        vm.expectRevert(ExperimentalCostedDirectVerifier.EmptyAuthorizedSigners.selector);
        new ExperimentalCostedDirectVerifier(signers, 1, 0);
    }

    function testRejectsZeroAuthorizedSigner() public {
        address[] memory signers = _authorizedSigners();
        signers[1] = address(0);
        vm.expectRevert(ExperimentalCostedDirectVerifier.ZeroAuthorizedSigner.selector);
        new ExperimentalCostedDirectVerifier(signers, SIGNATURE_CHECKS, HASH_ROUNDS);
    }

    function testRejectsDuplicateAuthorizedSigner() public {
        address[] memory signers = _authorizedSigners();
        signers[2] = signers[0];
        vm.expectRevert(ExperimentalCostedDirectVerifier.DuplicateAuthorizedSigner.selector);
        new ExperimentalCostedDirectVerifier(signers, SIGNATURE_CHECKS, HASH_ROUNDS);
    }

    function testRejectsZeroSignatureChecks() public {
        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignatureCheckCount.selector);
        new ExperimentalCostedDirectVerifier(_authorizedSigners(), 0, HASH_ROUNDS);
    }

    function testRejectsSignatureChecksAboveSignerCount() public {
        vm.expectRevert(ExperimentalCostedDirectVerifier.SignatureChecksExceedSignerCount.selector);
        new ExperimentalCostedDirectVerifier(_authorizedSigners(), SIGNATURE_CHECKS + 1, HASH_ROUNDS);
    }

    function testRejectsMoreThan4096SignatureChecks() public {
        vm.expectRevert(ExperimentalCostedDirectVerifier.TooManySignatureChecks.selector);
        new ExperimentalCostedDirectVerifier(_authorizedSigners(), 4097, HASH_ROUNDS);
    }

    function testRejectsMoreThan16384HashRounds() public {
        vm.expectRevert(ExperimentalCostedDirectVerifier.TooManyHashRounds.selector);
        new ExperimentalCostedDirectVerifier(_authorizedSigners(), SIGNATURE_CHECKS, 16385);
    }

    function testEveryConfiguredSignatureIsChecked() public {
        for (uint256 badIndex = 0; badIndex < SIGNATURE_CHECKS; ++badIndex) {
            bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
            bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
            signatures[badIndex] = _signature(
                UNAUTHORIZED_KEY,
                address(verifier),
                address(gateway),
                block.chainid,
                SOURCE_CHAIN,
                SOURCE_HEIGHT,
                SOURCE_BLOCK_HASH,
                SOURCE_ROOT,
                HASH_ROUNDS
            );

            vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
            gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
            _assertRequestRolledBack(gateway, requestId);
        }
    }

    function testRejectsSignaturesInWrongOrder() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
        (signatures[0], signatures[1]) = (signatures[1], signatures[0]);

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testRejectsWrongSigner() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
        signatures[1] = _signature(
            UNAUTHORIZED_KEY,
            address(verifier),
            address(gateway),
            block.chainid,
            SOURCE_CHAIN,
            SOURCE_HEIGHT,
            SOURCE_BLOCK_HASH,
            SOURCE_ROOT,
            HASH_ROUNDS
        );

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testRejectsWrongSignatureCount() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS - 1, HASH_ROUNDS);

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidProofSignatureCount.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testMalformedSignatureFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
        signatures[1] = hex"1234";

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignatureLength.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testHighSValueFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
        bytes32 highS = bytes32(uint256(0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0) + 1);
        signatures[1] = abi.encodePacked(bytes32(uint256(1)), highS, uint8(27));

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testInvalidVValueFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
        signatures[1] = abi.encodePacked(bytes32(uint256(1)), bytes32(uint256(1)), uint8(29));

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testMalformedDynamicProofFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        (bool ok,) =
            address(gateway).call(abi.encodeCall(TrustMapGateway.verifyDirectAndRecord, (requestId, hex"1234")));

        assertFalse(ok);
        _assertRequestRolledBack(gateway, requestId);
    }

    function testDigestMatchesIndependentHashRoundComputation() public view {
        bytes32 expected = _digest(
            address(verifier),
            address(gateway),
            block.chainid,
            SOURCE_CHAIN,
            SOURCE_HEIGHT,
            SOURCE_BLOCK_HASH,
            SOURCE_ROOT,
            HASH_ROUNDS
        );
        assertEq(verifier.attestationDigest(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT), expected);
    }

    function testChangedSourceChainIdInvalidatesProof() public {
        _expectContextMismatch(SOURCE_CHAIN + 1, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);
    }

    function testChangedSourceHeightInvalidatesProof() public {
        _expectContextMismatch(SOURCE_CHAIN, SOURCE_HEIGHT + 1, SOURCE_BLOCK_HASH, SOURCE_ROOT);
    }

    function testChangedSourceBlockHashInvalidatesProof() public {
        _expectContextMismatch(SOURCE_CHAIN, SOURCE_HEIGHT, bytes32(uint256(SOURCE_BLOCK_HASH) + 1), SOURCE_ROOT);
    }

    function testChangedSourceTrustRootInvalidatesProof() public {
        _expectContextMismatch(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, bytes32(uint256(SOURCE_ROOT) + 1));
    }

    function testReplayAcrossVerifierInstancesFails() public {
        (ExperimentalCostedDirectVerifier otherVerifier, TrustMapGateway otherGateway) =
            _newBoundPairWithProfile(SIGNATURE_CHECKS, HASH_ROUNDS);
        bytes32 requestId = otherGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, otherGateway, SIGNATURE_CHECKS, HASH_ROUNDS);

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        otherGateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(otherGateway, requestId);
        assertTrue(address(otherVerifier) != address(verifier));
    }

    function testReplayAcrossGatewayBindingsFails() public {
        (ExperimentalCostedDirectVerifier otherVerifier, TrustMapGateway otherGateway) =
            _newBoundPairWithProfile(SIGNATURE_CHECKS, HASH_ROUNDS);
        bytes32 requestId = otherGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(otherVerifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        otherGateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(otherGateway, requestId);
    }

    function testReplayAcrossHomeChainIdsFails() public {
        uint256 signedHomeChainId = block.chainid;
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);
        vm.chainId(signedHomeChainId + 1);

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function testOnlyBoundGatewayCanCallVerify() public {
        bytes memory proof = _proof(verifier, gateway);
        vm.expectRevert(ExperimentalCostedDirectVerifier.OnlyGateway.selector);
        verifier.verify(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, proof);
    }

    function testGatewayBindingIsOneTimeAndBidirectional() public {
        assertEq(verifier.gateway(), address(gateway));
        assertEq(address(gateway.directVerifier()), address(verifier));

        vm.expectRevert(ExperimentalCostedDirectVerifier.GatewayAlreadyBound.selector);
        verifier.bindGateway(address(gateway));
    }

    function testOnlyDeploymentBinderCanBindGateway() public {
        ExperimentalCostedDirectVerifier unbound =
            new ExperimentalCostedDirectVerifier(_authorizedSigners(), SIGNATURE_CHECKS, HASH_ROUNDS);
        TrustMapGateway targetGateway = new TrustMapGateway(3, IDirectVerifier(address(unbound)), bytes32(0));
        vm.prank(address(0xbeef));

        vm.expectRevert(ExperimentalCostedDirectVerifier.OnlyBinder.selector);
        unbound.bindGateway(address(targetGateway));
    }

    function testBindingRejectsGatewayConfiguredForDifferentVerifier() public {
        ExperimentalCostedDirectVerifier first =
            new ExperimentalCostedDirectVerifier(_authorizedSigners(), SIGNATURE_CHECKS, HASH_ROUNDS);
        ExperimentalCostedDirectVerifier second =
            new ExperimentalCostedDirectVerifier(_authorizedSigners(), SIGNATURE_CHECKS, HASH_ROUNDS);
        TrustMapGateway pairedWithSecond = new TrustMapGateway(3, IDirectVerifier(address(second)), bytes32(0));

        vm.expectRevert(ExperimentalCostedDirectVerifier.GatewayVerifierMismatch.selector);
        first.bindGateway(address(pairedWithSecond));
    }

    function testLargerCostProfileConsumesMoreVerificationGas() public {
        (ExperimentalCostedDirectVerifier baselineVerifier, TrustMapGateway baselineGateway) =
            _newBoundPairWithProfile(1, 0);

        uint256 baselineGas = _measureDirectVerification(baselineVerifier, baselineGateway, 1, 0);
        uint256 costedGas = _measureDirectVerification(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);

        assertTrue(costedGas > baselineGas);
    }

    /// @dev This calibrates synthetic execution cost only. It does not test or imply PoW SPV security.
    function testPowSpvCostCalibrationMeasuresFullGatewayVerification() public {
        (ExperimentalCostedDirectVerifier calibrationVerifier, TrustMapGateway calibrationGateway) =
            _newBoundPairWithProfile(SIGNATURE_CHECKS, POW_SPV_CALIBRATION_HASH_ROUNDS);

        uint256 gasUsed = _measureDirectVerification(
            calibrationVerifier, calibrationGateway, SIGNATURE_CHECKS, POW_SPV_CALIBRATION_HASH_ROUNDS
        );

        emit log_named_uint("pow-spv-3m verifyDirectAndRecord gas", gasUsed);
        assertTrue(gasUsed >= POW_SPV_MIN_GAS);
        assertTrue(gasUsed <= POW_SPV_MAX_GAS);
    }

    function _expectContextMismatch(
        uint256 requestSourceChain,
        uint256 requestSourceHeight,
        bytes32 requestSourceBlockHash,
        bytes32 proofSourceRoot
    ) private {
        bytes32 requestId = gateway.requestVerification(requestSourceChain, requestSourceHeight, requestSourceBlockHash);
        bytes[] memory signatures = _validSignatures(verifier, gateway, SIGNATURE_CHECKS, HASH_ROUNDS);

        vm.expectRevert(ExperimentalCostedDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(proofSourceRoot, signatures));
        _assertRequestRolledBack(gateway, requestId);
    }

    function _proof(ExperimentalCostedDirectVerifier targetVerifier, TrustMapGateway targetGateway)
        private
        returns (bytes memory)
    {
        return abi.encode(
            SOURCE_ROOT,
            _validSignatures(
                targetVerifier, targetGateway, targetVerifier.signatureChecks(), targetVerifier.hashRounds()
            )
        );
    }

    function _validSignatures(
        ExperimentalCostedDirectVerifier targetVerifier,
        TrustMapGateway targetGateway,
        uint32 checks,
        uint32 rounds
    ) private returns (bytes[] memory signatures) {
        signatures = new bytes[](checks);
        for (uint256 i = 0; i < checks; ++i) {
            signatures[i] = _signature(
                _signerKey(i),
                address(targetVerifier),
                address(targetGateway),
                block.chainid,
                SOURCE_CHAIN,
                SOURCE_HEIGHT,
                SOURCE_BLOCK_HASH,
                SOURCE_ROOT,
                rounds
            );
        }
    }

    function _signature(
        uint256 privateKey,
        address verifierAddress,
        address gatewayAddress,
        uint256 homeChainId,
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot,
        uint32 rounds
    ) private returns (bytes memory) {
        bytes32 digest = _digest(
            verifierAddress,
            gatewayAddress,
            homeChainId,
            sourceChainId,
            sourceHeight,
            sourceBlockHash,
            sourceTrustRoot,
            rounds
        );
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(privateKey, digest);
        return abi.encodePacked(r, s, v);
    }

    function _digest(
        address verifierAddress,
        address gatewayAddress,
        uint256 homeChainId,
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot,
        uint32 rounds
    ) private pure returns (bytes32) {
        bytes32 workHash = keccak256(
            abi.encode(
                TEST_ATTESTATION_TYPEHASH,
                verifierAddress,
                gatewayAddress,
                homeChainId,
                sourceChainId,
                sourceHeight,
                sourceBlockHash,
                sourceTrustRoot
            )
        );
        for (uint256 i = 0; i < rounds; ++i) {
            workHash = keccak256(abi.encodePacked(workHash, i));
        }
        return keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", workHash));
    }

    function _authorizedSigners() private returns (address[] memory signers) {
        signers = new address[](3);
        signers[0] = vm.addr(SIGNER_KEY_0);
        signers[1] = vm.addr(SIGNER_KEY_1);
        signers[2] = vm.addr(SIGNER_KEY_2);
    }

    function _signerKey(uint256 index) private pure returns (uint256) {
        if (index == 0) return SIGNER_KEY_0;
        if (index == 1) return SIGNER_KEY_1;
        if (index == 2) return SIGNER_KEY_2;
        revert("test signer index out of range");
    }

    function _newBoundPairWithProfile(uint32 checks, uint32 rounds)
        private
        returns (ExperimentalCostedDirectVerifier targetVerifier, TrustMapGateway targetGateway)
    {
        targetVerifier = new ExperimentalCostedDirectVerifier(_authorizedSigners(), checks, rounds);
        targetGateway = new TrustMapGateway(3, IDirectVerifier(address(targetVerifier)), bytes32(0));
        targetVerifier.bindGateway(address(targetGateway));
    }

    function _measureDirectVerification(
        ExperimentalCostedDirectVerifier targetVerifier,
        TrustMapGateway targetGateway,
        uint32 checks,
        uint32 rounds
    ) private returns (uint256 gasUsed) {
        bytes32 requestId = targetGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory proof = abi.encode(SOURCE_ROOT, _validSignatures(targetVerifier, targetGateway, checks, rounds));

        // Model a separate top-level transaction: all contract slots start cold, while the
        // transaction recipient account itself is warm. The verifier remains a cold internal
        // call target until the Gateway invokes it.
        vm.cool(address(targetGateway));
        vm.cool(address(targetVerifier));
        assertTrue(address(targetGateway).code.length > 0);

        uint256 gasBefore = gasleft();
        targetGateway.verifyDirectAndRecord(requestId, proof);
        gasUsed = gasBefore - gasleft();
    }

    function _assertRequestRolledBack(TrustMapGateway targetGateway, bytes32 requestId) private view {
        TrustMapGateway.VerificationRequest memory request = targetGateway.getRequest(requestId);
        assertFalse(request.processing);
        assertFalse(request.resolved);
        assertEq(targetGateway.currentTrustRoot(), bytes32(0));
        assertEq(targetGateway.currentLeafCount(), 0);
    }
}
