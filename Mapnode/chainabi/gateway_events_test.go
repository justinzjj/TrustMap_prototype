package chainabi_test

import (
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

func TestGatewayEventTopicsMatchSolidityGoldenValues(t *testing.T) {
	tests := []struct {
		name string
		got  common.Hash
		want string
	}{
		{"VerificationRequested", chainabi.VerificationRequestedTopic, "0x6e4486cba7e09fd23bd6d6edc0fc893ff7f02d9266d863047e298f7b132ee71a"},
		{"TrustRootUpdated", chainabi.TrustRootUpdatedTopic, "0xa2f90931b38050126801af291616bf523773603805893f350031b8b3cdf182a2"},
		{"DependencyRecorded", chainabi.DependencyRecordedTopic, "0x26fe4056692e22cb6515b58329b35b50d71b7605c18b7bcaf3bacdf8297245f5"},
		{"RequestResolved", chainabi.RequestResolvedTopic, "0x9e14f05723283ecafaae8f806553da1d3df582379f2502195bc237b3902da290"},
		{"DirectVerificationSucceeded", chainabi.DirectVerificationSucceededTopic, "0xf823b7a644bb08196389f4340810ad3d39e780aa8e99daf540a4622416ab28ba"},
		{"PathVerificationSucceeded", chainabi.PathVerificationSucceededTopic, "0x068d0b8a6ae7f5a6b9ec3c158eb348081e3e4ce74b6d9d5294584f3bf6ec103f"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != common.HexToHash(test.want) {
				t.Fatalf("topic = %s, want %s", test.got, test.want)
			}
		})
	}
}

func TestParseDependencyRecordedUsesExactIndexedAndDataLayout(t *testing.T) {
	requestID := common.HexToHash("0x11")
	dependencyKey := common.HexToHash("0x12")
	chainWord := common.HexToHash("0x2711")
	blockHash := common.HexToHash("0x13")
	trustRoot := common.HexToHash("0x14")
	data := make([]byte, 4*32)
	data[31] = 40
	copy(data[32:64], blockHash[:])
	copy(data[64:96], trustRoot[:])
	binary.BigEndian.PutUint32(data[124:128], 7)
	log := types.Log{Topics: []common.Hash{chainabi.DependencyRecordedTopic, dependencyKey, requestID, chainWord}, Data: data}
	event, err := chainabi.ParseDependencyRecorded(log)
	if err != nil || common.Hash(event.RequestID) != requestID || event.DependencyKey != dependencyKey || event.SourceHeight.BigInt().Uint64() != 40 || event.LeafIndex != 7 {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	log.Topics[0] = chainabi.TrustRootUpdatedTopic
	if _, err := chainabi.ParseDependencyRecorded(log); err == nil {
		t.Fatal("wrong event topic accepted")
	}
}
