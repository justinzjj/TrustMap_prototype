package executor

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
)

type HomeRootRPC interface {
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	CallContractAtHash(context.Context, ethereum.CallMsg, common.Hash) ([]byte, error)
}

type CanonicalHomeRootGuard struct {
	rpc     HomeRootRPC
	gateway common.Address
}

func NewHomeRootGuard(rpc HomeRootRPC, gateway common.Address) (*CanonicalHomeRootGuard, error) {
	if rpc == nil || gateway == (common.Address{}) {
		return nil, errors.New("home root guard requires RPC and Gateway")
	}
	return &CanonicalHomeRootGuard{rpc: rpc, gateway: gateway}, nil
}

// Validate reads both values against one EIP-1898 block hash and then rechecks
// that the height still names the same canonical block.
func (guard *CanonicalHomeRootGuard) Validate(ctx context.Context, expected trustview.TrustRoot) error {
	header, err := guard.rpc.HeaderByNumber(ctx, nil)
	if err != nil || header == nil || header.Number == nil || header.Number.Sign() < 0 {
		return errors.New("read canonical home head")
	}
	hash := header.Hash()
	rootResult, err := guard.rpc.CallContractAtHash(ctx, ethereum.CallMsg{To: &guard.gateway, Data: chainabi.EncodeCurrentTrustRootCall()}, hash)
	if err != nil {
		return err
	}
	root, err := chainabi.DecodeHashResult(rootResult)
	if err != nil {
		return err
	}
	updatedResult, err := guard.rpc.CallContractAtHash(ctx, ethereum.CallMsg{To: &guard.gateway, Data: chainabi.EncodeHasTrustRootUpdateAtBlockCall(header.Number)}, hash)
	if err != nil {
		return err
	}
	updated, err := chainabi.DecodeBoolResult(updatedResult)
	if err != nil {
		return err
	}
	recheck, err := guard.rpc.HeaderByNumber(ctx, header.Number)
	if err != nil || recheck == nil || recheck.Hash() != hash {
		return errors.New("home block changed during TrustRoot guard")
	}
	if updated {
		return ErrHomeBlockBoundary
	}
	if root != expected.Hash {
		return ErrStaleHomeTrustRoot
	}
	return nil
}
