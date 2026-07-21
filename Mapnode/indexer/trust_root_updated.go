package indexer

import (
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

func TrustRootUpdated(log types.Log) (chainabi.TrustRootUpdated, error) {
	return chainabi.ParseTrustRootUpdated(log)
}
