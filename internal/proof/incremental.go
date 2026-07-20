package proof

import "github.com/ethereum/go-ethereum/common"

// IncrementalTree is the pure fixed-depth frontier algorithm used by the
// Solidity lazy-update tree. It does not model blocks, anchors, or state.
type IncrementalTree struct {
	depth     uint8
	capacity  uint64
	nextIndex uint64
	frontier  []common.Hash
	zeros     []common.Hash
	root      common.Hash
}

func NewIncrementalTree(depth uint8) (*IncrementalTree, error) {
	zeros, err := ZeroHashes(depth)
	if err != nil {
		return nil, err
	}
	return &IncrementalTree{
		depth:    depth,
		capacity: uint64(1) << depth,
		frontier: make([]common.Hash, depth),
		zeros:    zeros,
		root:     zeros[depth],
	}, nil
}

func (tree *IncrementalTree) Append(leaf common.Hash) error {
	if tree.nextIndex >= tree.capacity {
		return ErrTreeFull
	}

	index := tree.nextIndex
	node := leaf
	for level := uint8(0); level < tree.depth; level++ {
		if index&1 == 0 {
			tree.frontier[level] = node
			node = HashPair(node, tree.zeros[level])
		} else {
			node = HashPair(tree.frontier[level], node)
		}
		index >>= 1
	}

	tree.root = node
	tree.nextIndex++
	return nil
}

func (tree *IncrementalTree) Root() common.Hash {
	return tree.root
}

func (tree *IncrementalTree) NextIndex() uint64 {
	return tree.nextIndex
}

func (tree *IncrementalTree) Capacity() uint64 {
	return tree.capacity
}
