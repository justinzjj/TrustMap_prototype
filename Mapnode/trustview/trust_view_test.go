package trustview

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestPaperFacingTrustViewTypesAndDirectedEdges(t *testing.T) {
	chainC, _ := domain.NewChainID(103)
	chainB, _ := domain.NewChainID(102)
	height, _ := domain.NewBlockHeight(10)
	c := NewTrustNode(NodeKey{ChainID: chainC, Height: height, BlockHash: common.HexToHash("0xc")}, TrustRoot{Hash: common.HexToHash("0xcc")}, evidence.ID{1})
	b := NewTrustNode(NodeKey{ChainID: chainB, Height: height, BlockHash: common.HexToHash("0xb")}, TrustRoot{Hash: common.HexToHash("0xbb")}, evidence.ID{2})
	edge, err := NewTrustEdge(c.ID, b.ID, evidence.ID{3}, nil, 25)
	if err != nil {
		t.Fatal(err)
	}
	view, err := NewTrustView(7, []TrustNode{c, b}, []TrustEdge{edge})
	if err != nil {
		t.Fatal(err)
	}
	if view.Edges[0].From != c.ID || view.Edges[0].To != b.ID {
		t.Fatal("TrustEdge lost recording/home -> verified source direction")
	}

	conflict := b
	conflict.Root = TrustRoot{Hash: common.HexToHash("0xdead")}
	if _, err := NewTrustView(8, []TrustNode{b, conflict}, nil); !errors.Is(err, ErrTrustRootConflict) {
		t.Fatalf("conflicting TrustRoot error = %v", err)
	}
	sameChainKey := b.Key
	sameChainKey.BlockHash = common.HexToHash("0xb2")
	sameChainNode := NewTrustNode(sameChainKey, TrustRoot{Hash: common.HexToHash("0xb22")}, evidence.ID{9})
	sameChainEdge, _ := NewTrustEdge(b.ID, sameChainNode.ID, evidence.ID{10}, nil, 25)
	if _, err := NewTrustView(9, []TrustNode{b, sameChainNode}, []TrustEdge{sameChainEdge}); !errors.Is(err, ErrInvalidTrustEdge) {
		t.Fatalf("same-chain header relation entered TrustView: %v", err)
	}
}
