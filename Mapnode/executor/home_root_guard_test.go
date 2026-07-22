package executor

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
)

type homeRootRPCFake struct {
	latest  *types.Header
	recheck *types.Header
	root    common.Hash
	updated bool
	calls   int
}

func (rpc *homeRootRPCFake) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	rpc.calls++
	if rpc.calls == 1 {
		return rpc.latest, nil
	}
	return rpc.recheck, nil
}
func (rpc *homeRootRPCFake) CallContractAtHash(_ context.Context, message ethereum.CallMsg, hash common.Hash) ([]byte, error) {
	if hash != rpc.latest.Hash() {
		return nil, errors.New("mixed block hash")
	}
	if string(message.Data) == string(chainabi.EncodeCurrentTrustRootCall()) {
		return rpc.root[:], nil
	}
	if string(message.Data) == string(chainabi.EncodeHasTrustRootUpdateAtBlockCall(rpc.latest.Number)) {
		word := make([]byte, 32)
		if rpc.updated {
			word[31] = 1
		}
		return word, nil
	}
	return nil, errors.New("unexpected call")
}

func TestHomeRootGuardUsesOneCanonicalBlockHashAndRejectsUpdateBoundary(t *testing.T) {
	gateway := common.HexToAddress("0x1111111111111111111111111111111111111111")
	header := &types.Header{Number: big.NewInt(7), Extra: []byte("same")}
	root := common.HexToHash("0x1234")
	rpc := &homeRootRPCFake{latest: header, recheck: header, root: root, updated: true}
	guard, err := NewHomeRootGuard(rpc, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Validate(context.Background(), trustview.TrustRoot{Hash: root}); !errors.Is(err, ErrHomeBlockBoundary) {
		t.Fatalf("error=%v", err)
	}
	rpc.calls, rpc.updated = 0, false
	if err := guard.Validate(context.Background(), trustview.TrustRoot{Hash: common.HexToHash("0x9999")}); !errors.Is(err, ErrStaleHomeTrustRoot) {
		t.Fatalf("error=%v", err)
	}
}

func TestHomeRootGuardRejectsCanonicalHashChangeAcrossCalls(t *testing.T) {
	header := &types.Header{Number: big.NewInt(7), Extra: []byte("first")}
	rpc := &homeRootRPCFake{latest: header, recheck: &types.Header{Number: big.NewInt(7), Extra: []byte("second")}, root: common.HexToHash("0x1")}
	guard, _ := NewHomeRootGuard(rpc, common.HexToAddress("0x1"))
	if err := guard.Validate(context.Background(), trustview.TrustRoot{Hash: rpc.root}); err == nil {
		t.Fatal("canonical hash change accepted")
	}
}
