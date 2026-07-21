package indexer

import (
	"encoding/binary"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

func TestVerifyVerificationReceiptReconstructsExactMembershipWitness(t *testing.T) {
	fixture := newReceiptFixture(t, common.Hash{})
	bundle, err := VerifyVerificationReceipt(fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Dependency == nil || bundle.Witness == nil || bundle.Dependency.LeafIndex != 1 || bundle.Witness.LeafIndex != 1 {
		t.Fatalf("bundle = %+v", bundle)
	}
	leaf := domain.LeafHash(bundle.Dependency.SourceTrustRoot, bundle.Dependency.SourceBlockHash)
	root, err := internalproof.RootFromWitness(leaf, bundle.Witness.LeafIndex, bundle.Witness.Siblings, fixture.input.MerkleDepth)
	if err != nil || root != bundle.Resolution.HomeTrustRoot {
		t.Fatalf("root=%s want=%s err=%v", root, bundle.Resolution.HomeTrustRoot, err)
	}
}

func TestVerifyVerificationReceiptAcceptsJSONRPCDecodedReceiptFixture(t *testing.T) {
	fixture := newReceiptFixture(t, common.Hash{})
	encoded, err := json.Marshal(fixture.input.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	var decoded types.Receipt
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	fixture.input.Receipt = &decoded
	if _, err := VerifyVerificationReceipt(fixture.input); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyVerificationReceiptFailsClosedOnInvalidBundle(t *testing.T) {
	tests := []struct {
		name string
		edit func(*receiptFixture)
	}{
		{"failed receipt", func(f *receiptFixture) { f.input.Receipt.Status = types.ReceiptStatusFailed }},
		{"wrong anchor leaf", func(f *receiptFixture) { f.input.Receipt.Logs[1].Data[0] ^= 1 }},
		{"wrong anchor index", func(f *receiptFixture) { f.input.Receipt.Logs[1].Topics[2][31] = 1 }},
		{"dependency before anchor", func(f *receiptFixture) {
			f.input.Receipt.Logs[1], f.input.Receipt.Logs[2] = f.input.Receipt.Logs[2], f.input.Receipt.Logs[1]
		}},
		{"broken root chain", func(f *receiptFixture) { f.input.Receipt.Logs[2].Data[32] ^= 1 }},
		{"wrong dependency leaf", func(f *receiptFixture) { f.input.Receipt.Logs[2].Data[0] ^= 1 }},
		{"wrong final root", func(f *receiptFixture) { f.input.Receipt.Logs[4].Data[63] ^= 1 }},
		{"incomplete", func(f *receiptFixture) {
			f.input.Receipt.Logs = append(f.input.Receipt.Logs[:2], f.input.Receipt.Logs[3:]...)
		}},
		{"wrong depth", func(f *receiptFixture) { f.input.MerkleDepth++ }},
		{"wrong receipt block", func(f *receiptFixture) { f.input.Receipt.BlockHash = common.HexToHash("0xdead") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReceiptFixture(t, common.Hash{})
			test.edit(&fixture)
			if _, err := VerifyVerificationReceipt(fixture.input); err == nil {
				t.Fatal("invalid verification receipt accepted")
			}
		})
	}
}

func TestVerifyVerificationReceiptAllowsResolutionWithoutNewDependency(t *testing.T) {
	fixture := newReceiptFixture(t, common.HexToHash("0x99"))
	resolved := fixture.input.Receipt.Logs[4]
	resolved.Index = 1
	resolved.Data = words(boolWord(false), fixture.ParentTrustRoot[:])
	fixture.input.Receipt.Logs = []*types.Log{fixture.input.Receipt.Logs[0], resolved}
	bundle, err := VerifyVerificationReceipt(fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Dependency != nil || bundle.Witness != nil || bundle.Resolution.NewDependency {
		t.Fatalf("resolution-only bundle = %+v", bundle)
	}
}

type receiptFixture struct {
	input           VerificationReceiptInput
	ParentTrustRoot common.Hash
}

func newReceiptFixture(t *testing.T, oldRoot common.Hash) receiptFixture {
	t.Helper()
	home, _ := domain.NewChainID(10002)
	source, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(40)
	requestID := domain.RequestID(common.HexToHash("0x1111"))
	requester := common.HexToAddress("0x1234")
	gateway := common.HexToAddress("0x5678")
	blockHash := common.HexToHash("0xb10c")
	parentHash := common.HexToHash("0xbeef")
	sourceHash := common.HexToHash("0xa001")
	sourceRoot := common.HexToHash("0xa002")
	dependencyKey := common.HexToHash("0x0")
	dependencyKey = computeDependencyKey(source, height, sourceHash, sourceRoot)
	request := coordinator.Request{ID: requestID, HomeChainID: home, Gateway: gateway, Requester: requester, SourceChainID: source, SourceHeight: height, SourceBlockHash: sourceHash, State: coordinator.Observed}
	depth := uint8(8)
	anchorLeaf := domain.LeafHash(oldRoot, parentHash)
	dependencyLeaf := domain.LeafHash(sourceRoot, sourceHash)
	tree, _ := internalproof.NewIncrementalTree(depth)
	_ = tree.Append(anchorLeaf)
	anchorRoot := tree.Root()
	_ = tree.Append(dependencyLeaf)
	finalRoot := tree.Root()
	txHash := common.HexToHash("0x7001")
	logs := []*types.Log{
		makeGatewayLog(gateway, blockHash, 77, txHash, 3, 0, []common.Hash{chainabi.DirectVerificationSucceededTopic, common.Hash(requestID), common.Hash(source)}, words(height[:], sourceHash[:], sourceRoot[:], dependencyKey[:])),
		makeGatewayLog(gateway, blockHash, 77, txHash, 3, 1, []common.Hash{chainabi.TrustRootUpdatedTopic, common.Hash(requestID), uint32Topic(0)}, words(anchorLeaf[:], oldRoot[:], anchorRoot[:], boolWord(true))),
		makeGatewayLog(gateway, blockHash, 77, txHash, 3, 2, []common.Hash{chainabi.TrustRootUpdatedTopic, common.Hash(requestID), uint32Topic(1)}, words(dependencyLeaf[:], anchorRoot[:], finalRoot[:], boolWord(false))),
		makeGatewayLog(gateway, blockHash, 77, txHash, 3, 3, []common.Hash{chainabi.DependencyRecordedTopic, dependencyKey, common.Hash(requestID), common.Hash(source)}, words(height[:], sourceHash[:], sourceRoot[:], uint32Word(1))),
		makeGatewayLog(gateway, blockHash, 77, txHash, 3, 4, []common.Hash{chainabi.RequestResolvedTopic, common.Hash(requestID), addressTopic(requester), dependencyKey}, words(boolWord(true), finalRoot[:])),
	}
	receipt := &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: txHash, BlockHash: blockHash, BlockNumber: new(big.Int).SetUint64(77), TransactionIndex: 3, Logs: logs}
	return receiptFixture{input: VerificationReceiptInput{Gateway: gateway, BlockHash: blockHash, BlockNumber: 77, ParentHash: parentHash, MerkleDepth: depth, Request: request, Receipt: receipt}, ParentTrustRoot: oldRoot}
}

func makeGatewayLog(address common.Address, blockHash common.Hash, block uint64, txHash common.Hash, txIndex, logIndex uint, topics []common.Hash, data []byte) *types.Log {
	return &types.Log{Address: address, Topics: topics, Data: data, BlockNumber: block, TxHash: txHash, TxIndex: txIndex, BlockHash: blockHash, Index: logIndex}
}

func words(values ...[]byte) []byte {
	result := make([]byte, 0, len(values)*32)
	for _, value := range values {
		word := make([]byte, 32)
		copy(word[32-len(value):], value)
		result = append(result, word...)
	}
	return result
}

func uint32Word(value uint32) []byte {
	result := make([]byte, 32)
	binary.BigEndian.PutUint32(result[28:], value)
	return result
}
func uint32Topic(value uint32) common.Hash { return common.BytesToHash(uint32Word(value)) }
func boolWord(value bool) []byte {
	if value {
		return uint32Word(1)
	}
	return uint32Word(0)
}
func addressTopic(value common.Address) common.Hash { return common.BytesToHash(value[:]) }
