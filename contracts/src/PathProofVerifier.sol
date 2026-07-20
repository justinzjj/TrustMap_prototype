// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

/// @notice Verifies paper-specified TrustMap Merkle paths against a home root.
abstract contract PathProofVerifier {
    struct Witness {
        uint32 leafIndex;
        bytes32[] siblings;
    }

    error InvalidTreeDepth();
    error EmptyPath();
    error PathLengthMismatch();
    error InvalidSiblingCount();
    error LeafIndexOutOfRange();
    error FinalRootMismatch();

    uint8 public immutable pathTreeDepth;
    uint64 public immutable pathTreeCapacity;

    constructor(uint8 depth) {
        if (depth == 0 || depth > 32) revert InvalidTreeDepth();
        pathTreeDepth = depth;
        pathTreeCapacity = uint64(1) << depth;
    }

    /// @notice The paper leaf binds only the previous/source root and block hash.
    function paperLeaf(bytes32 sourceTrustRoot, bytes32 sourceBlockHash) public pure returns (bytes32) {
        return keccak256(abi.encodePacked(sourceTrustRoot, sourceBlockHash));
    }

    function computeRoot(bytes32 leaf, Witness memory witness) public view returns (bytes32 root) {
        if (witness.siblings.length != pathTreeDepth) revert InvalidSiblingCount();
        if (uint64(witness.leafIndex) >= pathTreeCapacity) revert LeafIndexOutOfRange();

        root = leaf;
        uint32 index = witness.leafIndex;
        for (uint256 level = 0; level < pathTreeDepth; ++level) {
            bytes32 sibling = witness.siblings[level];
            root = (index & 1) == 0
                ? keccak256(abi.encodePacked(root, sibling))
                : keccak256(abi.encodePacked(sibling, root));
            index >>= 1;
        }
    }

    /// @notice Permissionless verification; state is never modified.
    function verifyPath(bytes32 baseTrustRoot, bytes32[] calldata blockHashes, Witness[] calldata witnesses)
        external
        view
        returns (bool)
    {
        _verifyPath(baseTrustRoot, blockHashes, witnesses);
        return true;
    }

    function _verifyPath(bytes32 baseTrustRoot, bytes32[] calldata blockHashes, Witness[] calldata witnesses)
        internal
        view
        returns (bytes32 finalRoot)
    {
        uint256 length = blockHashes.length;
        if (length == 0) revert EmptyPath();
        if (length != witnesses.length) revert PathLengthMismatch();

        finalRoot = baseTrustRoot;
        for (uint256 i = 0; i < length; ++i) {
            finalRoot = computeRoot(paperLeaf(finalRoot, blockHashes[i]), witnesses[i]);
        }
        if (finalRoot != _homeTrustRoot()) revert FinalRootMismatch();
    }

    function _homeTrustRoot() internal view virtual returns (bytes32);
}
