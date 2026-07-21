package evidence

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto/keccak"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrZeroContractAddress = errors.New("contract address must not be zero")
	ErrZeroTransactionHash = errors.New("transaction hash must not be zero")
	ErrZeroPayloadDigest   = errors.New("payload digest must not be zero")
	ErrZeroGateway         = errors.New("gateway address must not be zero")
	ErrZeroRequester       = errors.New("requester address must not be zero")
)

// ID is a domain-specific evidence identifier, distinct from request and graph IDs.
type ID [32]byte

type Locator struct {
	ChainID         domain.ChainID
	ContractAddress common.Address
	BlockNumber     domain.BlockHeight
	BlockHash       common.Hash
	TxHash          common.Hash
	TxIndex         uint32
	LogIndex        uint32
	PayloadDigest   common.Hash
}

func (locator Locator) Validate() error {
	if err := locator.ChainID.Validate(); err != nil {
		return err
	}
	if locator.ContractAddress == (common.Address{}) {
		return ErrZeroContractAddress
	}
	if locator.BlockHash == (common.Hash{}) {
		return domain.ErrZeroBlockHash
	}
	if locator.TxHash == (common.Hash{}) {
		return ErrZeroTransactionHash
	}
	if locator.PayloadDigest == (common.Hash{}) {
		return ErrZeroPayloadDigest
	}
	return nil
}

// ComputeID matches keccak256(abi.encode(uint256,address,uint256,bytes32,
// bytes32,uint32,uint32,bytes32)). Every static ABI value occupies one word.
func ComputeID(locator Locator) (ID, error) {
	if err := locator.Validate(); err != nil {
		return ID{}, err
	}
	encoded := [8 * 32]byte{}
	chainID := locator.ChainID.Bytes32()
	blockNumber := locator.BlockNumber.Bytes32()
	copy(encoded[0:32], chainID[:])
	copy(encoded[32+12:64], locator.ContractAddress[:])
	copy(encoded[64:96], blockNumber[:])
	copy(encoded[96:128], locator.BlockHash[:])
	copy(encoded[128:160], locator.TxHash[:])
	putUint32Word(encoded[160:192], locator.TxIndex)
	putUint32Word(encoded[192:224], locator.LogIndex)
	copy(encoded[224:256], locator.PayloadDigest[:])
	return ID(keccak256(encoded[:])), nil
}

// ComputeSyntheticID is reserved for RPC observations that have no
// transaction/log coordinate. Normal log Evidence must continue through
// ComputeID, which rejects a zero transaction hash.
func ComputeSyntheticID(locator Locator) (ID, error) {
	if err := locator.ChainID.Validate(); err != nil {
		return ID{}, err
	}
	if locator.ContractAddress == (common.Address{}) || locator.BlockHash == (common.Hash{}) || locator.PayloadDigest == (common.Hash{}) {
		return ID{}, errors.New("synthetic evidence requires chain, contract, block hash, and payload digest")
	}
	if locator.TxHash != (common.Hash{}) || locator.TxIndex != 0 || locator.LogIndex != 0 {
		return ID{}, errors.New("synthetic evidence must have zero transaction/log coordinates")
	}
	encoded := [8 * 32]byte{}
	chainID := locator.ChainID.Bytes32()
	blockNumber := locator.BlockNumber.Bytes32()
	copy(encoded[0:32], chainID[:])
	copy(encoded[32+12:64], locator.ContractAddress[:])
	copy(encoded[64:96], blockNumber[:])
	copy(encoded[96:128], locator.BlockHash[:])
	copy(encoded[224:256], locator.PayloadDigest[:])
	return ID(keccak256(encoded[:])), nil
}

// ComputeGatewayRequestID matches TrustMapGateway.computeRequestId exactly.
func ComputeGatewayRequestID(
	homeChainID domain.ChainID,
	gateway common.Address,
	requester common.Address,
	nonce *big.Int,
	sourceChainID domain.ChainID,
	sourceHeight domain.BlockHeight,
	sourceBlockHash common.Hash,
) (domain.RequestID, error) {
	if err := homeChainID.Validate(); err != nil {
		return domain.RequestID{}, err
	}
	if gateway == (common.Address{}) {
		return domain.RequestID{}, ErrZeroGateway
	}
	if requester == (common.Address{}) {
		return domain.RequestID{}, ErrZeroRequester
	}
	nonceWord, err := encodeUint256(nonce)
	if err != nil {
		return domain.RequestID{}, err
	}
	if err := sourceChainID.Validate(); err != nil {
		return domain.RequestID{}, err
	}
	if sourceBlockHash == (common.Hash{}) {
		return domain.RequestID{}, domain.ErrZeroBlockHash
	}

	encoded := [7 * 32]byte{}
	home := homeChainID.Bytes32()
	source := sourceChainID.Bytes32()
	height := sourceHeight.Bytes32()
	copy(encoded[0:32], home[:])
	copy(encoded[32+12:64], gateway[:])
	copy(encoded[64+12:96], requester[:])
	copy(encoded[96:128], nonceWord[:])
	copy(encoded[128:160], source[:])
	copy(encoded[160:192], height[:])
	copy(encoded[192:224], sourceBlockHash[:])
	return domain.RequestID(keccak256(encoded[:])), nil
}

func encodeUint256(value *big.Int) ([32]byte, error) {
	if value == nil {
		return [32]byte{}, domain.ErrNilUint256
	}
	if value.Sign() < 0 {
		return [32]byte{}, domain.ErrNegativeUint256
	}
	if value.BitLen() > 256 {
		return [32]byte{}, domain.ErrUint256Overflow
	}
	encoded := [32]byte{}
	value.FillBytes(encoded[:])
	return encoded, nil
}

func putUint32Word(word []byte, value uint32) {
	word[28] = byte(value >> 24)
	word[29] = byte(value >> 16)
	word[30] = byte(value >> 8)
	word[31] = byte(value)
}

func keccak256(data []byte) common.Hash {
	hash := keccak.NewLegacyKeccak256()
	_, _ = hash.Write(data)
	return common.BytesToHash(hash.Sum(nil))
}
