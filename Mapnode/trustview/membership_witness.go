package trustview

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
)

// MembershipWitness is the proof material that makes a TrustEdge a valid
// TrustRoot membership step. It is immutable once a snapshot references it.
type MembershipWitness struct {
	ID         WitnessID
	EvidenceID evidence.ID
	LeafIndex  uint32
	Siblings   []common.Hash
}

func NewMembershipWitness(evidenceID evidence.ID, leafIndex uint32, siblings []common.Hash) MembershipWitness {
	encoded := make([]byte, 0, 36+32*len(siblings))
	encoded = append(encoded, evidenceID[:]...)
	index := [4]byte{}
	binary.BigEndian.PutUint32(index[:], leafIndex)
	encoded = append(encoded, index[:]...)
	for _, sibling := range siblings {
		encoded = append(encoded, sibling[:]...)
	}
	return MembershipWitness{
		ID: WitnessID(crypto.Keccak256Hash(encoded)), EvidenceID: evidenceID,
		LeafIndex: leafIndex, Siblings: append([]common.Hash(nil), siblings...),
	}
}

func (witness MembershipWitness) Clone() MembershipWitness {
	copy := witness
	copy.Siblings = append([]common.Hash(nil), witness.Siblings...)
	return copy
}
