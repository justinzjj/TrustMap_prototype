package chainabi_test

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

func TestCurrentTrustRootCallMatchesSolidityGoldenSelector(t *testing.T) {
	call := chainabi.EncodeCurrentTrustRootCall()
	if got := hex.EncodeToString(call); got != "789aea64" {
		t.Fatalf("currentTrustRoot() call = %s", got)
	}
	want := common.HexToHash("0x010203")
	root, err := chainabi.DecodeCurrentTrustRootResult(want[:])
	if err != nil || root != want {
		t.Fatalf("decode result = %s, %v", root, err)
	}
	if _, err := chainabi.DecodeCurrentTrustRootResult(make([]byte, 31)); err == nil {
		t.Fatal("short currentTrustRoot result accepted")
	}
}
