// Package proof assembles the paper's recursive PathProof from one immutable
// TrustViewSnapshot and verifies it locally before persistence.
package proof

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrPathProofNotFound       = errors.New("PathProof not found")
	ErrPathProofIDMismatch     = errors.New("PathProofID does not match PathProof content")
	ErrProofMaterialMissing    = errors.New("proof_material_missing")
	ErrStaleTrustViewSnapshot  = errors.New("stale_snapshot")
	ErrSourceBlockHashMismatch = errors.New("source request block hash does not match TrustViewSnapshot target")
)

type PathProofID [32]byte

// PathProofHop retains the inverse mapping from Solidity verification order
// back to the Planner's home-to-source TrustEdge order.
type PathProofHop struct {
	PlanHopIndex int
	EdgeID       trustview.EdgeID
	ToNodeID     trustview.NodeID
	BlockHash    common.Hash
	WitnessID    trustview.WitnessID
}

// PathProof is the exact payload consumed by PathProofVerifier. For a planned
// C->B->A path it contains base A.TrustRoot, hashes [A,B] and witnesses
// [wBA,wCB], so recursive verification closes at C's HomeTrustRoot.
type PathProof struct {
	ID            PathProofID
	RequestID     domain.RequestID
	PlanID        planner.PlanID
	SnapshotID    trustview.SnapshotID
	BaseTrustRoot trustview.TrustRoot
	BlockHashes   []common.Hash
	Witnesses     []domain.MembershipWitness
	Hops          []PathProofHop
	CreatedAt     time.Time
}

func (value PathProof) Clone() PathProof {
	copy := value
	copy.BlockHashes = append([]common.Hash(nil), value.BlockHashes...)
	copy.Witnesses = make([]domain.MembershipWitness, len(value.Witnesses))
	for index, witness := range value.Witnesses {
		copy.Witnesses[index], _ = domain.NewMembershipWitness(witness.LeafIndex(), witness.Siblings())
	}
	copy.Hops = append([]PathProofHop(nil), value.Hops...)
	return copy
}

// ComputePathProofID content-addresses every contract-relevant byte and every
// snapshot/plan binding. Creation time is intentionally excluded.
func ComputePathProofID(value PathProof) PathProofID {
	encoded := make([]byte, 0, 256+len(value.Hops)*160)
	encoded = appendLengthPrefixed(encoded, []byte("TrustMap/PathProof/ID/v1"))
	encoded = append(encoded, value.RequestID[:]...)
	encoded = append(encoded, value.PlanID[:]...)
	encoded = append(encoded, value.SnapshotID[:]...)
	encoded = append(encoded, value.BaseTrustRoot.Hash[:]...)
	encoded = appendUint64Word(encoded, uint64(len(value.BlockHashes)))
	for _, blockHash := range value.BlockHashes {
		encoded = append(encoded, blockHash[:]...)
	}
	encoded = appendUint64Word(encoded, uint64(len(value.Witnesses)))
	for _, witness := range value.Witnesses {
		encoded = appendUint64Word(encoded, uint64(witness.LeafIndex()))
		siblings := witness.Siblings()
		encoded = appendUint64Word(encoded, uint64(len(siblings)))
		for _, sibling := range siblings {
			encoded = append(encoded, sibling[:]...)
		}
	}
	encoded = appendUint64Word(encoded, uint64(len(value.Hops)))
	for _, hop := range value.Hops {
		encoded = appendUint64Word(encoded, uint64(hop.PlanHopIndex))
		encoded = append(encoded, hop.EdgeID[:]...)
		encoded = append(encoded, hop.ToNodeID[:]...)
		encoded = append(encoded, hop.BlockHash[:]...)
		encoded = append(encoded, hop.WitnessID[:]...)
	}
	return PathProofID(crypto.Keccak256Hash(encoded))
}

func appendLengthPrefixed(destination, value []byte) []byte {
	length := [4]byte{}
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	destination = append(destination, length[:]...)
	return append(destination, value...)
}

func appendUint64Word(destination []byte, value uint64) []byte {
	word := [32]byte{}
	binary.BigEndian.PutUint64(word[24:], value)
	return append(destination, word[:]...)
}
