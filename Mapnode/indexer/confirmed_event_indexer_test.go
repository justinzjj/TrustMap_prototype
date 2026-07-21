package indexer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestConfirmedEventIndexerAdvancesEverySafeEmptyBlockInBoundedRanges(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	deploymentHeight, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 10)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 10, headers: headers, code: []byte{1, 2, 3}}
	operations := &fakeOperations{}
	gateway := common.HexToAddress("0x1234")
	indexer, err := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{
		ChainID: chainID, Gateway: gateway, GatewayCodeHash: crypto.Keccak256Hash(rpc.code),
		DeploymentBlock: deploymentHeight, Confirmations: 2, MaxBlockRange: 2, MerkleDepth: 8, PathStepCostGas: 30713,
	}, rpc, operations, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := indexer.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SafeHead != 8 || result.BlocksApplied != 3 || len(operations.applied) != 3 {
		t.Fatalf("result=%+v applied=%d", result, len(operations.applied))
	}
	if operations.markCalls != 1 {
		t.Fatalf("validation marks=%d, want 1", operations.markCalls)
	}
	if operations.marked != operations.cursor || operations.marked.Height.BigInt().Uint64() != result.SafeHead {
		t.Fatalf("validation marked cursor=%+v final=%+v safeHead=%d", operations.marked, operations.cursor, result.SafeHead)
	}
	for index, want := range []uint64{6, 7, 8} {
		if operations.applied[index].Number != want {
			t.Fatalf("applied[%d]=%d want=%d", index, operations.applied[index].Number, want)
		}
	}
	if len(rpc.filters) != 2 || rpc.filters[0][0] != 6 || rpc.filters[0][1] != 7 || rpc.filters[1][0] != 8 || rpc.filters[1][1] != 8 {
		t.Fatalf("filter ranges = %v", rpc.filters)
	}
}

func TestConfirmedEventIndexerDoesNotAdvanceWhenHeadIsBelowConfirmations(t *testing.T) {
	chainID, _ := domain.NewChainID(1)
	height, _ := domain.NewBlockHeight(1)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(1), head: 1, headers: makeHeaderChain(0, 1), code: []byte{1}}
	operations := &fakeOperations{}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(operations.applied) != 0 || operations.initialized || operations.markCalls != 0 {
		t.Fatalf("unsafe deployment initialized/applied: %+v", operations)
	}
}

func TestConfirmedEventIndexerWaitsWithoutDegradingWhenDeploymentIsNotSafe(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 6, headers: makeHeaderChain(0, 6), code: []byte{1}}
	operations := &fakeOperations{}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if operations.initialized || operations.markCalls != 0 || operations.degraded != "" {
		t.Fatalf("unsafe deployment changed validation state: %+v", operations)
	}
}

func TestConfirmedEventIndexerRestartRevalidatesPersistedCursor(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 7)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 7, headers: headers, code: []byte{1}}
	operations := &fakeOperations{cursor: reorg.NewCanonicalCursor(chainID, height, headers[5].Hash()), initialized: true}
	config := ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}

	first, _ := NewConfirmedEventIndexer(config, rpc, operations, nil)
	if _, err := first.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, _ := NewConfirmedEventIndexer(config, rpc, operations, nil)
	rpc.chainErr = errors.New("temporary restart RPC outage")
	if _, err := second.Step(context.Background()); err == nil || errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("restart transient error=%v", err)
	}
	if operations.markCalls != 1 || operations.degraded != "" {
		t.Fatalf("restart transient state=%+v", operations)
	}
	rpc.chainErr = nil
	if _, err := second.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if operations.markCalls != 2 || rpc.chainCalls != 3 || rpc.codeCalls != 2 {
		t.Fatalf("restart validation marks=%d chain calls=%d code calls=%d", operations.markCalls, rpc.chainCalls, rpc.codeCalls)
	}
}

func TestConfirmedEventIndexerInitialWrongCodeFailsClosedBeforeValidation(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 7)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 7, headers: headers, code: []byte{1}}
	operations := &fakeOperations{}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash([]byte{2}), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); !errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("Step error=%v", err)
	}
	if operations.degraded == "" || operations.markCalls != 0 {
		t.Fatalf("wrong code state=%+v", operations)
	}
}

func TestConfirmedEventIndexerInitializationConflictFailsClosed(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 7)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 7, headers: headers, code: []byte{1}}
	operations := &fakeOperations{initializeErr: DeterministicStoreError(errors.New("cursor changed"))}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); !errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("initialization conflict error=%v", err)
	}
	if operations.degraded == "" || operations.markCalls != 0 {
		t.Fatalf("initialization conflict state=%+v", operations)
	}
}

func TestConfirmedEventIndexerWaitsWithoutDegradingWhenPersistedCursorIsNotSafe(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(6)
	headers := makeHeaderChain(0, 7)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 7, headers: headers, code: []byte{1}}
	operations := &fakeOperations{cursor: reorg.NewCanonicalCursor(chainID, height, headers[6].Hash()), initialized: true}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); err != nil {
		t.Fatalf("unsafe persisted cursor error=%v", err)
	}
	if operations.degraded != "" || operations.markCalls != 0 {
		t.Fatalf("unsafe persisted cursor state=%+v", operations)
	}
}

func TestConfirmedEventIndexerPersistedCursorHashMismatchFailsClosed(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 7)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 7, headers: headers, code: []byte{1}}
	operations := &fakeOperations{cursor: reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0xdead")), initialized: true}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); !errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("non-canonical persisted cursor error=%v", err)
	}
	if operations.degraded == "" || operations.markCalls != 0 {
		t.Fatalf("non-canonical persisted cursor state=%+v", operations)
	}
}

func TestConfirmedEventIndexerFailsClosedOnDeterministicApplyError(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 8)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 8, headers: headers, code: []byte{1}}
	operations := &fakeOperations{cursor: reorg.NewCanonicalCursor(chainID, height, headers[5].Hash()), initialized: true, applyErr: DeterministicStoreError(errors.New("content conflict"))}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); !errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("Step error=%v", err)
	}
	if operations.degraded == "" {
		t.Fatal("deterministic apply error did not persist degradation")
	}
	if operations.markCalls != 0 {
		t.Fatalf("failed backlog marked validation %d times", operations.markCalls)
	}
}

func TestConfirmedEventIndexerRetriesDeploymentRPCFailureWithoutDegrading(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 8)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), chainErr: errors.New("temporary RPC outage"), head: 8, headers: headers, code: []byte{1}}
	operations := &fakeOperations{}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); err == nil || errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("Step error=%v", err)
	}
	if operations.degraded != "" {
		t.Fatalf("transient RPC error degraded indexer: %s", operations.degraded)
	}
}

func TestConfirmedEventIndexerFailsClosedOnMalformedHeaderResponse(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 8)
	headers[5] = &types.Header{Number: big.NewInt(4)}
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 8, headers: headers, code: []byte{1}}
	operations := &fakeOperations{}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	if _, err := indexer.Step(context.Background()); !errors.Is(err, ErrDeterministicIndexing) {
		t.Fatalf("Step error=%v", err)
	}
	if operations.degraded == "" {
		t.Fatal("malformed header did not degrade indexer")
	}
}

func TestPrepareBlockRejectsTwoResolutionTransactionsEvenWithoutNewDependency(t *testing.T) {
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(5)
	headers := makeHeaderChain(0, 6)
	rpc := &fakeIndexerRPC{chainID: big.NewInt(10002), head: 8, headers: headers, code: []byte{1}}
	operations := &fakeOperations{cursor: reorg.NewCanonicalCursor(chainID, height, headers[5].Hash()), initialized: true}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: height, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, operations, nil)
	gateway := indexer.config.Gateway
	logs := []types.Log{
		*makeGatewayLog(gateway, headers[6].Hash(), 6, common.HexToHash("0x11"), 0, 0, []common.Hash{chainabi.RequestResolvedTopic, common.HexToHash("0x1"), addressTopic(common.HexToAddress("0x2")), common.HexToHash("0x3")}, words(boolWord(false), common.HexToHash("0x4").Bytes())),
		*makeGatewayLog(gateway, headers[6].Hash(), 6, common.HexToHash("0x12"), 1, 1, []common.Hash{chainabi.RequestResolvedTopic, common.HexToHash("0x5"), addressTopic(common.HexToAddress("0x2")), common.HexToHash("0x6")}, words(boolWord(false), common.HexToHash("0x4").Bytes())),
	}
	if _, err := indexer.prepareBlock(context.Background(), operations.cursor, headers[6], logs); err == nil {
		t.Fatal("two resolution transactions accepted")
	}
}

func TestPrepareBlockClassifiesHashBoundObservationMismatchAsDeterministic(t *testing.T) {
	fixture := newReceiptFixture(t, common.Hash{})
	header := &types.Header{Number: new(big.Int).SetUint64(fixture.input.BlockNumber), ParentHash: fixture.input.ParentHash, Time: 1, GasLimit: 30_000_000}
	blockHash := header.Hash()
	fixture.input.BlockHash, fixture.input.Receipt.BlockHash = blockHash, blockHash
	for _, item := range fixture.input.Receipt.Logs {
		item.BlockHash = blockHash
	}
	rpc := &fakeIndexerRPC{chainID: fixture.input.Request.HomeChainID.BigInt(), head: fixture.input.BlockNumber + 2, headers: map[uint64]*types.Header{fixture.input.BlockNumber: header}, code: []byte{1}, receipts: map[common.Hash]*types.Receipt{fixture.input.Receipt.TxHash: fixture.input.Receipt}}
	operations := &fakeOperations{initialized: true, loadRequest: &fixture.input.Request, observeErr: DeterministicObservationError(errors.New("TrustRoot mismatch"))}
	indexer, _ := NewConfirmedEventIndexer(ConfirmedEventIndexerConfig{ChainID: fixture.input.Request.HomeChainID, Gateway: fixture.input.Gateway, GatewayCodeHash: crypto.Keccak256Hash(rpc.code), DeploymentBlock: fixture.input.Request.SourceHeight, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: fixture.input.MerkleDepth, PathStepCostGas: 30713}, rpc, operations, operations)
	var filtered []types.Log
	for _, item := range fixture.input.Receipt.Logs {
		if supportedTopic(item.Topics[0]) {
			filtered = append(filtered, *item)
		}
	}
	if _, err := indexer.prepareBlock(context.Background(), reorg.CanonicalCursor{}, header, filtered); !errors.Is(err, ErrDeterministicObservation) {
		t.Fatalf("prepare error=%v", err)
	}
}

type fakeIndexerRPC struct {
	chainID    *big.Int
	chainErr   error
	head       uint64
	headers    map[uint64]*types.Header
	code       []byte
	logs       []types.Log
	filters    [][2]uint64
	receipts   map[common.Hash]*types.Receipt
	chainCalls int
	codeCalls  int
}

func (rpc *fakeIndexerRPC) BlockNumber(context.Context) (uint64, error) { return rpc.head, nil }
func (rpc *fakeIndexerRPC) ChainID(context.Context) (*big.Int, error) {
	rpc.chainCalls++
	if rpc.chainErr != nil {
		return nil, rpc.chainErr
	}
	return new(big.Int).Set(rpc.chainID), nil
}
func (rpc *fakeIndexerRPC) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	if number == nil {
		return rpc.headers[rpc.head], nil
	}
	return rpc.headers[number.Uint64()], nil
}
func (rpc *fakeIndexerRPC) CodeAtHash(context.Context, common.Address, common.Hash) ([]byte, error) {
	rpc.codeCalls++
	return append([]byte(nil), rpc.code...), nil
}
func (rpc *fakeIndexerRPC) FilterLogs(_ context.Context, query ethereum.FilterQuery) ([]types.Log, error) {
	from, to := query.FromBlock.Uint64(), query.ToBlock.Uint64()
	rpc.filters = append(rpc.filters, [2]uint64{from, to})
	var result []types.Log
	for _, item := range rpc.logs {
		if item.BlockNumber >= from && item.BlockNumber <= to {
			result = append(result, item)
		}
	}
	return result, nil
}
func (rpc *fakeIndexerRPC) TransactionReceipt(_ context.Context, hash common.Hash) (*types.Receipt, error) {
	return rpc.receipts[hash], nil
}

type fakeOperations struct {
	cursor        reorg.CanonicalCursor
	initialized   bool
	applied       []ConfirmedBlock
	degraded      string
	applyErr      error
	loadRequest   *coordinator.Request
	observeErr    error
	markCalls     int
	marked        reorg.CanonicalCursor
	initializeErr error
}

func (operations *fakeOperations) LoadIndexerCursor(context.Context, domain.ChainID) (reorg.CanonicalCursor, bool, error) {
	return operations.cursor, operations.initialized, nil
}
func (operations *fakeOperations) InitializeIndexerCursor(_ context.Context, cursor reorg.CanonicalCursor) error {
	if operations.initializeErr != nil {
		return operations.initializeErr
	}
	operations.cursor, operations.initialized = cursor, true
	return nil
}
func (operations *fakeOperations) MarkIndexerValidated(_ context.Context, cursor reorg.CanonicalCursor) error {
	operations.markCalls++
	operations.marked = cursor
	return nil
}
func (operations *fakeOperations) LoadIndexerRequest(context.Context, domain.RequestID) (coordinator.Request, bool, error) {
	if operations.loadRequest != nil {
		return *operations.loadRequest, true, nil
	}
	return coordinator.Request{}, false, nil
}
func (operations *fakeOperations) ObserveExpectedTrustRoot(context.Context, domain.ChainID, domain.BlockHeight, common.Hash, common.Hash) (trustview.TrustNode, error) {
	if operations.observeErr != nil {
		return trustview.TrustNode{}, operations.observeErr
	}
	return trustview.TrustNode{}, errors.New("unexpected observation")
}
func (operations *fakeOperations) ApplyIndexerBlock(_ context.Context, block ConfirmedBlock) (bool, error) {
	if operations.applyErr != nil {
		return false, operations.applyErr
	}
	operations.applied = append(operations.applied, block)
	height, _ := domain.NewBlockHeight(block.Number)
	operations.cursor = reorg.CanonicalCursor{ChainID: block.ChainID, Height: height, Hash: block.Hash, State: reorg.Healthy}
	return true, nil
}
func (operations *fakeOperations) DegradeIndexerCursor(_ context.Context, _ domain.ChainID, reason string) error {
	operations.degraded = reason
	return nil
}

func makeHeaderChain(from, to uint64) map[uint64]*types.Header {
	result := make(map[uint64]*types.Header, to-from+1)
	parent := common.Hash{}
	for number := from; number <= to; number++ {
		header := &types.Header{Number: new(big.Int).SetUint64(number), ParentHash: parent, Time: number + 1, GasLimit: 30_000_000}
		result[number] = header
		parent = header.Hash()
	}
	return result
}
