// Package chainabi contains the small, checked ABI surface used by the live
// executor. It intentionally does not depend on generated Foundry artifacts.
package chainabi

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	bytes32Type      = mustABIType("bytes32", nil)
	bytesType        = mustABIType("bytes", nil)
	bytesArrayType   = mustABIType("bytes[]", nil)
	bytes32ArrayType = mustABIType("bytes32[]", nil)
	uint256Type      = mustABIType("uint256", nil)
	witnessArrayType = mustABIType("tuple[]", []abi.ArgumentMarshaling{
		{Name: "leafIndex", Type: "uint32"},
		{Name: "siblings", Type: "bytes32[]"},
	})
)

var (
	verifyDirectAndRecordSelector     = [4]byte{0xbb, 0x17, 0xa4, 0x40}
	verifyPathAndRecordSelector       = [4]byte{0xf8, 0xa3, 0xc2, 0x5c}
	attestationDigestSelector         = [4]byte{0x32, 0x97, 0x57, 0xef}
	requestResolvedSelector           = [4]byte{0x44, 0x63, 0x2c, 0xf3}
	hasTrustRootUpdateAtBlockSelector = [4]byte{0x11, 0xe9, 0xd3, 0xcd}
	directVerifierSelector            = [4]byte{0xd8, 0x5a, 0xc1, 0x0a}
	verifierGatewaySelector           = [4]byte{0x11, 0x61, 0x91, 0xb6}
	authorizedSignerCountSelector     = [4]byte{0xe3, 0xa0, 0x53, 0x24}
	authorizedSignerSelector          = [4]byte{0xa2, 0xb8, 0x44, 0x60}
	signatureChecksSelector           = [4]byte{0xf4, 0x96, 0x75, 0xb7}
	hashRoundsSelector                = [4]byte{0x1b, 0x3e, 0xe2, 0x37}
)

// SolidityWitness mirrors PathProofVerifier.Witness exactly.
type SolidityWitness struct {
	LeafIndex uint32        `abi:"leafIndex"`
	Siblings  []common.Hash `abi:"siblings"`
}

// PathCallProof is deliberately an ABI-layer value object. The executor maps
// the durable proof.PathProof to it without reversing any slice.
type PathCallProof struct {
	BaseTrustRoot common.Hash
	BlockHashes   []common.Hash
	Witnesses     []SolidityWitness
}

func EncodeDirectProof(sourceTrustRoot common.Hash, signatures [][]byte) ([]byte, error) {
	return abi.Arguments{{Type: bytes32Type}, {Type: bytesArrayType}}.Pack(sourceTrustRoot, cloneByteSlices(signatures))
}

func DecodeDirectProof(encoded []byte) (common.Hash, [][]byte, error) {
	values, err := abi.Arguments{{Type: bytes32Type}, {Type: bytesArrayType}}.Unpack(encoded)
	if err != nil || len(values) != 2 {
		return common.Hash{}, nil, errors.New("decode direct proof")
	}
	root, ok := values[0].([32]byte)
	if !ok {
		return common.Hash{}, nil, errors.New("decode direct proof root")
	}
	signatures, ok := values[1].([][]byte)
	if !ok {
		return common.Hash{}, nil, errors.New("decode direct proof signatures")
	}
	return common.Hash(root), cloneByteSlices(signatures), nil
}

func EncodeVerifyDirectAndRecordCall(requestID domain.RequestID, proof []byte) []byte {
	return encodeCall(verifyDirectAndRecordSelector, abi.Arguments{{Type: bytes32Type}, {Type: bytesType}}, [32]byte(requestID), append([]byte(nil), proof...))
}

func EncodeVerifyPathAndRecordCall(requestID domain.RequestID, proof PathCallProof) []byte {
	witnesses := make([]SolidityWitness, len(proof.Witnesses))
	for index, witness := range proof.Witnesses {
		witnesses[index] = SolidityWitness{LeafIndex: witness.LeafIndex, Siblings: append([]common.Hash(nil), witness.Siblings...)}
	}
	return encodeCall(verifyPathAndRecordSelector, abi.Arguments{{Type: bytes32Type}, {Type: bytes32Type}, {Type: bytes32ArrayType}, {Type: witnessArrayType}}, [32]byte(requestID), proof.BaseTrustRoot, append([]common.Hash(nil), proof.BlockHashes...), witnesses)
}

func EncodeAttestationDigestCall(sourceChainID, sourceHeight *big.Int, sourceBlockHash, sourceTrustRoot common.Hash) []byte {
	return encodeCall(attestationDigestSelector, abi.Arguments{{Type: uint256Type}, {Type: uint256Type}, {Type: bytes32Type}, {Type: bytes32Type}}, nonNilBig(sourceChainID), nonNilBig(sourceHeight), sourceBlockHash, sourceTrustRoot)
}

func EncodeRequestResolvedCall(requestID domain.RequestID) []byte {
	return encodeCall(requestResolvedSelector, abi.Arguments{{Type: bytes32Type}}, [32]byte(requestID))
}

func EncodeHasTrustRootUpdateAtBlockCall(blockNumber *big.Int) []byte {
	return encodeCall(hasTrustRootUpdateAtBlockSelector, abi.Arguments{{Type: uint256Type}}, nonNilBig(blockNumber))
}

func EncodeDirectVerifierCall() []byte        { return selectorOnly(directVerifierSelector) }
func EncodeVerifierGatewayCall() []byte       { return selectorOnly(verifierGatewaySelector) }
func EncodeAuthorizedSignerCountCall() []byte { return selectorOnly(authorizedSignerCountSelector) }
func EncodeSignatureChecksCall() []byte       { return selectorOnly(signatureChecksSelector) }
func EncodeHashRoundsCall() []byte            { return selectorOnly(hashRoundsSelector) }

func EncodeAuthorizedSignerCall(index *big.Int) []byte {
	return encodeCall(authorizedSignerSelector, abi.Arguments{{Type: uint256Type}}, nonNilBig(index))
}

func DecodeHashResult(encoded []byte) (common.Hash, error) {
	if len(encoded) != common.HashLength {
		return common.Hash{}, errors.New("hash result must be one ABI word")
	}
	return common.BytesToHash(encoded), nil
}

func DecodeBoolResult(encoded []byte) (bool, error) {
	if len(encoded) != common.HashLength {
		return false, errors.New("bool result must be one ABI word")
	}
	for _, value := range encoded[:31] {
		if value != 0 {
			return false, errors.New("bool result is not canonical")
		}
	}
	switch encoded[31] {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, errors.New("bool result is not canonical")
	}
}

func DecodeAddressResult(encoded []byte) (common.Address, error) {
	if len(encoded) != common.HashLength {
		return common.Address{}, errors.New("address result must be one ABI word")
	}
	for _, value := range encoded[:12] {
		if value != 0 {
			return common.Address{}, errors.New("address result is not canonical")
		}
	}
	return common.BytesToAddress(encoded[12:]), nil
}

func DecodeUint32Result(encoded []byte) (uint32, error) {
	if len(encoded) != common.HashLength {
		return 0, errors.New("uint32 result must be one ABI word")
	}
	for _, value := range encoded[:28] {
		if value != 0 {
			return 0, errors.New("uint32 result overflows")
		}
	}
	return uint32(encoded[28])<<24 | uint32(encoded[29])<<16 | uint32(encoded[30])<<8 | uint32(encoded[31]), nil
}

func DecodeUint256Result(encoded []byte) (*big.Int, error) {
	if len(encoded) != common.HashLength {
		return nil, errors.New("uint256 result must be one ABI word")
	}
	return new(big.Int).SetBytes(encoded), nil
}

func encodeCall(selector [4]byte, arguments abi.Arguments, values ...any) []byte {
	encoded, err := arguments.Pack(values...)
	if err != nil {
		panic("invalid static TrustMap ABI definition: " + err.Error())
	}
	return append(selectorOnly(selector), encoded...)
}

func selectorOnly(selector [4]byte) []byte { return append([]byte(nil), selector[:]...) }

func mustABIType(name string, components []abi.ArgumentMarshaling) abi.Type {
	value, err := abi.NewType(name, "", components)
	if err != nil {
		panic(err)
	}
	return value
}

func nonNilBig(value *big.Int) *big.Int {
	if value == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(value)
}

func cloneByteSlices(values [][]byte) [][]byte {
	result := make([][]byte, len(values))
	for index := range values {
		result[index] = append([]byte(nil), values[index]...)
	}
	return result
}
