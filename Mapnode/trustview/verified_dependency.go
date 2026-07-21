package trustview

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var ErrInvalidVerifiedDependency = errors.New("invalid verified DependencyRecorded event")

// VerifiedDependency is the paper/contract-facing representation of a
// DependencyRecorded event. It binds a TrustEdge to the exact source block and
// TrustRoot committed by the recording-chain event.
type VerifiedDependency struct {
	RequestID       domain.RequestID
	DependencyKey   common.Hash
	SourceChainID   domain.ChainID
	SourceHeight    domain.BlockHeight
	SourceBlockHash common.Hash
	SourceTrustRoot TrustRoot
	LeafIndex       uint32
	EvidenceID      evidence.ID
}

func NewVerifiedDependency(
	requestID domain.RequestID,
	sourceChainID domain.ChainID,
	sourceHeight domain.BlockHeight,
	sourceBlockHash common.Hash,
	sourceTrustRoot TrustRoot,
	leafIndex uint32,
	evidenceID evidence.ID,
) VerifiedDependency {
	dependency := VerifiedDependency{
		RequestID: requestID, SourceChainID: sourceChainID, SourceHeight: sourceHeight,
		SourceBlockHash: sourceBlockHash, SourceTrustRoot: sourceTrustRoot,
		LeafIndex: leafIndex, EvidenceID: evidenceID,
	}
	dependency.DependencyKey = ComputeDependencyKey(sourceChainID, sourceHeight, sourceBlockHash, sourceTrustRoot)
	return dependency
}

// ComputeDependencyKey matches Gateway's
// keccak256(abi.encode(sourceChainId,sourceHeight,sourceBlockHash,sourceTrustRoot)).
func ComputeDependencyKey(chainID domain.ChainID, height domain.BlockHeight, blockHash common.Hash, root TrustRoot) common.Hash {
	encoded := [4 * 32]byte{}
	copy(encoded[0:32], chainID[:])
	copy(encoded[32:64], height[:])
	copy(encoded[64:96], blockHash[:])
	copy(encoded[96:128], root.Hash[:])
	return crypto.Keccak256Hash(encoded[:])
}

// ComputeDependencyRecordedPayloadDigest is the canonical digest committed by
// evidence.Locator.PayloadDigest. Each static ABI value occupies one word.
func ComputeDependencyRecordedPayloadDigest(dependency VerifiedDependency) common.Hash {
	encoded := [7 * 32]byte{}
	copy(encoded[0:32], dependency.DependencyKey[:])
	copy(encoded[32:64], dependency.RequestID[:])
	copy(encoded[64:96], dependency.SourceChainID[:])
	copy(encoded[96:128], dependency.SourceHeight[:])
	copy(encoded[128:160], dependency.SourceBlockHash[:])
	copy(encoded[160:192], dependency.SourceTrustRoot.Hash[:])
	encoded[220] = byte(dependency.LeafIndex >> 24)
	encoded[221] = byte(dependency.LeafIndex >> 16)
	encoded[222] = byte(dependency.LeafIndex >> 8)
	encoded[223] = byte(dependency.LeafIndex)
	return crypto.Keccak256Hash(encoded[:])
}

func (dependency VerifiedDependency) Validate(target TrustNode, payloadDigest common.Hash) error {
	if dependency.RequestID == (domain.RequestID{}) || dependency.EvidenceID == (evidence.ID{}) {
		return ErrInvalidVerifiedDependency
	}
	if err := dependency.SourceChainID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidVerifiedDependency, err)
	}
	wantKey := ComputeDependencyKey(dependency.SourceChainID, dependency.SourceHeight, dependency.SourceBlockHash, dependency.SourceTrustRoot)
	if dependency.DependencyKey != wantKey || dependency.SourceChainID != target.Key.ChainID ||
		dependency.SourceHeight != target.Key.Height || dependency.SourceBlockHash != target.Key.BlockHash ||
		dependency.SourceTrustRoot != target.Root || payloadDigest != ComputeDependencyRecordedPayloadDigest(dependency) {
		return ErrInvalidVerifiedDependency
	}
	return nil
}
