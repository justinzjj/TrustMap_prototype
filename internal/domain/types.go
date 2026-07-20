package domain

import (
	"encoding/binary"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

var (
	ErrZeroChainID     = errors.New("chain ID must not be zero")
	ErrZeroBlockHash   = errors.New("block hash must not be zero")
	ErrEmptyWitness    = errors.New("membership witness must contain siblings")
	ErrNilUint256      = errors.New("uint256 value must not be nil")
	ErrNegativeUint256 = errors.New("uint256 value must not be negative")
	ErrUint256Overflow = errors.New("uint256 value exceeds 256 bits")
)

type ChainID [32]byte

func NewChainID(value uint64) (ChainID, error) {
	chainID := ChainID(uint256FromUint64(value))
	if err := chainID.Validate(); err != nil {
		return ChainID{}, err
	}
	return chainID, nil
}

func NewChainIDFromBig(value *big.Int) (ChainID, error) {
	encoded, err := uint256FromBig(value)
	if err != nil {
		return ChainID{}, err
	}
	chainID := ChainID(encoded)
	if err := chainID.Validate(); err != nil {
		return ChainID{}, err
	}
	return chainID, nil
}

func (id ChainID) Validate() error {
	if id == (ChainID{}) {
		return ErrZeroChainID
	}
	return nil
}

func (id ChainID) BigInt() *big.Int {
	return new(big.Int).SetBytes(id[:])
}

func (id ChainID) Bytes32() [32]byte {
	return [32]byte(id)
}

type BlockHeight [32]byte

func NewBlockHeight(value uint64) (BlockHeight, error) {
	return BlockHeight(uint256FromUint64(value)), nil
}

func NewBlockHeightFromBig(value *big.Int) (BlockHeight, error) {
	encoded, err := uint256FromBig(value)
	if err != nil {
		return BlockHeight{}, err
	}
	return BlockHeight(encoded), nil
}

func (height BlockHeight) BigInt() *big.Int {
	return new(big.Int).SetBytes(height[:])
}

func (height BlockHeight) Bytes32() [32]byte {
	return [32]byte(height)
}

type RequestID common.Hash

type Dependency struct {
	SourceChainID   ChainID
	SourceHeight    BlockHeight
	SourceBlockHash common.Hash
	SourceTrustRoot common.Hash
}

func NewDependency(
	sourceChainID ChainID,
	sourceHeight BlockHeight,
	sourceBlockHash common.Hash,
	sourceTrustRoot common.Hash,
) (Dependency, error) {
	dependency := Dependency{
		SourceChainID:   sourceChainID,
		SourceHeight:    sourceHeight,
		SourceBlockHash: sourceBlockHash,
		SourceTrustRoot: sourceTrustRoot,
	}
	if err := dependency.Validate(); err != nil {
		return Dependency{}, err
	}
	return dependency, nil
}

func (dependency Dependency) Validate() error {
	if err := dependency.SourceChainID.Validate(); err != nil {
		return err
	}
	if dependency.SourceBlockHash == (common.Hash{}) {
		return ErrZeroBlockHash
	}
	return nil
}

type MembershipWitness struct {
	leafIndex uint32
	siblings  []common.Hash
}

func NewMembershipWitness(leafIndex uint32, siblings []common.Hash) (MembershipWitness, error) {
	witness := MembershipWitness{
		leafIndex: leafIndex,
		siblings:  cloneHashes(siblings),
	}
	if err := witness.Validate(); err != nil {
		return MembershipWitness{}, err
	}
	return witness, nil
}

func (witness MembershipWitness) Validate() error {
	if len(witness.siblings) == 0 {
		return ErrEmptyWitness
	}
	return nil
}

func (witness MembershipWitness) LeafIndex() uint32 {
	return witness.leafIndex
}

func (witness MembershipWitness) Siblings() []common.Hash {
	return cloneHashes(witness.siblings)
}

func cloneHashes(hashes []common.Hash) []common.Hash {
	return append([]common.Hash(nil), hashes...)
}

func uint256FromUint64(value uint64) [32]byte {
	encoded := [32]byte{}
	binary.BigEndian.PutUint64(encoded[24:], value)
	return encoded
}

func uint256FromBig(value *big.Int) ([32]byte, error) {
	if value == nil {
		return [32]byte{}, ErrNilUint256
	}
	if value.Sign() < 0 {
		return [32]byte{}, ErrNegativeUint256
	}
	if value.BitLen() > 256 {
		return [32]byte{}, ErrUint256Overflow
	}

	encoded := [32]byte{}
	value.FillBytes(encoded[:])
	return encoded, nil
}
