package trustview

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type SnapshotID [32]byte

// TrustViewSnapshot is an immutable, sealed materialization used by exactly
// one request planning/proof attempt.
type TrustViewSnapshot struct {
	ID            SnapshotID
	Revision      uint64
	HomeChainID   domain.ChainID
	HomeTrustRoot TrustRoot
	StartNodeID   NodeID
	TargetNodeID  NodeID
	Sealed        bool
	Nodes         []TrustNode
	Edges         []TrustEdge
}

func ComputeSnapshotID(requestID domain.RequestID, attempt, revision uint64, start, target NodeID, expectedHomeRoot TrustRoot) SnapshotID {
	encoded := make([]byte, 0, 32*6)
	encoded = append(encoded, requestID[:]...)
	word := [32]byte{}
	binary.BigEndian.PutUint64(word[24:], attempt)
	encoded = append(encoded, word[:]...)
	word = [32]byte{}
	binary.BigEndian.PutUint64(word[24:], revision)
	encoded = append(encoded, word[:]...)
	encoded = append(encoded, start[:]...)
	encoded = append(encoded, target[:]...)
	encoded = append(encoded, expectedHomeRoot.Hash[:]...)
	return SnapshotID(crypto.Keccak256Hash(encoded))
}

func (snapshot TrustViewSnapshot) Clone() TrustViewSnapshot {
	copy := snapshot
	copy.Nodes = append([]TrustNode(nil), snapshot.Nodes...)
	copy.Edges = append([]TrustEdge(nil), snapshot.Edges...)
	for index := range copy.Edges {
		copy.Edges[index].WitnessID = cloneWitnessID(copy.Edges[index].WitnessID)
	}
	return copy
}
