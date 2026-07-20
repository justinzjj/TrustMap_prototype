// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {PathProofVerifier} from "../src/PathProofVerifier.sol";
import {TestBase} from "./utils/TestBase.sol";

contract PathProofHarness is PathProofVerifier {
    bytes32 private homeRoot;

    constructor(uint8 depth) PathProofVerifier(depth) {}

    function setHomeRootForTest(bytes32 root) external {
        homeRoot = root;
    }

    function _homeTrustRoot() internal view override returns (bytes32) {
        return homeRoot;
    }
}

contract PathProofVerifierTest is TestBase {
    PathProofHarness private verifier;

    function setUp() public {
        verifier = new PathProofHarness(2);
    }

    function testPaperLeafGoldenVector() public view {
        bytes32 sourceRoot = bytes32(uint256(1));
        bytes32 sourceBlockHash = bytes32(uint256(2));

        bytes32 expected = 0xe90b7bceb6e7df5418fb78d8ee546e97c83a08bbccc01a0644d599ccd2a7c2e0;

        assertEq(verifier.paperLeaf(sourceRoot, sourceBlockHash), expected);
    }

    function testSharedPackedLeafFixtureParity() public view {
        bytes32 sourceRoot = 0x1111111111111111111111111111111111111111111111111111111111111111;
        bytes32 sourceBlockHash = 0x2222222222222222222222222222222222222222222222222222222222222222;
        bytes32 expected = 0x3e92e0db88d6afea9edc4eedf62fffa4d92bcdfc310dccbe943747fe8302e871;

        assertEq(verifier.paperLeaf(sourceRoot, sourceBlockHash), expected);
    }

    function testSharedDepthThreeWitnessFixtureParity() public {
        PathProofHarness depthThree = new PathProofHarness(3);
        bytes32[] memory siblings = new bytes32[](3);
        siblings[0] = 0x3e92e0db88d6afea9edc4eedf62fffa4d92bcdfc310dccbe943747fe8302e871;
        siblings[1] = 0xad3228b676f7d3cd4284a5443f17f1962b36e491b30a40b2405849e597ba5fb5;
        siblings[2] = 0xb4c11951957c6f8f642c4af61cd6b24640fec6dc7fc607ee8206a99e92410d30;
        PathProofVerifier.Witness memory witness = PathProofVerifier.Witness({leafIndex: 1, siblings: siblings});

        bytes32 actual =
            depthThree.computeRoot(0xc502f868a3f2d78c5adf18b41f606fc4c6cd8a4a9838125f03aadf235245b910, witness);

        assertEq(actual, 0xde56c791963d89622dc09a16a1f00afc2b31193d9c52006f89737ea7dd1201b7);
    }

    function testSharedTwoHopRecursiveFixtureParity() public {
        PathProofHarness depthThree = new PathProofHarness(3);
        bytes32 baseRoot = 0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;
        bytes32[] memory blockHashes = new bytes32[](2);
        blockHashes[0] = 0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb;
        blockHashes[1] = 0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc;
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](2);
        witnesses[0] = _witness3(
            1,
            0x0101010101010101010101010101010101010101010101010101010101010101,
            0x0202020202020202020202020202020202020202020202020202020202020202,
            0x0303030303030303030303030303030303030303030303030303030303030303
        );
        witnesses[1] = _witness3(
            6,
            0x0404040404040404040404040404040404040404040404040404040404040404,
            0x0505050505050505050505050505050505050505050505050505050505050505,
            0x0606060606060606060606060606060606060606060606060606060606060606
        );
        bytes32 intermediate = depthThree.computeRoot(depthThree.paperLeaf(baseRoot, blockHashes[0]), witnesses[0]);
        assertEq(intermediate, 0x31b1bf37bff2a1754f1dacf82e989e7801672a402980133a454c601e66657bfa);
        depthThree.setHomeRootForTest(0x211c83d83f7e830ff451fb5f434534e77b4bd19ae7ae9379f65a1aa8ee4f9203);

        assertTrue(depthThree.verifyPath(baseRoot, blockHashes, witnesses));
    }

    function testSingleHopPath() public {
        bytes32 baseRoot = bytes32(uint256(11));
        bytes32 blockHash = bytes32(uint256(12));
        bytes32 leaf = keccak256(abi.encodePacked(baseRoot, blockHash));
        bytes32 sibling0 = bytes32(uint256(13));
        bytes32 sibling1 = bytes32(uint256(14));
        bytes32 level1 = keccak256(abi.encodePacked(sibling0, leaf));
        bytes32 expectedRoot = keccak256(abi.encodePacked(level1, sibling1));
        verifier.setHomeRootForTest(expectedRoot);

        bytes32[] memory blockHashes = new bytes32[](1);
        blockHashes[0] = blockHash;
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = _witness(1, sibling0, sibling1);

        assertTrue(verifier.verifyPath(baseRoot, blockHashes, witnesses));
    }

    function testMultiHopPath() public {
        bytes32 baseRoot = bytes32(uint256(21));
        bytes32[] memory blockHashes = new bytes32[](2);
        blockHashes[0] = bytes32(uint256(22));
        blockHashes[1] = bytes32(uint256(23));

        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](2);
        witnesses[0] = _witness(0, bytes32(uint256(24)), bytes32(uint256(25)));
        bytes32 root1 = verifier.computeRoot(verifier.paperLeaf(baseRoot, blockHashes[0]), witnesses[0]);
        witnesses[1] = _witness(2, bytes32(uint256(26)), bytes32(uint256(27)));
        bytes32 expectedRoot = verifier.computeRoot(verifier.paperLeaf(root1, blockHashes[1]), witnesses[1]);
        verifier.setHomeRootForTest(expectedRoot);

        assertTrue(verifier.verifyPath(baseRoot, blockHashes, witnesses));
    }

    function testRejectsEmptyPath() public {
        bytes32[] memory blockHashes = new bytes32[](0);
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](0);

        vm.expectRevert(PathProofVerifier.EmptyPath.selector);
        verifier.verifyPath(bytes32(uint256(1)), blockHashes, witnesses);
    }

    function testRejectsPathLengthMismatch() public {
        bytes32[] memory blockHashes = new bytes32[](1);
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](0);

        vm.expectRevert(PathProofVerifier.PathLengthMismatch.selector);
        verifier.verifyPath(bytes32(uint256(1)), blockHashes, witnesses);
    }

    function testRejectsWrongSiblingLength() public {
        bytes32[] memory siblings = new bytes32[](1);
        PathProofVerifier.Witness memory witness = PathProofVerifier.Witness({leafIndex: 0, siblings: siblings});

        vm.expectRevert(PathProofVerifier.InvalidSiblingCount.selector);
        verifier.computeRoot(bytes32(uint256(1)), witness);
    }

    function testRejectsOutOfRangeIndex() public {
        PathProofVerifier.Witness memory witness = _witness(4, bytes32(0), bytes32(0));

        vm.expectRevert(PathProofVerifier.LeafIndexOutOfRange.selector);
        verifier.computeRoot(bytes32(uint256(1)), witness);
    }

    function testRejectsDepthZeroAndThirtyThree() public {
        vm.expectRevert(PathProofVerifier.InvalidTreeDepth.selector);
        new PathProofHarness(0);

        vm.expectRevert(PathProofVerifier.InvalidTreeDepth.selector);
        new PathProofHarness(33);
    }

    function testDepthThirtyTwoAcceptsMaxUint32WitnessIndex() public {
        PathProofHarness depthThirtyTwo = new PathProofHarness(32);
        bytes32[] memory siblings = new bytes32[](32);
        PathProofVerifier.Witness memory witness =
            PathProofVerifier.Witness({leafIndex: type(uint32).max, siblings: siblings});

        assertEq(depthThirtyTwo.pathTreeCapacity(), uint256(1) << 32);
        depthThirtyTwo.computeRoot(bytes32(uint256(1)), witness);
    }

    function testRejectsWrongBaseOrHashViaFinalRoot() public {
        bytes32 baseRoot = bytes32(uint256(31));
        bytes32[] memory blockHashes = new bytes32[](1);
        blockHashes[0] = bytes32(uint256(32));
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = _witness(0, bytes32(uint256(33)), bytes32(uint256(34)));
        bytes32 expectedRoot = verifier.computeRoot(verifier.paperLeaf(baseRoot, blockHashes[0]), witnesses[0]);
        verifier.setHomeRootForTest(expectedRoot);

        vm.expectRevert(PathProofVerifier.FinalRootMismatch.selector);
        verifier.verifyPath(bytes32(uint256(999)), blockHashes, witnesses);

        blockHashes[0] = bytes32(uint256(998));
        vm.expectRevert(PathProofVerifier.FinalRootMismatch.selector);
        verifier.verifyPath(baseRoot, blockHashes, witnesses);
    }

    function testRejectsWrongSiblingOrIndexViaFinalRoot() public {
        bytes32 baseRoot = bytes32(uint256(41));
        bytes32[] memory blockHashes = new bytes32[](1);
        blockHashes[0] = bytes32(uint256(42));
        PathProofVerifier.Witness[] memory witnesses = new PathProofVerifier.Witness[](1);
        witnesses[0] = _witness(0, bytes32(uint256(43)), bytes32(uint256(44)));
        bytes32 expectedRoot = verifier.computeRoot(verifier.paperLeaf(baseRoot, blockHashes[0]), witnesses[0]);
        verifier.setHomeRootForTest(expectedRoot);

        witnesses[0].siblings[0] = bytes32(uint256(997));
        vm.expectRevert(PathProofVerifier.FinalRootMismatch.selector);
        verifier.verifyPath(baseRoot, blockHashes, witnesses);

        witnesses[0] = _witness(1, bytes32(uint256(43)), bytes32(uint256(44)));
        vm.expectRevert(PathProofVerifier.FinalRootMismatch.selector);
        verifier.verifyPath(baseRoot, blockHashes, witnesses);
    }

    function _witness(uint32 index, bytes32 sibling0, bytes32 sibling1)
        private
        pure
        returns (PathProofVerifier.Witness memory witness)
    {
        bytes32[] memory siblings = new bytes32[](2);
        siblings[0] = sibling0;
        siblings[1] = sibling1;
        witness = PathProofVerifier.Witness({leafIndex: index, siblings: siblings});
    }

    function _witness3(uint32 index, bytes32 sibling0, bytes32 sibling1, bytes32 sibling2)
        private
        pure
        returns (PathProofVerifier.Witness memory witness)
    {
        bytes32[] memory siblings = new bytes32[](3);
        siblings[0] = sibling0;
        siblings[1] = sibling1;
        siblings[2] = sibling2;
        witness = PathProofVerifier.Witness({leafIndex: index, siblings: siblings});
    }
}
