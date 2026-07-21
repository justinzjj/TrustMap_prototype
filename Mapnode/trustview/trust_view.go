package trustview

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrTrustRootConflict = errors.New("conflicting TrustRoot for NodeKey")
	ErrInvalidTrustNode  = errors.New("invalid TrustView node")
	ErrDanglingTrustEdge = errors.New("TrustEdge endpoint is absent from TrustView")
)

type NodeID [32]byte

type NodeKey struct {
	ChainID   domain.ChainID
	Height    domain.BlockHeight
	BlockHash common.Hash
}

type TrustNode struct {
	ID         NodeID
	Key        NodeKey
	Root       TrustRoot
	EvidenceID evidence.ID
}

func NewTrustNode(key NodeKey, root TrustRoot, evidenceID evidence.ID) TrustNode {
	encoded := make([]byte, 0, 96)
	chainID := key.ChainID.Bytes32()
	height := key.Height.Bytes32()
	encoded = append(encoded, chainID[:]...)
	encoded = append(encoded, height[:]...)
	encoded = append(encoded, key.BlockHash[:]...)
	return TrustNode{ID: NodeID(crypto.Keccak256Hash(encoded)), Key: key, Root: root, EvidenceID: evidenceID}
}

func (node TrustNode) Validate() error {
	if err := node.Key.ChainID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTrustNode, err)
	}
	if node.Key.BlockHash == (common.Hash{}) || node.ID == (NodeID{}) {
		return ErrInvalidTrustNode
	}
	want := NewTrustNode(node.Key, node.Root, node.EvidenceID)
	if want.ID != node.ID {
		return fmt.Errorf("%w: NodeID does not match NodeKey", ErrInvalidTrustNode)
	}
	return nil
}

// TrustView is the active directed graph at one graph revision.
type TrustView struct {
	Revision uint64
	Nodes    []TrustNode
	Edges    []TrustEdge
}

func NewTrustView(revision uint64, nodes []TrustNode, edges []TrustEdge) (TrustView, error) {
	byID := make(map[NodeID]TrustNode, len(nodes))
	byKey := make(map[NodeKey]TrustNode, len(nodes))
	for _, node := range nodes {
		if err := node.Validate(); err != nil {
			return TrustView{}, err
		}
		if previous, exists := byKey[node.Key]; exists && previous.Root != node.Root {
			return TrustView{}, ErrTrustRootConflict
		}
		if previous, exists := byID[node.ID]; exists {
			if previous.Key != node.Key || previous.Root != node.Root || previous.EvidenceID != node.EvidenceID {
				return TrustView{}, ErrTrustRootConflict
			}
			continue
		}
		byID[node.ID], byKey[node.Key] = node, node
	}
	uniqueEdges := make(map[EdgeID]TrustEdge, len(edges))
	for _, edge := range edges {
		if err := edge.Validate(); err != nil {
			return TrustView{}, err
		}
		fromNode, ok := byID[edge.From]
		if !ok {
			return TrustView{}, ErrDanglingTrustEdge
		}
		toNode, ok := byID[edge.To]
		if !ok {
			return TrustView{}, ErrDanglingTrustEdge
		}
		if fromNode.Key.ChainID == toNode.Key.ChainID {
			return TrustView{}, ErrInvalidTrustEdge
		}
		if previous, exists := uniqueEdges[edge.ID]; exists {
			if previous.From != edge.From || previous.To != edge.To || previous.EvidenceID != edge.EvidenceID || previous.LeafIndex != edge.LeafIndex ||
				previous.PathStepCost != edge.PathStepCost || !sameWitness(previous.WitnessID, edge.WitnessID) {
				return TrustView{}, ErrInvalidTrustEdge
			}
			continue
		}
		edge.WitnessID = cloneWitnessID(edge.WitnessID)
		uniqueEdges[edge.ID] = edge
	}
	view := TrustView{Revision: revision}
	for _, node := range byID {
		view.Nodes = append(view.Nodes, node)
	}
	for _, edge := range uniqueEdges {
		view.Edges = append(view.Edges, edge)
	}
	sort.Slice(view.Nodes, func(i, j int) bool { return bytes.Compare(view.Nodes[i].ID[:], view.Nodes[j].ID[:]) < 0 })
	sort.Slice(view.Edges, func(i, j int) bool { return bytes.Compare(view.Edges[i].ID[:], view.Edges[j].ID[:]) < 0 })
	return view, nil
}

func sameWitness(left, right *WitnessID) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
