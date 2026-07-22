package executor

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func BuildPathCall(requestID domain.RequestID, persisted pathproof.PathProof) ([]byte, error) {
	if requestID == (domain.RequestID{}) || persisted.RequestID != requestID || persisted.ID != pathproof.ComputePathProofID(persisted) || len(persisted.BlockHashes) == 0 || len(persisted.BlockHashes) != len(persisted.Witnesses) {
		return nil, errors.New("invalid persisted snapshot-bound PathProof")
	}
	witnesses := make([]chainabi.SolidityWitness, len(persisted.Witnesses))
	for index, witness := range persisted.Witnesses {
		witnesses[index] = chainabi.SolidityWitness{LeafIndex: witness.LeafIndex(), Siblings: witness.Siblings()}
	}
	return chainabi.EncodeVerifyPathAndRecordCall(requestID, chainabi.PathCallProof{
		BaseTrustRoot: persisted.BaseTrustRoot.Hash,
		BlockHashes:   append([]common.Hash(nil), persisted.BlockHashes...),
		Witnesses:     witnesses,
	}), nil
}
