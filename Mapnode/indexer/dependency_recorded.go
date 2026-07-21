package indexer

import (
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

func DependencyRecorded(log types.Log) (chainabi.DependencyRecorded, error) {
	return chainabi.ParseDependencyRecorded(log)
}
