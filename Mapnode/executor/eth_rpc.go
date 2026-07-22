package executor

import (
	"context"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

// EthRPC adds the one checked Gateway getter needed by recovery while
// preserving ethclient's EIP-1559 and EIP-1898 methods.
type EthRPC struct{ *ethclient.Client }

func (rpc EthRPC) RequestResolvedAtHash(ctx context.Context, gateway common.Address, requestID domain.RequestID, blockHash common.Hash) (bool, error) {
	result, err := rpc.CallContractAtHash(ctx, ethereum.CallMsg{To: &gateway, Data: chainabi.EncodeRequestResolvedCall(requestID)}, blockHash)
	if err != nil {
		return false, err
	}
	return chainabi.DecodeBoolResult(result)
}
