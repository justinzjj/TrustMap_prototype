// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

/// @notice Maintains a block-local fixed-depth incremental Merkle commitment.
/// @dev It intentionally exposes no production root setter; updates are internal.
abstract contract TrustRootCommitment {
    error InvalidCommitmentDepth();
    error TreeFull();

    event TrustRootUpdated(
        bytes32 indexed requestId,
        uint32 indexed leafIndex,
        bytes32 leaf,
        bytes32 oldTrustRoot,
        bytes32 newTrustRoot,
        bool isAnchor
    );

    uint8 public immutable commitmentTreeDepth;
    uint64 public immutable commitmentTreeCapacity;
    bytes32 public currentTrustRoot;
    uint256 public activeTreeBlock;
    uint64 public currentLeafCount;
    bytes32 public anchorRootForActiveBlock;
    mapping(uint256 updateBlock => bytes32 finalRoot) public trustRootByUpdateBlock;
    mapping(uint256 updateBlock => bool updated) public hasTrustRootUpdateAtBlock;

    bytes32[] private frontier;
    bytes32[] private zeroSubtrees;

    constructor(uint8 depth, bytes32 initialTrustRoot) {
        if (depth == 0 || depth > 32) revert InvalidCommitmentDepth();
        commitmentTreeDepth = depth;
        commitmentTreeCapacity = uint64(1) << depth;
        currentTrustRoot = initialTrustRoot;

        frontier = new bytes32[](depth);
        zeroSubtrees = new bytes32[](depth);
        for (uint256 level = 1; level < depth; ++level) {
            zeroSubtrees[level] = keccak256(abi.encodePacked(zeroSubtrees[level - 1], zeroSubtrees[level - 1]));
        }
    }

    function _beginBlockTree(bytes32 requestId, bytes32 anchorLeaf) internal {
        for (uint256 level = 0; level < commitmentTreeDepth; ++level) {
            frontier[level] = bytes32(0);
        }
        activeTreeBlock = block.number;
        currentLeafCount = 0;
        _appendLeaf(requestId, anchorLeaf, true);
        anchorRootForActiveBlock = currentTrustRoot;
    }

    function _appendLeaf(bytes32 requestId, bytes32 leaf, bool isAnchor) internal returns (uint32 leafIndex) {
        uint64 count = currentLeafCount;
        if (count >= commitmentTreeCapacity) revert TreeFull();
        // Safe because count < capacity and depth is capped at 32.
        // forge-lint: disable-next-line(unsafe-typecast)
        leafIndex = uint32(count);

        bytes32 node = leaf;
        uint64 index = count;
        for (uint256 level = 0; level < commitmentTreeDepth; ++level) {
            if ((index & 1) == 0) {
                frontier[level] = node;
                node = keccak256(abi.encodePacked(node, zeroSubtrees[level]));
            } else {
                node = keccak256(abi.encodePacked(frontier[level], node));
            }
            index >>= 1;
        }

        bytes32 oldTrustRoot = currentTrustRoot;
        currentTrustRoot = node;
        currentLeafCount = count + 1;
        trustRootByUpdateBlock[block.number] = node;
        hasTrustRootUpdateAtBlock[block.number] = true;
        emit TrustRootUpdated(requestId, leafIndex, leaf, oldTrustRoot, node, isAnchor);
    }
}
