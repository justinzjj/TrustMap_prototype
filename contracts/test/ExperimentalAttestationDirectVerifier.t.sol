// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {ExperimentalAttestationDirectVerifier} from "../src/ExperimentalAttestationDirectVerifier.sol";
import {IDirectVerifier} from "../src/IDirectVerifier.sol";
import {TrustMapGateway} from "../src/TrustMapGateway.sol";
import {TestBase} from "./utils/TestBase.sol";

contract ExperimentalAttestationDirectVerifierTest is TestBase {
    bytes32 private constant TEST_ATTESTATION_TYPEHASH = keccak256(
        "TrustMapExperimentalAttestation(address verifier,address gateway,uint256 homeChainId,uint256 sourceChainId,uint256 sourceHeight,bytes32 sourceBlockHash,bytes32 sourceTrustRoot)"
    );
    uint256 private constant SIGNER_KEY = 0xa11ce;
    uint256 private constant OTHER_KEY = 0xb0b;
    uint256 private constant SOURCE_CHAIN = 10006;
    uint256 private constant SOURCE_HEIGHT = 700;
    bytes32 private constant SOURCE_BLOCK_HASH = bytes32(uint256(0x701));
    bytes32 private constant SOURCE_ROOT = bytes32(uint256(0x702));

    ExperimentalAttestationDirectVerifier private verifier;
    TrustMapGateway private gateway;

    function setUp() public {
        vm.roll(200);
        vm.setBlockhash(199, bytes32(uint256(0x199)));
        verifier = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));
        gateway = new TrustMapGateway(3, IDirectVerifier(address(verifier)), bytes32(0));
        verifier.bindGateway(address(gateway));
    }

    function testAuthorizedAttestationVerifiesStoredRequestContext() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory proof = _proof(SIGNER_KEY, SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);

        (bytes32 key, bool recorded) = gateway.verifyDirectAndRecord(requestId, proof);

        assertTrue(recorded);
        assertEq(key, gateway.computeDependencyKey(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT));
        assertTrue(gateway.requestResolved(requestId));
    }

    function testOnlyBoundGatewayCanCallVerify() public {
        bytes memory proof = _proof(SIGNER_KEY, SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.OnlyGateway.selector);
        verifier.verify(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, proof);
    }

    function testWrongStoredContextFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT + 1, SOURCE_BLOCK_HASH);
        bytes memory proof = _proof(SIGNER_KEY, SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        assertEq(gateway.currentTrustRoot(), bytes32(0));
        assertEq(gateway.currentLeafCount(), 0);
        assertFalse(gateway.requestResolved(requestId));
    }

    function testChangedSourceChainIdInvalidatesIndependentSignature() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN + 1, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory proof = _proof(SIGNER_KEY, SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        assertFalse(gateway.requestResolved(requestId));
    }

    function testChangedSourceBlockHashInvalidatesIndependentSignature() public {
        bytes32 changedBlockHash = bytes32(uint256(SOURCE_BLOCK_HASH) + 1);
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, changedBlockHash);
        bytes memory proof = _proof(SIGNER_KEY, SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        assertFalse(gateway.requestResolved(requestId));
    }

    function testChangedSourceTrustRootInvalidatesIndependentSignature() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory signature = _signature(
            SIGNER_KEY,
            address(verifier),
            address(gateway),
            block.chainid,
            SOURCE_CHAIN,
            SOURCE_HEIGHT,
            SOURCE_BLOCK_HASH,
            SOURCE_ROOT
        );
        bytes memory proof = abi.encode(bytes32(uint256(SOURCE_ROOT) + 1), signature);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        assertFalse(gateway.requestResolved(requestId));
    }

    function testReplayAcrossVerifierInstancesFails() public {
        (ExperimentalAttestationDirectVerifier otherVerifier, TrustMapGateway otherGateway) = _newBoundPair();
        bytes32 requestId = otherGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory signature = _signature(
            SIGNER_KEY,
            address(verifier),
            address(otherGateway),
            block.chainid,
            SOURCE_CHAIN,
            SOURCE_HEIGHT,
            SOURCE_BLOCK_HASH,
            SOURCE_ROOT
        );

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        otherGateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signature));

        assertFalse(otherGateway.requestResolved(requestId));
        assertTrue(address(otherVerifier) != address(verifier));
    }

    function testReplayAcrossGatewayBindingsFails() public {
        (ExperimentalAttestationDirectVerifier otherVerifier, TrustMapGateway otherGateway) = _newBoundPair();
        bytes32 requestId = otherGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory signature = _signature(
            SIGNER_KEY,
            address(otherVerifier),
            address(gateway),
            block.chainid,
            SOURCE_CHAIN,
            SOURCE_HEIGHT,
            SOURCE_BLOCK_HASH,
            SOURCE_ROOT
        );

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        otherGateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signature));

        assertFalse(otherGateway.requestResolved(requestId));
        assertTrue(address(otherGateway) != address(gateway));
    }

    function testReplayAcrossHomeChainIdsFails() public {
        uint256 signedHomeChainId = block.chainid;
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory signature = _signature(
            SIGNER_KEY,
            address(verifier),
            address(gateway),
            signedHomeChainId,
            SOURCE_CHAIN,
            SOURCE_HEIGHT,
            SOURCE_BLOCK_HASH,
            SOURCE_ROOT
        );
        vm.chainId(signedHomeChainId + 1);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, abi.encode(SOURCE_ROOT, signature));

        assertFalse(gateway.requestResolved(requestId));
    }

    function testUnauthorizedSignerFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory proof = _proof(OTHER_KEY, SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        assertEq(gateway.currentTrustRoot(), bytes32(0));
        assertFalse(gateway.requestResolved(requestId));
    }

    function testMalformedSignatureFailsAtomically() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory proof = abi.encode(SOURCE_ROOT, hex"1234");

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignatureLength.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        assertEq(gateway.currentTrustRoot(), bytes32(0));
        assertFalse(gateway.requestResolved(requestId));
    }

    function testGatewayBindingIsOneTime() public {
        vm.expectRevert(ExperimentalAttestationDirectVerifier.GatewayAlreadyBound.selector);
        verifier.bindGateway(address(0x1234));
    }

    function testGatewayConstructorRejectsEoaVerifier() public {
        vm.expectRevert(TrustMapGateway.DirectVerifierMustBeContract.selector);
        new TrustMapGateway(3, IDirectVerifier(address(0x1234)), bytes32(0));
    }

    function testBindGatewayRejectsEoa() public {
        ExperimentalAttestationDirectVerifier unbound = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));

        vm.expectRevert(ExperimentalAttestationDirectVerifier.GatewayMustBeContract.selector);
        unbound.bindGateway(address(0x1234));
    }

    function testBindGatewayRejectsWrongVerifierPair() public {
        ExperimentalAttestationDirectVerifier first = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));
        ExperimentalAttestationDirectVerifier second = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));
        TrustMapGateway pairedWithSecond = new TrustMapGateway(3, IDirectVerifier(address(second)), bytes32(0));

        vm.expectRevert(ExperimentalAttestationDirectVerifier.GatewayVerifierMismatch.selector);
        first.bindGateway(address(pairedWithSecond));
    }

    function testCorrectBindingIsBidirectionallyConsistent() public view {
        assertEq(verifier.gateway(), address(gateway));
        assertEq(address(gateway.directVerifier()), address(verifier));
    }

    function testOnlyBinderCanBindGateway() public {
        ExperimentalAttestationDirectVerifier unbound = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));
        vm.prank(address(0xbeef));

        vm.expectRevert(ExperimentalAttestationDirectVerifier.OnlyBinder.selector);
        unbound.bindGateway(address(gateway));
    }

    function testBindGatewayRejectsZeroAddress() public {
        ExperimentalAttestationDirectVerifier unbound = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));

        vm.expectRevert(ExperimentalAttestationDirectVerifier.ZeroGateway.selector);
        unbound.bindGateway(address(0));
    }

    function testHighSValueFailsAndRollsBackProcessing() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes32 highS = bytes32(uint256(0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0) + 1);
        bytes memory proof = abi.encode(SOURCE_ROOT, abi.encodePacked(bytes32(uint256(1)), highS, uint8(27)));

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        TrustMapGateway.VerificationRequest memory request = gateway.getRequest(requestId);
        assertFalse(request.processing);
        assertFalse(request.resolved);
    }

    function testInvalidVFailsAndRollsBackProcessing() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes memory proof =
            abi.encode(SOURCE_ROOT, abi.encodePacked(bytes32(uint256(1)), bytes32(uint256(1)), uint8(29)));

        vm.expectRevert(ExperimentalAttestationDirectVerifier.InvalidSignature.selector);
        gateway.verifyDirectAndRecord(requestId, proof);

        TrustMapGateway.VerificationRequest memory request = gateway.getRequest(requestId);
        assertFalse(request.processing);
        assertFalse(request.resolved);
    }

    function testMalformedDynamicAbiProofRevertsSafelyAndRollsBackProcessing() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);

        // Solidity's abi.decode uses a generic revert for malformed dynamic ABI;
        // this test intentionally checks rollback rather than a custom selector.
        (bool ok,) =
            address(gateway).call(abi.encodeCall(TrustMapGateway.verifyDirectAndRecord, (requestId, hex"1234")));

        assertFalse(ok);
        TrustMapGateway.VerificationRequest memory request = gateway.getRequest(requestId);
        assertFalse(request.processing);
        assertFalse(request.resolved);
        assertEq(gateway.currentTrustRoot(), bytes32(0));
    }

    function _proof(
        uint256 privateKey,
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot
    ) private returns (bytes memory) {
        return abi.encode(
            sourceTrustRoot,
            _signature(
                privateKey,
                address(verifier),
                address(gateway),
                block.chainid,
                sourceChainId,
                sourceHeight,
                sourceBlockHash,
                sourceTrustRoot
            )
        );
    }

    function _signature(
        uint256 privateKey,
        address verifierAddress,
        address gatewayAddress,
        uint256 homeChainId,
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot
    ) private returns (bytes memory) {
        bytes32 payloadHash = keccak256(
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
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", payloadHash));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(privateKey, digest);
        return abi.encodePacked(r, s, v);
    }

    function _newBoundPair()
        private
        returns (ExperimentalAttestationDirectVerifier otherVerifier, TrustMapGateway otherGateway)
    {
        otherVerifier = new ExperimentalAttestationDirectVerifier(vm.addr(SIGNER_KEY));
        otherGateway = new TrustMapGateway(3, IDirectVerifier(address(otherVerifier)), bytes32(0));
        otherVerifier.bindGateway(address(otherGateway));
    }
}
