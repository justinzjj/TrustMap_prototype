package proof

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

// HashPair matches Solidity keccak256(abi.encodePacked(left, right)).
func HashPair(left common.Hash, right common.Hash) common.Hash {
	return crypto.Keccak256Hash(left[:], right[:])
}

// ZeroHashes returns the canonical empty subtree hash for levels 0 through depth.
// Level zero is an empty bytes32 leaf and level depth is the empty tree root.
func ZeroHashes(depth uint8) ([]common.Hash, error) {
	if err := validateDepth(depth); err != nil {
		return nil, err
	}

	zeros := make([]common.Hash, int(depth)+1)
	for level := 1; level <= int(depth); level++ {
		zeros[level] = HashPair(zeros[level-1], zeros[level-1])
	}
	return zeros, nil
}

// RootFromWitness reconstructs a fixed-depth Merkle root from a leaf and its
// bottom-up siblings. Each low-to-high leaf-index bit selects the pair order.
func RootFromWitness(
	leaf common.Hash,
	leafIndex uint32,
	siblings []common.Hash,
	expectedDepth uint8,
) (common.Hash, error) {
	if err := validateDepth(expectedDepth); err != nil {
		return common.Hash{}, err
	}
	if len(siblings) != int(expectedDepth) {
		return common.Hash{}, fmt.Errorf(
			"%w: got %d, want %d",
			ErrInvalidSiblingCount,
			len(siblings),
			expectedDepth,
		)
	}
	capacity := uint64(1) << expectedDepth
	if uint64(leafIndex) >= capacity {
		return common.Hash{}, fmt.Errorf(
			"%w: index %d, capacity %d",
			ErrLeafIndexOutOfRange,
			leafIndex,
			capacity,
		)
	}

	root := leaf
	index := leafIndex
	for _, sibling := range siblings {
		if index&1 == 0 {
			root = HashPair(root, sibling)
		} else {
			root = HashPair(sibling, root)
		}
		index >>= 1
	}
	return root, nil
}

// VerifyPath mirrors PathProofVerifier: every reconstructed root becomes the
// source root bound into the next hop's leaf.
func VerifyPath(
	baseTrustRoot common.Hash,
	blockHashes []common.Hash,
	witnesses []domain.MembershipWitness,
	expectedHomeRoot common.Hash,
	expectedDepth uint8,
) error {
	if err := validateDepth(expectedDepth); err != nil {
		return err
	}
	if len(blockHashes) == 0 {
		return ErrEmptyPath
	}
	if len(blockHashes) != len(witnesses) {
		return fmt.Errorf(
			"%w: %d block hashes, %d witnesses",
			ErrPathLengthMismatch,
			len(blockHashes),
			len(witnesses),
		)
	}

	root := baseTrustRoot
	for i, blockHash := range blockHashes {
		leaf := domain.LeafHash(root, blockHash)
		var err error
		root, err = RootFromWitness(
			leaf,
			witnesses[i].LeafIndex(),
			witnesses[i].Siblings(),
			expectedDepth,
		)
		if err != nil {
			return fmt.Errorf("hop %d: %w", i, err)
		}
	}
	if root != expectedHomeRoot {
		return fmt.Errorf(
			"%w: got %s, want %s",
			ErrFinalRootMismatch,
			root,
			expectedHomeRoot,
		)
	}
	return nil
}

func validateDepth(depth uint8) error {
	if depth == 0 || depth > MaxTreeDepth {
		return fmt.Errorf("%w: got %d", ErrInvalidTreeDepth, depth)
	}
	return nil
}
