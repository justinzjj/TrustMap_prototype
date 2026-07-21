package indexer

import (
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

func RequestResolved(log types.Log) (chainabi.RequestResolved, error) {
	return chainabi.ParseRequestResolved(log)
}
