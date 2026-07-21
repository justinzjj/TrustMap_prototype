package chainabi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	VerificationRequestedTopic       = common.HexToHash("0x6e4486cba7e09fd23bd6d6edc0fc893ff7f02d9266d863047e298f7b132ee71a")
	TrustRootUpdatedTopic            = common.HexToHash("0xa2f90931b38050126801af291616bf523773603805893f350031b8b3cdf182a2")
	DependencyRecordedTopic          = common.HexToHash("0x26fe4056692e22cb6515b58329b35b50d71b7605c18b7bcaf3bacdf8297245f5")
	RequestResolvedTopic             = common.HexToHash("0x9e14f05723283ecafaae8f806553da1d3df582379f2502195bc237b3902da290")
	DirectVerificationSucceededTopic = common.HexToHash("0xf823b7a644bb08196389f4340810ad3d39e780aa8e99daf540a4622416ab28ba")
	PathVerificationSucceededTopic   = common.HexToHash("0x068d0b8a6ae7f5a6b9ec3c158eb348081e3e4ce74b6d9d5294584f3bf6ec103f")
)

type VerificationSucceeded struct {
	RequestID       domain.RequestID
	SourceChainID   domain.ChainID
	SourceHeight    domain.BlockHeight
	SourceBlockHash common.Hash
	SourceTrustRoot common.Hash
	DependencyKey   common.Hash
	Path            bool
	HopCount        *big.Int
}

type VerificationRequested struct {
	RequestID       domain.RequestID
	Requester       common.Address
	SourceChainID   domain.ChainID
	SourceHeight    domain.BlockHeight
	SourceBlockHash common.Hash
	RequesterNonce  [32]byte
}

type TrustRootUpdated struct {
	RequestID    domain.RequestID
	LeafIndex    uint32
	Leaf         common.Hash
	OldTrustRoot common.Hash
	NewTrustRoot common.Hash
	IsAnchor     bool
}

type DependencyRecorded struct {
	DependencyKey   common.Hash
	RequestID       domain.RequestID
	SourceChainID   domain.ChainID
	SourceHeight    domain.BlockHeight
	SourceBlockHash common.Hash
	SourceTrustRoot common.Hash
	LeafIndex       uint32
}

type RequestResolved struct {
	RequestID     domain.RequestID
	Requester     common.Address
	DependencyKey common.Hash
	NewDependency bool
	HomeTrustRoot common.Hash
}

func ParseVerificationRequested(log types.Log) (VerificationRequested, error) {
	if err := requireEvent(log, VerificationRequestedTopic, 4, 3); err != nil {
		return VerificationRequested{}, err
	}
	chainID, err := domain.NewChainIDFromBig(new(big.Int).SetBytes(log.Topics[3][:]))
	if err != nil {
		return VerificationRequested{}, err
	}
	requester, err := topicAddress(log.Topics[2])
	if err != nil {
		return VerificationRequested{}, err
	}
	height, _ := domain.NewBlockHeightFromBig(new(big.Int).SetBytes(word(log.Data, 0)))
	var nonce [32]byte
	copy(nonce[:], word(log.Data, 2))
	return VerificationRequested{RequestID: domain.RequestID(log.Topics[1]), Requester: requester, SourceChainID: chainID, SourceHeight: height, SourceBlockHash: common.BytesToHash(word(log.Data, 1)), RequesterNonce: nonce}, nil
}

func ParseTrustRootUpdated(log types.Log) (TrustRootUpdated, error) {
	if err := requireEvent(log, TrustRootUpdatedTopic, 3, 4); err != nil {
		return TrustRootUpdated{}, err
	}
	leafIndex, err := topicUint32(log.Topics[2])
	if err != nil {
		return TrustRootUpdated{}, err
	}
	isAnchor, err := wordBool(word(log.Data, 3))
	if err != nil {
		return TrustRootUpdated{}, err
	}
	return TrustRootUpdated{RequestID: domain.RequestID(log.Topics[1]), LeafIndex: leafIndex, Leaf: common.BytesToHash(word(log.Data, 0)), OldTrustRoot: common.BytesToHash(word(log.Data, 1)), NewTrustRoot: common.BytesToHash(word(log.Data, 2)), IsAnchor: isAnchor}, nil
}

func ParseDependencyRecorded(log types.Log) (DependencyRecorded, error) {
	if err := requireEvent(log, DependencyRecordedTopic, 4, 4); err != nil {
		return DependencyRecorded{}, err
	}
	chainID, err := domain.NewChainIDFromBig(new(big.Int).SetBytes(log.Topics[3][:]))
	if err != nil {
		return DependencyRecorded{}, err
	}
	height, _ := domain.NewBlockHeightFromBig(new(big.Int).SetBytes(word(log.Data, 0)))
	leafIndex, err := wordUint32(word(log.Data, 3))
	if err != nil {
		return DependencyRecorded{}, err
	}
	return DependencyRecorded{DependencyKey: log.Topics[1], RequestID: domain.RequestID(log.Topics[2]), SourceChainID: chainID, SourceHeight: height, SourceBlockHash: common.BytesToHash(word(log.Data, 1)), SourceTrustRoot: common.BytesToHash(word(log.Data, 2)), LeafIndex: leafIndex}, nil
}

func ParseRequestResolved(log types.Log) (RequestResolved, error) {
	if err := requireEvent(log, RequestResolvedTopic, 4, 2); err != nil {
		return RequestResolved{}, err
	}
	requester, err := topicAddress(log.Topics[2])
	if err != nil {
		return RequestResolved{}, err
	}
	newDependency, err := wordBool(word(log.Data, 0))
	if err != nil {
		return RequestResolved{}, err
	}
	return RequestResolved{RequestID: domain.RequestID(log.Topics[1]), Requester: requester, DependencyKey: log.Topics[3], NewDependency: newDependency, HomeTrustRoot: common.BytesToHash(word(log.Data, 1))}, nil
}

func ParseVerificationSucceeded(log types.Log) (VerificationSucceeded, error) {
	words := 4
	path := false
	switch {
	case len(log.Topics) > 0 && log.Topics[0] == DirectVerificationSucceededTopic:
	case len(log.Topics) > 0 && log.Topics[0] == PathVerificationSucceededTopic:
		words, path = 5, true
	default:
		return VerificationSucceeded{}, errors.New("unsupported verification success event")
	}
	if err := requireEvent(log, log.Topics[0], 3, words); err != nil {
		return VerificationSucceeded{}, err
	}
	chainID, err := domain.NewChainIDFromBig(new(big.Int).SetBytes(log.Topics[2][:]))
	if err != nil {
		return VerificationSucceeded{}, err
	}
	height, err := domain.NewBlockHeightFromBig(new(big.Int).SetBytes(word(log.Data, 0)))
	if err != nil {
		return VerificationSucceeded{}, err
	}
	result := VerificationSucceeded{
		RequestID: domain.RequestID(log.Topics[1]), SourceChainID: chainID, SourceHeight: height,
		SourceBlockHash: common.BytesToHash(word(log.Data, 1)), SourceTrustRoot: common.BytesToHash(word(log.Data, 2)),
		DependencyKey: common.BytesToHash(word(log.Data, words-1)), Path: path,
	}
	if path {
		result.HopCount = new(big.Int).SetBytes(word(log.Data, 3))
		if result.HopCount.Sign() == 0 {
			return VerificationSucceeded{}, errors.New("path verification hop count must be positive")
		}
	}
	return result, nil
}

func requireEvent(log types.Log, topic common.Hash, topics, words int) error {
	if len(log.Topics) != topics || len(log.Data) != words*32 || log.Topics[0] != topic {
		return fmt.Errorf("invalid Gateway event layout")
	}
	return nil
}

func word(data []byte, index int) []byte { return data[index*32 : (index+1)*32] }

func wordUint32(value []byte) (uint32, error) {
	for _, item := range value[:28] {
		if item != 0 {
			return 0, errors.New("ABI uint32 overflows")
		}
	}
	return binary.BigEndian.Uint32(value[28:]), nil
}

func topicUint32(value common.Hash) (uint32, error) { return wordUint32(value[:]) }

func wordBool(value []byte) (bool, error) {
	parsed, err := wordUint32(value)
	if err != nil || parsed > 1 {
		return false, errors.New("ABI bool is not canonical")
	}
	return parsed == 1, nil
}

func topicAddress(value common.Hash) (common.Address, error) {
	for _, item := range value[:12] {
		if item != 0 {
			return common.Address{}, errors.New("indexed address is not canonical")
		}
	}
	return common.BytesToAddress(value[12:]), nil
}
