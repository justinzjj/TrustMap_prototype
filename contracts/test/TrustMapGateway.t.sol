// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {IDirectVerifier} from "../src/IDirectVerifier.sol";
import {PathProofVerifier} from "../src/PathProofVerifier.sol";
import {TrustMapGateway} from "../src/TrustMapGateway.sol";
import {TrustRootCommitment} from "../src/TrustRootCommitment.sol";
import {TestBase, Vm} from "./utils/TestBase.sol";

contract TestDirectVerifier is IDirectVerifier {
    bytes32 internal root;

    constructor(bytes32 initialRoot) {
        root = initialRoot;
    }

    function setRoot(bytes32 newRoot) external {
        root = newRoot;
    }

    function verify(uint256, uint256, bytes32, bytes calldata) external view returns (bytes32 sourceTrustRoot) {
        return root;
    }
}

contract RevertingDirectVerifier is IDirectVerifier {
    error DeliberateFailure();

    function verify(uint256, uint256, bytes32, bytes calldata) external pure returns (bytes32) {
        revert DeliberateFailure();
    }
}

contract ReentrantDirectVerifier is IDirectVerifier {
    TrustMapGateway private gateway;
    bytes32 private requestId;
    bytes32 private sourceRoot;
    bool private attempted;
    bool public reentrySucceeded;
    bool public reentryRejected;

    function configure(TrustMapGateway gateway_, bytes32 requestId_, bytes32 sourceRoot_) external {
        gateway = gateway_;
        requestId = requestId_;
        sourceRoot = sourceRoot_;
    }

    function verify(uint256, uint256, bytes32, bytes calldata) external returns (bytes32) {
        if (!attempted) {
            attempted = true;
            try gateway.verifyDirectAndRecord(requestId, "reentrant") {
                reentrySucceeded = true;
            } catch {
                reentryRejected = true;
            }
        }
        return sourceRoot;
    }
}

contract TrustMapGatewayTest is TestBase {
    uint256 private constant SOURCE_CHAIN = 10006;
    uint256 private constant SOURCE_HEIGHT = 55;
    bytes32 private constant SOURCE_BLOCK_HASH = bytes32(uint256(0xabc));
    bytes32 private constant SOURCE_ROOT = bytes32(uint256(0xdef));

    TestDirectVerifier private direct;
    TrustMapGateway private gateway;

    function setUp() public {
        vm.roll(100);
        vm.setBlockhash(99, bytes32(uint256(0x9999)));
        direct = new TestDirectVerifier(SOURCE_ROOT);
        gateway = new TrustMapGateway(3, IDirectVerifier(address(direct)), bytes32(0));
    }

    function testFirstDependencyInsertsAnchorThenDependency() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        (bytes32 dependencyKey, bool recorded) = gateway.verifyDirectAndRecord(requestId, "");

        bytes32 anchorLeaf = gateway.paperLeaf(bytes32(0), bytes32(uint256(0x9999)));
        bytes32 rootAfterAnchor = _rootForSingleLeaf(anchorLeaf, 3);
        bytes32 dependencyLeaf = gateway.paperLeaf(SOURCE_ROOT, SOURCE_BLOCK_HASH);
        bytes32 expectedRoot = _rootAfterSecondLeaf(anchorLeaf, dependencyLeaf, 3);

        assertTrue(recorded);
        assertEq(gateway.currentTrustRoot(), expectedRoot);
        assertEq(gateway.currentLeafCount(), 2);
        assertEq(gateway.activeTreeBlock(), 100);
        assertEq(gateway.anchorRootForActiveBlock(), rootAfterAnchor);
        assertEq(
            dependencyKey, gateway.computeDependencyKey(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT)
        );
        assertTrue(gateway.dependencyRecorded(dependencyKey));
        assertTrue(gateway.requestResolved(requestId));
    }

    function testSameBlockDoesNotRepeatAnchor() public {
        _recordDirect(SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);
        direct.setRoot(bytes32(uint256(0x1001)));
        bytes32 secondHash = bytes32(uint256(0x1002));
        _recordDirect(SOURCE_HEIGHT + 1, secondHash, bytes32(uint256(0x1001)));

        assertEq(gateway.currentLeafCount(), 3);
    }

    function testNewBlockAnchorsPreviousTrustRootRecursively() public {
        _recordDirect(SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);
        bytes32 previousRoot = gateway.currentTrustRoot();

        vm.roll(101);
        vm.setBlockhash(100, bytes32(uint256(0xaaaa)));
        direct.setRoot(bytes32(uint256(0x2001)));
        _recordDirect(SOURCE_HEIGHT + 1, bytes32(uint256(0x2002)), bytes32(uint256(0x2001)));

        bytes32 expectedAnchor = gateway.paperLeaf(previousRoot, bytes32(uint256(0xaaaa)));
        assertEq(gateway.anchorRootForActiveBlock(), _rootForSingleLeaf(expectedAnchor, 3));
        assertEq(gateway.currentLeafCount(), 2);
        assertEq(gateway.activeTreeBlock(), 101);
    }

    function testDuplicateDependencyDoesNotAddLeafButResolvesNewRequest() public {
        bytes32 firstRequest = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        (bytes32 key,) = gateway.verifyDirectAndRecord(firstRequest, "");
        bytes32 rootAfterFirst = gateway.currentTrustRoot();

        bytes32 secondRequest = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        (bytes32 secondKey, bool recorded) = gateway.verifyDirectAndRecord(secondRequest, "different proof");

        assertEq(secondKey, key);
        assertFalse(recorded);
        assertEq(gateway.currentLeafCount(), 2);
        assertEq(gateway.currentTrustRoot(), rootAfterFirst);
        assertTrue(gateway.requestResolved(secondRequest));
    }

    function testRequestCannotBeResolvedTwice() public {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        gateway.verifyDirectAndRecord(requestId, "");

        vm.expectRevert(TrustMapGateway.RequestAlreadyResolved.selector);
        gateway.verifyDirectAndRecord(requestId, "");
    }

    function testDirectFailureRollsBackAllState() public {
        TrustMapGateway failing =
            new TrustMapGateway(3, IDirectVerifier(address(new RevertingDirectVerifier())), bytes32(uint256(77)));
        bytes32 requestId = failing.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);

        vm.expectRevert(RevertingDirectVerifier.DeliberateFailure.selector);
        failing.verifyDirectAndRecord(requestId, "");

        assertEq(failing.currentTrustRoot(), bytes32(uint256(77)));
        assertEq(failing.currentLeafCount(), 0);
        assertFalse(failing.requestResolved(requestId));
        assertFalse(failing.getRequest(requestId).processing);
    }

    function testPathSuccessRecordsBaseRootAndRequestContext() public {
        bytes32 baseRoot = bytes32(uint256(0x301));
        bytes32 pathBlockHash = bytes32(uint256(0x302));
        PathProofVerifier.Witness memory witness =
            _witness(1, bytes32(uint256(0x303)), bytes32(uint256(0x304)), bytes32(uint256(0x305)));
        bytes32 finalRoot = _compute(baseRoot, pathBlockHash, witness);
        TrustMapGateway pathGateway = new TrustMapGateway(3, IDirectVerifier(address(direct)), finalRoot);
        bytes32 requestId = pathGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, pathBlockHash);
        bytes32[] memory hashes = new bytes32[](1);
        hashes[0] = pathBlockHash;
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = witness;

        pathGateway.verifyPathAndRecord(requestId, baseRoot, hashes, witnesses);

        bytes32 expectedKey = keccak256(abi.encode(SOURCE_CHAIN, SOURCE_HEIGHT, pathBlockHash, baseRoot));
        assertTrue(pathGateway.dependencyRecorded(expectedKey));
        assertTrue(pathGateway.requestResolved(requestId));
    }

    function testPathRejectsFirstHashThatDoesNotMatchRequestAndRollsBack() public {
        bytes32 initialRoot = bytes32(uint256(0x401));
        TrustMapGateway pathGateway = new TrustMapGateway(3, IDirectVerifier(address(direct)), initialRoot);
        bytes32 requestId = pathGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes32[] memory hashes = new bytes32[](1);
        hashes[0] = bytes32(uint256(0x402));
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = _witness(0, bytes32(0), bytes32(0), bytes32(0));

        vm.expectRevert(TrustMapGateway.SourceBlockHashMismatch.selector);
        pathGateway.verifyPathAndRecord(requestId, bytes32(uint256(0x403)), hashes, witnesses);

        assertEq(pathGateway.currentTrustRoot(), initialRoot);
        assertFalse(pathGateway.requestResolved(requestId));
        assertFalse(pathGateway.getRequest(requestId).processing);
    }

    function testPathFailureDoesNotFallbackOrMutate() public {
        bytes32 initialRoot = bytes32(uint256(0x501));
        TrustMapGateway pathGateway = new TrustMapGateway(3, IDirectVerifier(address(direct)), initialRoot);
        bytes32 requestId = pathGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        bytes32[] memory hashes = new bytes32[](1);
        hashes[0] = SOURCE_BLOCK_HASH;
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = _witness(0, bytes32(0), bytes32(0), bytes32(0));

        vm.expectRevert(PathProofVerifier.FinalRootMismatch.selector);
        pathGateway.verifyPathAndRecord(requestId, bytes32(uint256(0x502)), hashes, witnesses);

        assertEq(pathGateway.currentTrustRoot(), initialRoot);
        assertEq(pathGateway.currentLeafCount(), 0);
        assertFalse(pathGateway.requestResolved(requestId));
        assertFalse(pathGateway.getRequest(requestId).processing);
    }

    function testTreeFullRevertsAtomically() public {
        TrustMapGateway small = new TrustMapGateway(1, IDirectVerifier(address(direct)), bytes32(0));
        bytes32 first = small.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        small.verifyDirectAndRecord(first, "");
        bytes32 fullRoot = small.currentTrustRoot();
        direct.setRoot(bytes32(uint256(0x6001)));
        bytes32 second = small.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT + 1, bytes32(uint256(0x6002)));

        vm.expectRevert(TrustRootCommitment.TreeFull.selector);
        small.verifyDirectAndRecord(second, "");

        assertEq(small.currentTrustRoot(), fullRoot);
        assertEq(small.currentLeafCount(), 2);
        assertFalse(small.requestResolved(second));
        assertFalse(small.getRequest(second).processing);
    }

    function testDuplicateDependencyInLaterBlockDoesNotStartTreeUntilNewDependency() public {
        _recordDirect(SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);
        bytes32 originalRoot = gateway.currentTrustRoot();

        vm.roll(101);
        vm.setBlockhash(100, bytes32(uint256(0x7301)));
        bytes32 duplicate = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        gateway.verifyDirectAndRecord(duplicate, "duplicate");

        assertEq(gateway.currentTrustRoot(), originalRoot);
        assertEq(gateway.activeTreeBlock(), 100);
        assertEq(gateway.currentLeafCount(), 2);

        direct.setRoot(bytes32(uint256(0x7302)));
        _recordDirect(SOURCE_HEIGHT + 1, bytes32(uint256(0x7303)), bytes32(uint256(0x7302)));
        assertEq(gateway.activeTreeBlock(), 101);
        assertEq(gateway.currentLeafCount(), 2);
    }

    function testTreeFullRequestCanRetrySuccessfullyInNextBlock() public {
        TrustMapGateway small = new TrustMapGateway(1, IDirectVerifier(address(direct)), bytes32(0));
        bytes32 first = small.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        small.verifyDirectAndRecord(first, "");

        direct.setRoot(bytes32(uint256(0x7401)));
        bytes32 secondHash = bytes32(uint256(0x7402));
        bytes32 second = small.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT + 1, secondHash);
        vm.expectRevert(TrustRootCommitment.TreeFull.selector);
        small.verifyDirectAndRecord(second, "");
        assertFalse(small.getRequest(second).processing);

        vm.roll(101);
        vm.setBlockhash(100, bytes32(uint256(0x7403)));
        small.verifyDirectAndRecord(second, "retry");

        assertTrue(small.requestResolved(second));
        assertEq(small.activeTreeBlock(), 101);
        assertEq(small.currentLeafCount(), 2);
    }

    function testReentrantDirectVerifierCannotResolveSameRequestTwice() public {
        ReentrantDirectVerifier reentrant = new ReentrantDirectVerifier();
        TrustMapGateway guarded = new TrustMapGateway(3, IDirectVerifier(address(reentrant)), bytes32(0));
        bytes32 requestId = guarded.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        reentrant.configure(guarded, requestId, SOURCE_ROOT);

        guarded.verifyDirectAndRecord(requestId, "outer");

        assertTrue(reentrant.reentryRejected());
        assertFalse(reentrant.reentrySucceeded());
        assertTrue(guarded.requestResolved(requestId));
        assertEq(guarded.currentLeafCount(), 2);
    }

    function testHistoricalUpdateBlockRootRetainsLatestRootPerBlock() public {
        _recordDirect(SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);
        bytes32 firstInBlock = gateway.currentTrustRoot();
        assertEq(gateway.trustRootByUpdateBlock(100), firstInBlock);

        direct.setRoot(bytes32(uint256(0x7101)));
        _recordDirect(SOURCE_HEIGHT + 1, bytes32(uint256(0x7102)), bytes32(uint256(0x7101)));
        bytes32 finalInBlock = gateway.currentTrustRoot();
        assertEq(gateway.trustRootByUpdateBlock(100), finalInBlock);
        assertTrue(gateway.hasTrustRootUpdateAtBlock(100));

        vm.roll(101);
        vm.setBlockhash(100, bytes32(uint256(0x7103)));
        direct.setRoot(bytes32(uint256(0x7104)));
        _recordDirect(SOURCE_HEIGHT + 2, bytes32(uint256(0x7105)), bytes32(uint256(0x7104)));

        assertEq(gateway.trustRootByUpdateBlock(100), finalInBlock);
        assertEq(gateway.trustRootByUpdateBlock(101), gateway.currentTrustRoot());
        assertTrue(gateway.hasTrustRootUpdateAtBlock(101));
    }

    function testRejectsZeroSourceChainId() public {
        vm.expectRevert(TrustMapGateway.InvalidSourceChainId.selector);
        gateway.requestVerification(0, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
    }

    function testRejectsZeroSourceBlockHash() public {
        vm.expectRevert(TrustMapGateway.InvalidSourceBlockHash.selector);
        gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, bytes32(0));
    }

    function testRequestAndRollWithoutSuccessfulVerificationDoesNotChangeRoot() public {
        bytes32 initialRoot = gateway.currentTrustRoot();
        gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        vm.roll(101);
        vm.setBlockhash(100, bytes32(uint256(0x7201)));

        assertEq(gateway.currentTrustRoot(), initialRoot);
        assertEq(gateway.currentLeafCount(), 0);
        assertFalse(gateway.hasTrustRootUpdateAtBlock(100));
        assertFalse(gateway.hasTrustRootUpdateAtBlock(101));
    }

    function testNoTrustRootSetterExists() public {
        bytes32 beforeRoot = gateway.currentTrustRoot();
        (bool ok,) = address(gateway).call(abi.encodeWithSignature("setTrustRoot(bytes32)", bytes32(uint256(1))));

        assertFalse(ok);
        assertEq(gateway.currentTrustRoot(), beforeRoot);
    }

    function testAllDirectLifecycleEventsHaveKeyFieldsAndRequiredOrder() public {
        vm.recordLogs();
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH);
        Vm.Log[] memory requestLogs = vm.getRecordedLogs();
        assertEq(requestLogs.length, 1);
        assertEq(requestLogs[0].emitter, address(gateway));
        assertEq(
            requestLogs[0].topics[0],
            keccak256("VerificationRequested(bytes32,address,uint256,uint256,bytes32,uint256)")
        );
        assertEq(requestLogs[0].topics[1], requestId);
        assertEq(requestLogs[0].topics[2], bytes32(uint256(uint160(address(this)))));
        assertEq(requestLogs[0].topics[3], bytes32(SOURCE_CHAIN));
        (uint256 requestedHeight, bytes32 requestedHash, uint256 nonce) =
            abi.decode(requestLogs[0].data, (uint256, bytes32, uint256));
        assertEq(requestedHeight, SOURCE_HEIGHT);
        assertEq(requestedHash, SOURCE_BLOCK_HASH);
        assertEq(nonce, 0);

        vm.recordLogs();
        gateway.verifyDirectAndRecord(requestId, "");
        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 5);
        assertEq(
            logs[0].topics[0], keccak256("DirectVerificationSucceeded(bytes32,uint256,uint256,bytes32,bytes32,bytes32)")
        );
        assertEq(logs[1].topics[0], keccak256("TrustRootUpdated(bytes32,uint32,bytes32,bytes32,bytes32,bool)"));
        assertEq(logs[2].topics[0], keccak256("TrustRootUpdated(bytes32,uint32,bytes32,bytes32,bytes32,bool)"));
        assertEq(
            logs[3].topics[0], keccak256("DependencyRecorded(bytes32,bytes32,uint256,uint256,bytes32,bytes32,uint32)")
        );
        assertEq(logs[4].topics[0], keccak256("RequestResolved(bytes32,address,bytes32,bool,bytes32)"));

        bytes32 key = gateway.computeDependencyKey(SOURCE_CHAIN, SOURCE_HEIGHT, SOURCE_BLOCK_HASH, SOURCE_ROOT);
        _assertDirectSucceededLog(logs[0], requestId, key);
        _assertFirstAnchorAndDependencyLogs(logs[1], logs[2], requestId);
        _assertDependencyAndResolvedLogs(logs[3], logs[4], requestId, key);
    }

    function testPathVerificationSucceededEventHasSourceAndHopFields() public {
        bytes32 baseRoot = bytes32(uint256(0x7501));
        bytes32 sourceHash = bytes32(uint256(0x7502));
        PathProofVerifier.Witness memory witness =
            _witness(0, bytes32(uint256(0x7503)), bytes32(uint256(0x7504)), bytes32(uint256(0x7505)));
        bytes32 finalRoot = _compute(baseRoot, sourceHash, witness);
        TrustMapGateway pathGateway = new TrustMapGateway(3, IDirectVerifier(address(direct)), finalRoot);
        bytes32 requestId = pathGateway.requestVerification(SOURCE_CHAIN, SOURCE_HEIGHT, sourceHash);
        Vm.Log[] memory logs = _verifyPathAndGetLogs(pathGateway, requestId, baseRoot, sourceHash, witness);

        assertEq(logs.length, 5);
        _assertPathSucceededLog(logs[0], requestId, sourceHash, baseRoot);
    }

    function _verifyPathAndGetLogs(
        TrustMapGateway pathGateway,
        bytes32 requestId,
        bytes32 baseRoot,
        bytes32 sourceHash,
        PathProofVerifier.Witness memory witness
    ) private returns (Vm.Log[] memory logs) {
        bytes32[] memory hashes = new bytes32[](1);
        hashes[0] = sourceHash;
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = witness;
        vm.recordLogs();
        pathGateway.verifyPathAndRecord(requestId, baseRoot, hashes, witnesses);
        logs = vm.getRecordedLogs();
    }

    function _assertPathSucceededLog(Vm.Log memory log, bytes32 requestId, bytes32 sourceHash, bytes32 baseRoot)
        private
        pure
    {
        assertEq(
            log.topics[0],
            keccak256("PathVerificationSucceeded(bytes32,uint256,uint256,bytes32,bytes32,uint256,bytes32)")
        );
        assertEq(log.topics[1], requestId);
        assertEq(log.topics[2], bytes32(SOURCE_CHAIN));
        (uint256 height, bytes32 blockHash, bytes32 root, uint256 hops, bytes32 key) =
            abi.decode(log.data, (uint256, bytes32, bytes32, uint256, bytes32));
        assertEq(height, SOURCE_HEIGHT);
        assertEq(blockHash, sourceHash);
        assertEq(root, baseRoot);
        assertEq(hops, 1);
        assertEq(key, keccak256(abi.encode(SOURCE_CHAIN, SOURCE_HEIGHT, sourceHash, baseRoot)));
    }

    function _assertDirectSucceededLog(Vm.Log memory log, bytes32 requestId, bytes32 key) private pure {
        assertEq(log.topics[1], requestId);
        assertEq(log.topics[2], bytes32(SOURCE_CHAIN));
        (uint256 height, bytes32 blockHash, bytes32 root, bytes32 emittedKey) =
            abi.decode(log.data, (uint256, bytes32, bytes32, bytes32));
        assertEq(height, SOURCE_HEIGHT);
        assertEq(blockHash, SOURCE_BLOCK_HASH);
        assertEq(root, SOURCE_ROOT);
        assertEq(emittedKey, key);
    }

    function _assertFirstAnchorAndDependencyLogs(Vm.Log memory anchor, Vm.Log memory dependency, bytes32 requestId)
        private
        pure
    {
        assertEq(anchor.topics[1], requestId);
        assertEq(anchor.topics[2], bytes32(uint256(0)));
        (bytes32 anchorLeaf,, bytes32 anchorRoot, bool isAnchor) =
            abi.decode(anchor.data, (bytes32, bytes32, bytes32, bool));
        assertEq(anchorLeaf, keccak256(abi.encodePacked(bytes32(0), bytes32(uint256(0x9999)))));
        assertTrue(isAnchor);

        assertEq(dependency.topics[1], requestId);
        assertEq(dependency.topics[2], bytes32(uint256(1)));
        (bytes32 dependencyLeaf, bytes32 oldRoot,, bool isDependencyAnchor) =
            abi.decode(dependency.data, (bytes32, bytes32, bytes32, bool));
        assertEq(dependencyLeaf, keccak256(abi.encodePacked(SOURCE_ROOT, SOURCE_BLOCK_HASH)));
        assertEq(oldRoot, anchorRoot);
        assertFalse(isDependencyAnchor);
    }

    function _assertDependencyAndResolvedLogs(
        Vm.Log memory dependency,
        Vm.Log memory resolved,
        bytes32 requestId,
        bytes32 key
    ) private view {
        assertEq(dependency.topics[1], key);
        assertEq(dependency.topics[2], requestId);
        assertEq(dependency.topics[3], bytes32(SOURCE_CHAIN));
        (uint256 height, bytes32 blockHash, bytes32 root, uint32 leafIndex) =
            abi.decode(dependency.data, (uint256, bytes32, bytes32, uint32));
        assertEq(height, SOURCE_HEIGHT);
        assertEq(blockHash, SOURCE_BLOCK_HASH);
        assertEq(root, SOURCE_ROOT);
        assertEq(leafIndex, 1);

        assertEq(resolved.topics[1], requestId);
        assertEq(resolved.topics[2], bytes32(uint256(uint160(address(this)))));
        assertEq(resolved.topics[3], key);
        (bool newDependency, bytes32 homeRoot) = abi.decode(resolved.data, (bool, bytes32));
        assertTrue(newDependency);
        assertTrue(homeRoot != bytes32(0));
    }

    function _recordDirect(uint256 height, bytes32 blockHash, bytes32 expectedSourceRoot) private {
        bytes32 requestId = gateway.requestVerification(SOURCE_CHAIN, height, blockHash);
        gateway.verifyDirectAndRecord(requestId, "");
        assertTrue(
            gateway.dependencyRecorded(
                gateway.computeDependencyKey(SOURCE_CHAIN, height, blockHash, expectedSourceRoot)
            )
        );
    }

    function _witness(uint32 index, bytes32 s0, bytes32 s1, bytes32 s2)
        private
        pure
        returns (PathProofVerifier.Witness memory witness)
    {
        bytes32[] memory siblings = new bytes32[](3);
        siblings[0] = s0;
        siblings[1] = s1;
        siblings[2] = s2;
        witness = PathProofVerifier.Witness({leafIndex: index, siblings: siblings});
    }

    function _compute(bytes32 baseRoot, bytes32 blockHash, PathProofVerifier.Witness memory witness)
        private
        pure
        returns (bytes32 node)
    {
        node = keccak256(abi.encodePacked(baseRoot, blockHash));
        uint32 index = witness.leafIndex;
        for (uint256 i = 0; i < witness.siblings.length; ++i) {
            node = (index & 1) == 0
                ? keccak256(abi.encodePacked(node, witness.siblings[i]))
                : keccak256(abi.encodePacked(witness.siblings[i], node));
            index >>= 1;
        }
    }

    function _rootForSingleLeaf(bytes32 leaf, uint8 depth) private pure returns (bytes32 root) {
        root = leaf;
        bytes32 zero;
        for (uint256 i = 0; i < depth; ++i) {
            root = keccak256(abi.encodePacked(root, zero));
            zero = keccak256(abi.encodePacked(zero, zero));
        }
    }

    function _rootAfterSecondLeaf(bytes32 leaf0, bytes32 leaf1, uint8 depth) private pure returns (bytes32 root) {
        root = keccak256(abi.encodePacked(leaf0, leaf1));
        bytes32 zero = keccak256(abi.encodePacked(bytes32(0), bytes32(0)));
        for (uint256 i = 1; i < depth; ++i) {
            root = keccak256(abi.encodePacked(root, zero));
            zero = keccak256(abi.encodePacked(zero, zero));
        }
    }
}
