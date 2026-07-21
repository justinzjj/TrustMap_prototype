package indexer

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func VerificationRequested(log types.Log, homeChainID domain.ChainID, gateway common.Address, at time.Time) (coordinator.Request, error) {
	event, err := chainabi.ParseVerificationRequested(log)
	if err != nil {
		return coordinator.Request{}, err
	}
	want, err := evidence.ComputeGatewayRequestID(homeChainID, gateway, event.Requester, new(big.Int).SetBytes(event.RequesterNonce[:]), event.SourceChainID, event.SourceHeight, event.SourceBlockHash)
	if err != nil {
		return coordinator.Request{}, err
	}
	if want != event.RequestID {
		return coordinator.Request{}, fmt.Errorf("VerificationRequested request ID mismatch")
	}
	return coordinator.Request{ID: event.RequestID, HomeChainID: homeChainID, Gateway: gateway, Requester: event.Requester, Nonce: coordinator.Uint256(event.RequesterNonce), SourceChainID: event.SourceChainID, SourceHeight: event.SourceHeight, SourceBlockHash: event.SourceBlockHash, State: coordinator.Observed, CreatedAt: at, UpdatedAt: at}, nil
}
