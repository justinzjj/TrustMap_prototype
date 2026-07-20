package domain

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto/keccak"
)

func LeafHash(sourceTrustRoot common.Hash, sourceBlockHash common.Hash) common.Hash {
	return keccak256(sourceTrustRoot[:], sourceBlockHash[:])
}

func DependencyKey(dependency Dependency) common.Hash {
	encoded := [128]byte{}
	chainID := dependency.SourceChainID.Bytes32()
	height := dependency.SourceHeight.Bytes32()
	copy(encoded[0:32], chainID[:])
	copy(encoded[32:64], height[:])
	copy(encoded[64:96], dependency.SourceBlockHash[:])
	copy(encoded[96:128], dependency.SourceTrustRoot[:])
	return keccak256(encoded[:])
}

func keccak256(parts ...[]byte) common.Hash {
	hash := keccak.NewLegacyKeccak256()
	for _, part := range parts {
		_, _ = hash.Write(part)
	}
	return common.BytesToHash(hash.Sum(nil))
}
