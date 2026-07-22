package evidence

import (
	"context"
	"encoding/binary"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

type remoteRPCFixture struct {
	chainID       *big.Int
	head          uint64
	header        *types.Header
	requestHeader *types.Header
	code          []byte
	receipt       *types.Receipt
	requests      []types.Log
	err           error
}

func (rpc *remoteRPCFixture) ChainID(context.Context) (*big.Int, error) {
	if rpc.err != nil {
		return nil, rpc.err
	}
	return new(big.Int).Set(rpc.chainID), nil
}
func (rpc *remoteRPCFixture) BlockNumber(context.Context) (uint64, error) {
	if rpc.err != nil {
		return 0, rpc.err
	}
	return rpc.head, nil
}
func (rpc *remoteRPCFixture) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	if rpc.err != nil {
		return nil, rpc.err
	}
	if number == nil || number.Uint64() != rpc.header.Number.Uint64() {
		if rpc.requestHeader == nil || number == nil || number.Uint64() != rpc.requestHeader.Number.Uint64() {
			return nil, nil
		}
		copy := *rpc.requestHeader
		return &copy, nil
	}
	copy := *rpc.header
	return &copy, nil
}
func (rpc *remoteRPCFixture) CodeAtHash(context.Context, common.Address, common.Hash) ([]byte, error) {
	if rpc.err != nil {
		return nil, rpc.err
	}
	return append([]byte(nil), rpc.code...), nil
}
func (rpc *remoteRPCFixture) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	if rpc.err != nil {
		return nil, rpc.err
	}
	return rpc.receipt, nil
}
func (rpc *remoteRPCFixture) FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
	if rpc.err != nil {
		return nil, rpc.err
	}
	return append([]types.Log(nil), rpc.requests...), nil
}

type remoteObserverFixture struct {
	calls int
	wrong bool
	err   error
}

func (observer *remoteObserverFixture) ObserveExpectedTrustRoot(_ context.Context, chainID domain.ChainID, height domain.BlockHeight, blockHash, root common.Hash) (ValidatedTrustRootObservation, error) {
	observer.calls++
	if observer.err != nil {
		return ValidatedTrustRootObservation{}, observer.err
	}
	if observer.wrong {
		root[0] ^= 1
	}
	return ValidatedTrustRootObservation{ChainID: chainID, Height: height, BlockHash: blockHash, TrustRoot: root, EvidenceID: ID{byte(observer.calls)}}, nil
}

func TestRemoteDependencyValidatorAcceptsOnlyCompleteCanonicalReceiptAndObservations(t *testing.T) {
	fixture := newRemoteFixture(t)
	result, err := fixture.validator.Validate(context.Background(), fixture.locator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.State != Candidate || result.Dependency.RequestID == ([32]byte{}) || len(result.WitnessSiblings) != int(fixture.config.MerkleDepth) || result.HomeObservation.TrustRoot != result.HomeTrustRoot || fixture.observer.calls != 2 {
		t.Fatalf("result=%+v calls=%d", result, fixture.observer.calls)
	}
}

func TestRemoteDependencyValidatorRejectsFabricatedValidPeerLocatorsAndAllBindingFailures(t *testing.T) {
	tests := []struct {
		name string
		edit func(*remoteFixture)
	}{
		{"wrong chain", func(f *remoteFixture) { f.rpc.chainID = big.NewInt(999) }},
		{"wrong code", func(f *remoteFixture) { f.rpc.code = []byte{9} }},
		{"wrong block", func(f *remoteFixture) { f.locator.BlockHash[0] ^= 1 }},
		{"failed receipt", func(f *remoteFixture) { f.rpc.receipt.Status = types.ReceiptStatusFailed }},
		{"removed receipt log", func(f *remoteFixture) { f.rpc.receipt.Logs[3].Removed = true }},
		{"wrong receipt transaction index", func(f *remoteFixture) { f.rpc.receipt.TransactionIndex++ }},
		{"missing request", func(f *remoteFixture) { f.rpc.requests = nil }},
		{"wrong exact dependency log", func(f *remoteFixture) { f.locator.LogIndex++ }},
		{"wrong receipt event order", func(f *remoteFixture) {
			f.rpc.receipt.Logs[3], f.rpc.receipt.Logs[4] = f.rpc.receipt.Logs[4], f.rpc.receipt.Logs[3]
			f.rpc.receipt.Logs[3].Index = 3
			f.rpc.receipt.Logs[4].Index = 4
			f.locator.LogIndex = 4
		}},
		{"wrong payload digest", func(f *remoteFixture) { f.locator.PayloadDigest[0] ^= 1 }},
		{"wrong root chain", func(f *remoteFixture) { f.rpc.receipt.Logs[2].Data[32] ^= 1 }},
		{"wrong witness depth", func(f *remoteFixture) { f.config.MerkleDepth++ }},
		{"wrong hash-bound observation", func(f *remoteFixture) { f.observer.wrong = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRemoteFixture(t)
			test.edit(&fixture)
			fixture.validator, _ = NewRemoteDependencyValidator(fixture.config, fixture.rpc, fixture.observer)
			if _, err := fixture.validator.Validate(context.Background(), fixture.locator); !errors.Is(err, ErrRemoteDependencyInvalid) {
				t.Fatalf("Validate() error=%v, want invalid", err)
			}
		})
	}
}

func TestRemoteDependencyValidatorRetriesUntilConfirmationDepth(t *testing.T) {
	fixture := newRemoteFixture(t)
	fixture.rpc.head = fixture.rpc.header.Number.Uint64() + fixture.config.Confirmations - 1
	if _, err := fixture.validator.Validate(context.Background(), fixture.locator); !errors.Is(err, ErrRemoteDependencyRetryable) {
		t.Fatalf("Validate() error=%v, want retryable", err)
	}
}

func TestRemoteDependencyValidatorClassifiesRPCFailuresRetryable(t *testing.T) {
	fixture := newRemoteFixture(t)
	fixture.rpc.err = errors.New("rpc down")
	if _, err := fixture.validator.Validate(context.Background(), fixture.locator); !errors.Is(err, ErrRemoteDependencyRetryable) {
		t.Fatalf("Validate() error=%v", err)
	}
}

type remoteFixture struct {
	config    RemoteDependencyConfig
	rpc       *remoteRPCFixture
	observer  *remoteObserverFixture
	validator *RemoteDependencyValidator
	locator   Locator
}

func newRemoteFixture(t *testing.T) remoteFixture {
	t.Helper()
	home, _ := domain.NewChainID(10002)
	source, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(40)
	requestID := domain.RequestID(common.HexToHash("0x1111"))
	requester := common.HexToAddress("0x1234")
	gateway := common.HexToAddress("0x5678")
	parentHash := common.HexToHash("0xbeef")
	sourceHash := common.HexToHash("0xa001")
	sourceRoot := common.HexToHash("0xa002")
	dependencyKey := remoteDependencyKey(source, height, sourceHash, sourceRoot)
	depth := uint8(8)
	oldRoot := common.Hash{}
	anchorLeaf := domain.LeafHash(oldRoot, parentHash)
	dependencyLeaf := domain.LeafHash(sourceRoot, sourceHash)
	tree, _ := internalproof.NewIncrementalTree(depth)
	_ = tree.Append(anchorLeaf)
	anchorRoot := tree.Root()
	_ = tree.Append(dependencyLeaf)
	finalRoot := tree.Root()
	header := &types.Header{Number: big.NewInt(77), ParentHash: parentHash, Time: 100}
	blockHash := header.Hash()
	txHash := common.HexToHash("0x7001")
	txIndex := uint(3)
	logs := []*types.Log{
		remoteLog(gateway, blockHash, 77, txHash, txIndex, 0, []common.Hash{chainabi.DirectVerificationSucceededTopic, common.Hash(requestID), common.Hash(source)}, remoteWords(height[:], sourceHash[:], sourceRoot[:], dependencyKey[:])),
		remoteLog(gateway, blockHash, 77, txHash, txIndex, 1, []common.Hash{chainabi.TrustRootUpdatedTopic, common.Hash(requestID), remoteUint32Topic(0)}, remoteWords(anchorLeaf[:], oldRoot[:], anchorRoot[:], remoteBoolWord(true))),
		remoteLog(gateway, blockHash, 77, txHash, txIndex, 2, []common.Hash{chainabi.TrustRootUpdatedTopic, common.Hash(requestID), remoteUint32Topic(1)}, remoteWords(dependencyLeaf[:], anchorRoot[:], finalRoot[:], remoteBoolWord(false))),
		remoteLog(gateway, blockHash, 77, txHash, txIndex, 3, []common.Hash{chainabi.DependencyRecordedTopic, dependencyKey, common.Hash(requestID), common.Hash(source)}, remoteWords(height[:], sourceHash[:], sourceRoot[:], remoteUint32Word(1))),
		remoteLog(gateway, blockHash, 77, txHash, txIndex, 4, []common.Hash{chainabi.RequestResolvedTopic, common.Hash(requestID), remoteAddressTopic(requester), dependencyKey}, remoteWords(remoteBoolWord(true), finalRoot[:])),
	}
	receipt := &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: txHash, BlockHash: blockHash, BlockNumber: big.NewInt(77), TransactionIndex: txIndex, Logs: logs}
	nonce := new(big.Int).SetUint64(5)
	computedRequest, _ := ComputeGatewayRequestID(home, gateway, requester, nonce, source, height, sourceHash)
	requestID = computedRequest
	for _, log := range logs {
		switch log.Topics[0] {
		case chainabi.DirectVerificationSucceededTopic, chainabi.TrustRootUpdatedTopic, chainabi.RequestResolvedTopic:
			log.Topics[1] = common.Hash(requestID)
		case chainabi.DependencyRecordedTopic:
			log.Topics[2] = common.Hash(requestID)
		}
	}
	requestHeader := &types.Header{Number: big.NewInt(50), ParentHash: common.HexToHash("0x4444"), Time: 90}
	requestLog := *remoteLog(gateway, requestHeader.Hash(), 50, common.HexToHash("0x6666"), 0, 1, []common.Hash{chainabi.VerificationRequestedTopic, common.Hash(requestID), remoteAddressTopic(requester), common.Hash(source)}, remoteWords(height[:], sourceHash[:], remoteUint256Word(nonce)))
	dependency := chainabi.DependencyRecorded{DependencyKey: dependencyKey, RequestID: requestID, SourceChainID: source, SourceHeight: height, SourceBlockHash: sourceHash, SourceTrustRoot: sourceRoot, LeafIndex: 1}
	payload := remotePayloadDigest(dependency)
	code := []byte{1, 2, 3}
	config := RemoteDependencyConfig{ChainID: home, Gateway: gateway, GatewayCodeHash: crypto.Keccak256Hash(code), DeploymentBlock: 1, Confirmations: 2, MerkleDepth: depth, PathStepCostGas: 30713}
	rpc := &remoteRPCFixture{chainID: home.BigInt(), head: 79, header: header, requestHeader: requestHeader, code: code, receipt: receipt, requests: []types.Log{requestLog}}
	observer := &remoteObserverFixture{}
	validator, err := NewRemoteDependencyValidator(config, rpc, observer)
	if err != nil {
		t.Fatal(err)
	}
	blockHeight, _ := domain.NewBlockHeight(77)
	locator := Locator{ChainID: home, ContractAddress: gateway, BlockNumber: blockHeight, BlockHash: blockHash, TxHash: txHash, TxIndex: uint32(txIndex), LogIndex: 3, PayloadDigest: payload}
	return remoteFixture{config: config, rpc: rpc, observer: observer, validator: validator, locator: locator}
}

func remoteLog(address common.Address, blockHash common.Hash, block uint64, txHash common.Hash, txIndex, logIndex uint, topics []common.Hash, data []byte) *types.Log {
	return &types.Log{Address: address, BlockHash: blockHash, BlockNumber: block, TxHash: txHash, TxIndex: txIndex, Index: logIndex, Topics: topics, Data: data}
}
func remoteWords(values ...[]byte) []byte {
	result := make([]byte, 0, len(values)*32)
	for _, value := range values {
		word := make([]byte, 32)
		copy(word[32-len(value):], value)
		result = append(result, word...)
	}
	return result
}
func remoteUint32Word(value uint32) []byte {
	result := make([]byte, 32)
	binary.BigEndian.PutUint32(result[28:], value)
	return result
}
func remoteUint256Word(value *big.Int) []byte {
	result := make([]byte, 32)
	value.FillBytes(result)
	return result
}
func remoteUint32Topic(value uint32) common.Hash { return common.BytesToHash(remoteUint32Word(value)) }
func remoteBoolWord(value bool) []byte {
	if value {
		return remoteUint32Word(1)
	}
	return remoteUint32Word(0)
}
func remoteAddressTopic(value common.Address) common.Hash { return common.BytesToHash(value[:]) }
func remotePayloadDigest(dependency chainabi.DependencyRecorded) common.Hash {
	encoded := [7 * 32]byte{}
	copy(encoded[0:32], dependency.DependencyKey[:])
	copy(encoded[32:64], dependency.RequestID[:])
	copy(encoded[64:96], dependency.SourceChainID[:])
	copy(encoded[96:128], dependency.SourceHeight[:])
	copy(encoded[128:160], dependency.SourceBlockHash[:])
	copy(encoded[160:192], dependency.SourceTrustRoot[:])
	binary.BigEndian.PutUint32(encoded[220:224], dependency.LeafIndex)
	return crypto.Keccak256Hash(encoded[:])
}
func remoteDependencyKey(chainID domain.ChainID, height domain.BlockHeight, blockHash, root common.Hash) common.Hash {
	encoded := [4 * 32]byte{}
	copy(encoded[0:32], chainID[:])
	copy(encoded[32:64], height[:])
	copy(encoded[64:96], blockHash[:])
	copy(encoded[96:128], root[:])
	return crypto.Keccak256Hash(encoded[:])
}
