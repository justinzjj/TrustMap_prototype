package trustview

import (
	"errors"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
)

var ErrInvalidTrustEdge = errors.New("invalid TrustEdge")

type EdgeID [32]byte
type WitnessID [32]byte

// TrustEdge is directed from the chain that recorded a TrustRoot dependency
// to the source-chain block verified by that dependency. It is not a generic
// parent-header relationship.
type TrustEdge struct {
	ID           EdgeID
	From         NodeID
	To           NodeID
	EvidenceID   evidence.ID
	WitnessID    *WitnessID
	PathStepCost uint64
}

func NewTrustEdge(from, to NodeID, evidenceID evidence.ID, witnessID *WitnessID, pathStepCost uint64) (TrustEdge, error) {
	if from == (NodeID{}) || to == (NodeID{}) || from == to || evidenceID == (evidence.ID{}) || pathStepCost == 0 {
		return TrustEdge{}, ErrInvalidTrustEdge
	}
	encoded := make([]byte, 0, 96)
	encoded = append(encoded, from[:]...)
	encoded = append(encoded, to[:]...)
	encoded = append(encoded, evidenceID[:]...)
	edge := TrustEdge{
		ID: EdgeID(crypto.Keccak256Hash(encoded)), From: from, To: to, EvidenceID: evidenceID,
		WitnessID: cloneWitnessID(witnessID), PathStepCost: pathStepCost,
	}
	return edge, nil
}

func (edge TrustEdge) Validate() error {
	want, err := NewTrustEdge(edge.From, edge.To, edge.EvidenceID, edge.WitnessID, edge.PathStepCost)
	if err != nil || want.ID != edge.ID {
		return ErrInvalidTrustEdge
	}
	return nil
}

func cloneWitnessID(id *WitnessID) *WitnessID {
	if id == nil {
		return nil
	}
	copy := *id
	return &copy
}
