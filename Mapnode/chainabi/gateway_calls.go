package chainabi

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
)

var currentTrustRootSelector = [4]byte{0x78, 0x9a, 0xea, 0x64}

func EncodeCurrentTrustRootCall() []byte {
	return append([]byte(nil), currentTrustRootSelector[:]...)
}

func DecodeCurrentTrustRootResult(encoded []byte) (common.Hash, error) {
	if len(encoded) != common.HashLength {
		return common.Hash{}, errors.New("currentTrustRoot result must be one ABI word")
	}
	return common.BytesToHash(encoded), nil
}
