package chain

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type TrustRootReader struct {
	blocks  *CanonicalBlockReader
	gateway *GatewayClient
}

func NewTrustRootReader(blocks *CanonicalBlockReader, gateway *GatewayClient) *TrustRootReader {
	return &TrustRootReader{blocks: blocks, gateway: gateway}
}

func (reader *TrustRootReader) Observe(ctx context.Context, height domain.BlockHeight, blockHash common.Hash, confirmations uint64) (trustview.TrustRootObservation, error) {
	if reader == nil || reader.blocks == nil || reader.gateway == nil {
		return trustview.TrustRootObservation{}, errors.New("TrustRootReader requires canonical block and Gateway clients")
	}
	block, err := reader.blocks.Confirmed(ctx, height, blockHash, confirmations)
	if err != nil {
		return trustview.TrustRootObservation{}, err
	}
	root, err := reader.gateway.CurrentTrustRoot(ctx, block)
	if err != nil {
		return trustview.TrustRootObservation{}, err
	}
	// Recheck canonicality after the hash-bound code/call sequence. Hash tags
	// prevent reading replacement-block state; this second header check prevents
	// persisting a block that ceased to be canonical during the RPC round trip.
	block, err = reader.blocks.Confirmed(ctx, height, blockHash, confirmations)
	if err != nil {
		return trustview.TrustRootObservation{}, err
	}
	deployment := reader.gateway.Deployment()
	observation, err := trustview.NewTrustRootObservation(trustview.TrustRootObservationContent{
		ChainID: deployment.ChainID, Height: height, BlockHash: blockHash, Gateway: deployment.Address,
		TrustRoot: root, GatewayCodeHash: deployment.CodeHash, RequiredConfirmations: confirmations,
		ConfirmedHeadHeight: block.ConfirmedHeadHeight, ConfirmedHeadHash: block.ConfirmedHeadHash,
	})
	if err != nil {
		return trustview.TrustRootObservation{}, err
	}
	observation.ObservedAt = time.Now().UTC()
	return observation, nil
}
