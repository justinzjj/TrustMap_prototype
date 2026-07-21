package trustview

import (
	"encoding/binary"
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type TrustRootObservationID [32]byte

type TrustRootObservationContent struct {
	ChainID               domain.ChainID
	Height                domain.BlockHeight
	BlockHash             common.Hash
	Gateway               common.Address
	TrustRoot             TrustRoot
	GatewayCodeHash       common.Hash
	RequiredConfirmations uint64
	ConfirmedHeadHeight   domain.BlockHeight
	ConfirmedHeadHash     common.Hash
}

type TrustRootObservation struct {
	ID TrustRootObservationID
	TrustRootObservationContent
	ObservedAt time.Time
}

func NewTrustRootObservation(content TrustRootObservationContent) (TrustRootObservation, error) {
	observation := TrustRootObservation{TrustRootObservationContent: content}
	if err := observation.Validate(); err != nil {
		return TrustRootObservation{}, err
	}
	observation.ID = observation.ComputeID()
	return observation, nil
}

func (observation TrustRootObservation) Validate() error {
	if err := observation.ChainID.Validate(); err != nil {
		return err
	}
	if observation.BlockHash == (common.Hash{}) || observation.Gateway == (common.Address{}) ||
		observation.GatewayCodeHash == (common.Hash{}) || observation.ConfirmedHeadHash == (common.Hash{}) ||
		observation.RequiredConfirmations == 0 {
		return errors.New("TrustRootObservation requires block, Gateway, code hash, and confirmation evidence")
	}
	required := new(big.Int).Add(observation.Height.BigInt(), new(big.Int).SetUint64(observation.RequiredConfirmations))
	if observation.ConfirmedHeadHeight.BigInt().Cmp(required) < 0 {
		return errors.New("TrustRootObservation block is not sufficiently confirmed")
	}
	return nil
}

func (observation TrustRootObservation) ComputeID() TrustRootObservationID {
	encoded := make([]byte, 0, 32*9)
	chainID, height, head := observation.ChainID.Bytes32(), observation.Height.Bytes32(), observation.ConfirmedHeadHeight.Bytes32()
	encoded = append(encoded, chainID[:]...)
	encoded = append(encoded, height[:]...)
	encoded = append(encoded, observation.BlockHash[:]...)
	addressWord := [32]byte{}
	copy(addressWord[12:], observation.Gateway[:])
	encoded = append(encoded, addressWord[:]...)
	encoded = append(encoded, observation.TrustRoot.Hash[:]...)
	encoded = append(encoded, observation.GatewayCodeHash[:]...)
	confirmations := [32]byte{}
	binary.BigEndian.PutUint64(confirmations[24:], observation.RequiredConfirmations)
	encoded = append(encoded, confirmations[:]...)
	encoded = append(encoded, head[:]...)
	encoded = append(encoded, observation.ConfirmedHeadHash[:]...)
	return TrustRootObservationID(crypto.Keccak256Hash(encoded))
}

func (observation TrustRootObservation) SyntheticEvidence() evidence.Record {
	id := observation.ComputeID()
	payload := crypto.Keccak256Hash(append([]byte("TrustMap/TrustRootObservation/v1\x00"), id[:]...))
	locator := evidence.Locator{ChainID: observation.ChainID, ContractAddress: observation.Gateway, BlockNumber: observation.Height, BlockHash: observation.BlockHash, PayloadDigest: payload}
	evidenceID, _ := evidence.ComputeSyntheticID(locator)
	return evidence.Record{ID: evidenceID, Locator: locator, State: evidence.Candidate, CreatedAt: observation.ObservedAt, UpdatedAt: observation.ObservedAt}
}

func (observation TrustRootObservation) TrustNode() TrustNode {
	return NewTrustNode(NodeKey{ChainID: observation.ChainID, Height: observation.Height, BlockHash: observation.BlockHash}, observation.TrustRoot, observation.SyntheticEvidence().ID)
}
