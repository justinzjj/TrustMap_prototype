package app

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/bootstrap"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/indexer"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestOpenBuildsPhaseThreeAppButFailsClosedUntilIndexerValidation(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2711")
	defer closeRPC()
	application, err := Open(context.Background(), config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if application.Ready() || application.Registry == nil || application.ChainCatalog == nil || application.LiveChains == nil || application.canonicalCursors == nil || application.TrustRootObservations == nil || application.TrustView == nil || application.PathProofBuilder == nil {
		t.Fatalf("incomplete Phase 3 composition: %+v", application)
	}
	processor := &countingProcessor{}
	application.coordinator = processor
	if _, err := application.Process(context.Background(), coordinator.Work{}); !errors.Is(err, ErrOperationalUnavailable) {
		t.Fatalf("unvalidated Process error=%v", err)
	}
	if calls := processor.calls.Load(); calls != 0 {
		t.Fatalf("unvalidated Process entered coordinator %d times", calls)
	}
	home, err := application.Registry.HomeChain()
	if err != nil || !home.Home || !home.SignerAuthority || !home.TransactionAuthority {
		t.Fatalf("home authority mapping = %+v, %v", home, err)
	}
	profile, ok := application.Registry.DirectProfile(home.ChainID)
	if !ok || !profile.Calibrated || profile.DirectCost != 3_000_096 {
		t.Fatalf("deployment profile mapping = %+v, %t", profile, ok)
	}
	if application.PathTreeDepth() != manifest.MerkleDepth {
		t.Fatalf("trusted PathTreeDepth = %d, want %d", application.PathTreeDepth(), manifest.MerkleDepth)
	}
	if all := application.ChainCatalog.All(); len(all) != 2 || all[0].Name != "chain-c" || all[1].Name != "chain-d" {
		t.Fatalf("live catalog=%+v", all)
	}
	if _, err := os.Stat(config.Database.Path); err != nil {
		t.Fatalf("SQLite database was not opened/migrated: %v", err)
	}
}

func TestMarkIndexerValidatedEnablesReadinessAndProcess(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2711")
	defer closeRPC()
	ctx := context.Background()
	application, err := Open(ctx, config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(manifest.DeploymentBlock)
	cursor := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x1234"))
	if err := application.InitializeIndexerCursor(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	processor := &countingProcessor{}
	application.coordinator = processor
	if err := application.MarkIndexerValidated(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	if !application.Ready() {
		t.Fatal("validated App did not become ready")
	}
	if _, err := application.Process(ctx, coordinator.Work{}); err != nil {
		t.Fatal(err)
	}
	if calls := processor.calls.Load(); calls != 1 {
		t.Fatalf("validated Process calls=%d", calls)
	}
}

func TestMarkIndexerValidatedRejectsCursorChangedBeforeTransition(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2711")
	defer closeRPC()
	ctx := context.Background()
	application, err := Open(ctx, config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(manifest.DeploymentBlock)
	persisted := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x1234"))
	if err := application.InitializeIndexerCursor(ctx, persisted); err != nil {
		t.Fatal(err)
	}
	expected := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x5678"))
	if err := application.MarkIndexerValidated(ctx, expected); !errors.Is(err, indexer.ErrDeterministicStore) {
		t.Fatalf("changed cursor mark error=%v", err)
	}
	if application.Ready() {
		t.Fatal("cursor mismatch opened operational gate")
	}
}

func TestIndexerApplyRemainsAvailableBeforeProcessValidation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "worker-gate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	blocks := &countingIndexerStore{}
	application := &App{database: database, canonicalCursors: store.NewCanonicalCursorRepository(database), indexerRepository: blocks, coordinator: &countingProcessor{}}
	application.ready.Store(true)
	if _, err := application.ApplyIndexerBlock(context.Background(), indexer.ConfirmedBlock{}); err != nil {
		t.Fatal(err)
	}
	if blocks.calls.Load() != 1 || application.Ready() {
		t.Fatalf("worker calls=%d ready=%t", blocks.calls.Load(), application.Ready())
	}
}

func TestDegradeIndexerCursorLatchesProcessFailureBeforePersistence(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "degrade-latch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	injected := errors.New("degraded row write failed")
	blocks := &countingIndexerStore{degradeErr: injected}
	processor := &countingProcessor{}
	application := &App{database: database, canonicalCursors: store.NewCanonicalCursorRepository(database), indexerRepository: blocks, coordinator: processor}
	application.ready.Store(true)
	application.indexerValidated = true
	chainID, _ := domain.NewChainID(10002)

	if err := application.DegradeIndexerCursor(context.Background(), chainID, "deterministic failure"); !errors.Is(err, injected) {
		t.Fatalf("degrade error=%v", err)
	}
	if application.Ready() {
		t.Fatal("failed degradation persistence left App ready")
	}
	if _, err := application.Process(context.Background(), coordinator.Work{}); !errors.Is(err, ErrOperationalDegraded) {
		t.Fatalf("Process after failed degradation persistence error=%v", err)
	}
	if calls := processor.calls.Load(); calls != 0 {
		t.Fatalf("failed degradation persistence entered coordinator %d times", calls)
	}
}

func TestConfirmedIndexerFailClosedKeepsGateClosedWhenDegradationPersistenceFails(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "fail-closed-latch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	injected := errors.New("degraded row write failed")
	blocks := &countingIndexerStore{degradeErr: injected}
	processor := &countingProcessor{}
	application := &App{database: database, canonicalCursors: store.NewCanonicalCursorRepository(database), indexerRepository: blocks, coordinator: processor}
	application.ready.Store(true)
	application.indexerValidated = true
	chainID, _ := domain.NewChainID(10002)
	deployment, _ := domain.NewBlockHeight(5)
	rpc := &invalidDeploymentRPC{chainID: chainID.BigInt(), head: 7, code: []byte{1}}
	confirmed, err := indexer.NewConfirmedEventIndexer(indexer.ConfirmedEventIndexerConfig{ChainID: chainID, Gateway: common.HexToAddress("0x1"), GatewayCodeHash: crypto.Keccak256Hash([]byte{2}), DeploymentBlock: deployment, Confirmations: 2, MaxBlockRange: 10, MerkleDepth: 8, PathStepCostGas: 30713}, rpc, application, application)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmed.Step(context.Background()); !errors.Is(err, indexer.ErrDeterministicIndexing) || !errors.Is(err, injected) {
		t.Fatalf("failClosed error=%v", err)
	}
	if application.Ready() {
		t.Fatal("joined failClosed error left App ready")
	}
	if _, err := application.Process(context.Background(), coordinator.Work{}); !errors.Is(err, ErrOperationalDegraded) {
		t.Fatalf("Process after joined failClosed error=%v", err)
	}
	if calls := processor.calls.Load(); calls != 0 {
		t.Fatalf("joined failClosed error entered coordinator %d times", calls)
	}
}

func TestAppPersistedDegradedGateDisablesReadyHealthAndProcessAcrossRestart(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2711")
	defer closeRPC()
	ctx := context.Background()
	application, err := Open(ctx, config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	nextHeight, _ := domain.NewBlockHeight(8)
	initial := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))
	if _, _, err := application.InitializeCanonicalCursor(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if _, _, err := application.AdvanceCanonicalCursor(ctx, chainID, initial.Hash, reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0x70")}); !errors.Is(err, reorg.ErrCanonicalMismatch) {
		t.Fatalf("degrade cursor err=%v", err)
	}
	if application.Ready() {
		t.Fatal("running App remained ready after persisted degradation")
	}
	if _, err := application.Process(ctx, coordinator.Work{}); !errors.Is(err, ErrOperationalDegraded) {
		t.Fatalf("running Process err=%v", err)
	}
	recorder := httptest.NewRecorder()
	bootstrap.NewDynamicHealthHandler(application.Ready).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded health status=%d", recorder.Code)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	application, err = Open(ctx, config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if application.Ready() {
		t.Fatal("restarted App ignored persisted degradation")
	}
	recorder = httptest.NewRecorder()
	bootstrap.NewDynamicHealthHandler(application.Ready).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("restarted degraded health status=%d", recorder.Code)
	}
	if _, err := application.Process(ctx, coordinator.Work{}); !errors.Is(err, ErrOperationalDegraded) {
		t.Fatalf("restarted Process err=%v", err)
	}
}

func TestProcessAndCanonicalDegradationAreLinearized(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2711")
	defer closeRPC()
	ctx := context.Background()
	application, err := Open(ctx, config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.indexerValidated = true

	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	nextHeight, _ := domain.NewBlockHeight(8)
	initial := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))
	if _, _, err := application.InitializeCanonicalCursor(ctx, initial); err != nil {
		t.Fatal(err)
	}

	processor := &blockingProcessor{entered: make(chan struct{}), release: make(chan struct{})}
	application.coordinator = processor
	processDone := make(chan error, 1)
	go func() {
		_, err := application.Process(ctx, coordinator.Work{})
		processDone <- err
	}()
	<-processor.entered

	advanceStarted := make(chan struct{})
	advanceDone := make(chan error, 1)
	go func() {
		close(advanceStarted)
		_, _, err := application.AdvanceCanonicalCursor(ctx, chainID, initial.Hash, reorg.CanonicalBlock{
			Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0x70"),
		})
		advanceDone <- err
	}()
	<-advanceStarted
	select {
	case err := <-advanceDone:
		t.Fatalf("degradation completed while Process held the operational read lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(processor.release)
	if err := <-processDone; err != nil {
		t.Fatalf("in-flight Process err=%v", err)
	}
	if err := <-advanceDone; !errors.Is(err, reorg.ErrCanonicalMismatch) {
		t.Fatalf("degrade cursor err=%v", err)
	}
	if _, err := application.Process(ctx, coordinator.Work{}); !errors.Is(err, ErrOperationalDegraded) {
		t.Fatalf("Process after degradation err=%v", err)
	}
	if calls := processor.calls.Load(); calls != 1 {
		t.Fatalf("processor calls=%d, want 1", calls)
	}
}

type blockingProcessor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func TestIndexerApplyHoldsOperationalWriteLockAgainstPlanning(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "lock.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	blocks := &blockingIndexerStore{entered: make(chan struct{}), release: make(chan struct{})}
	processor := &countingProcessor{}
	application := &App{database: database, canonicalCursors: store.NewCanonicalCursorRepository(database), indexerRepository: blocks, coordinator: processor}
	application.ready.Store(true)
	application.indexerValidated = true
	applyDone := make(chan error, 1)
	go func() {
		_, err := application.ApplyIndexerBlock(context.Background(), indexer.ConfirmedBlock{})
		applyDone <- err
	}()
	<-blocks.entered
	processDone := make(chan error, 1)
	go func() { _, err := application.Process(context.Background(), coordinator.Work{}); processDone <- err }()
	select {
	case err := <-processDone:
		t.Fatalf("planning crossed in-flight indexer write lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(blocks.release)
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-processDone; err != nil {
		t.Fatal(err)
	}
	if processor.calls.Load() != 1 {
		t.Fatalf("processor calls=%d", processor.calls.Load())
	}
}

type blockingIndexerStore struct{ entered, release chan struct{} }

func (*blockingIndexerStore) Configure(context.Context, store.IndexerConfig) error { return nil }
func (store *blockingIndexerStore) ApplyConfirmedBlock(context.Context, indexer.ConfirmedBlock) (bool, error) {
	close(store.entered)
	<-store.release
	return true, nil
}
func (*blockingIndexerStore) Degrade(context.Context, domain.ChainID, string) error { return nil }
func (*blockingIndexerStore) HasDegraded(context.Context) (bool, error)             { return false, nil }

type countingIndexerStore struct {
	calls      atomic.Int32
	degradeErr error
}

func (*countingIndexerStore) Configure(context.Context, store.IndexerConfig) error { return nil }
func (store *countingIndexerStore) ApplyConfirmedBlock(context.Context, indexer.ConfirmedBlock) (bool, error) {
	store.calls.Add(1)
	return true, nil
}
func (store *countingIndexerStore) Degrade(context.Context, domain.ChainID, string) error {
	return store.degradeErr
}
func (*countingIndexerStore) HasDegraded(context.Context) (bool, error) { return false, nil }

type invalidDeploymentRPC struct {
	chainID *big.Int
	head    uint64
	code    []byte
}

func (rpc *invalidDeploymentRPC) BlockNumber(context.Context) (uint64, error) { return rpc.head, nil }
func (rpc *invalidDeploymentRPC) ChainID(context.Context) (*big.Int, error) {
	return new(big.Int).Set(rpc.chainID), nil
}
func (*invalidDeploymentRPC) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	return &types.Header{Number: new(big.Int).Set(number), GasLimit: 30_000_000}, nil
}
func (rpc *invalidDeploymentRPC) CodeAtHash(context.Context, common.Address, common.Hash) ([]byte, error) {
	return append([]byte(nil), rpc.code...), nil
}
func (*invalidDeploymentRPC) FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
	return nil, nil
}
func (*invalidDeploymentRPC) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return nil, nil
}

type countingProcessor struct{ calls atomic.Int32 }

func (processor *countingProcessor) Process(context.Context, coordinator.Work) (coordinator.Result, error) {
	processor.calls.Add(1)
	return coordinator.Result{}, nil
}

func (processor *blockingProcessor) Process(ctx context.Context, _ coordinator.Work) (coordinator.Result, error) {
	processor.calls.Add(1)
	processor.once.Do(func() { close(processor.entered) })
	select {
	case <-processor.release:
		return coordinator.Result{}, nil
	case <-ctx.Done():
		return coordinator.Result{}, ctx.Err()
	}
}

func TestOpenDoesNotCreateDatabaseBeforeRuntimeBindingSucceeds(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2712")
	defer closeRPC()
	if _, err := Open(context.Background(), config, manifest); err == nil {
		t.Fatal("Open accepted wrong runtime chain")
	}
	if _, err := os.Stat(config.Database.Path); !os.IsNotExist(err) {
		t.Fatalf("runtime failure mutated database path: %v", err)
	}
}

func appFixture(t *testing.T, runtimeChainID string) (bootstrap.Config, bootstrap.DeploymentManifest, func()) {
	t.Helper()
	gatewayCode, verifierCode := []byte{0x60, 0x01}, []byte{0x60, 0x02}
	gateway := common.HexToAddress("0x1111111111111111111111111111111111111111")
	verifier := common.HexToAddress("0x2222222222222222222222222222222222222222")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     uint64            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&call)
		result := any(runtimeChainID)
		if call.Method == "eth_getCode" {
			var address string
			_ = json.Unmarshal(call.Params[0], &address)
			if common.HexToAddress(address) == gateway {
				result = "0x6001"
			} else {
				result = "0x6002"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
	}))
	signers := []bootstrap.AuthorizedSigner{
		{Address: "0x1000000000000000000000000000000000000001"},
		{Address: "0x2000000000000000000000000000000000000002"},
		{Address: "0x3000000000000000000000000000000000000003"},
	}
	cost := uint64(3_000_096)
	profile := &bootstrap.DirectVerifierProfile{Version: 1, ProfileID: "pow-spv-3m", ContractName: "ExperimentalCostedDirectVerifier", AuthorizedSigners: signers, SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: &cost}
	config := bootstrap.Config{Version: 1, Name: "mapnode-c", HomeChain: bootstrap.HomeChain{Name: "chain-c", ChainID: "10001", HTTPRPC: server.URL, Confirmations: 2, GatewayManifest: "/runtime/deployment/gateway-manifest.json"}, Database: bootstrap.Database{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "runtime", "mapnode.sqlite")}, DirectVerifier: bootstrap.DirectVerifier{Profile: profile}}
	config.Chains = []bootstrap.ChainCatalog{
		{Name: "chain-c", ChainID: "10001", HTTPRPC: server.URL, Confirmations: 2, DeploymentManifest: "/runtime/chains/chain-c/deployment/gateway-manifest.json", Home: true},
		{Name: "chain-d", ChainID: "10002", HTTPRPC: "http://remote-not-dialed.invalid:8545", Confirmations: 3, DeploymentManifest: "/runtime/chains/chain-d/deployment/gateway-manifest.json"},
	}
	if err := os.MkdirAll(filepath.Dir(config.Database.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := bootstrap.DeploymentManifest{Version: 1, Status: "deployed", ChainID: "10001", DeploymentBlock: 1, MerkleDepth: 8, Gateway: gateway.Hex(), DirectVerifier: verifier.Hex(), ProfileID: "pow-spv-3m", AuthorizedSigners: []string{signers[0].Address, signers[1].Address, signers[2].Address}, SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: &cost, CodeHashes: bootstrap.CodeHashes{Gateway: crypto.Keccak256Hash(gatewayCode).Hex(), DirectVerifier: crypto.Keccak256Hash(verifierCode).Hex()}}
	return config, manifest, server.Close
}
