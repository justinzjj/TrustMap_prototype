package trustview

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestVerifiedDependencyBindsDependencyRecordedPayloadToSourceTrustRoot(t *testing.T) {
	chainID, _ := domain.NewChainID(102)
	height, _ := domain.NewBlockHeight(77)
	to := NewTrustNode(NodeKey{ChainID: chainID, Height: height, BlockHash: common.HexToHash("0xb1")}, TrustRoot{Hash: common.HexToHash("0xbb")}, evidence.ID{1})
	dependency := NewVerifiedDependency(
		domain.RequestID(common.HexToHash("0x1234")), to.Key.ChainID, to.Key.Height,
		to.Key.BlockHash, to.Root, 9, evidence.ID{2},
	)
	payload := ComputeDependencyRecordedPayloadDigest(dependency)
	if err := dependency.Validate(to, payload); err != nil {
		t.Fatalf("valid DependencyRecorded rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*VerifiedDependency, *TrustNode, *common.Hash)
	}{
		{"dependency key", func(value *VerifiedDependency, _ *TrustNode, _ *common.Hash) { value.DependencyKey[0] ^= 1 }},
		{"source chain", func(value *VerifiedDependency, _ *TrustNode, _ *common.Hash) { value.SourceChainID[31] ^= 1 }},
		{"source height", func(value *VerifiedDependency, _ *TrustNode, _ *common.Hash) { value.SourceHeight[31] ^= 1 }},
		{"source block", func(value *VerifiedDependency, _ *TrustNode, _ *common.Hash) { value.SourceBlockHash[0] ^= 1 }},
		{"source TrustRoot", func(value *VerifiedDependency, _ *TrustNode, _ *common.Hash) { value.SourceTrustRoot.Hash[0] ^= 1 }},
		{"target TrustRoot", func(_ *VerifiedDependency, node *TrustNode, _ *common.Hash) { node.Root.Hash[0] ^= 1 }},
		{"payload", func(_ *VerifiedDependency, _ *TrustNode, digest *common.Hash) { digest[0] ^= 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changedDependency, changedNode, changedPayload := dependency, to, payload
			tt.mutate(&changedDependency, &changedNode, &changedPayload)
			if err := changedDependency.Validate(changedNode, changedPayload); !errors.Is(err, ErrInvalidVerifiedDependency) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}
