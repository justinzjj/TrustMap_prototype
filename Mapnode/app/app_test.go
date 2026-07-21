package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/bootstrap"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestOpenBuildsReadyPhaseThreeAppFromValidatedDeployment(t *testing.T) {
	config, manifest, closeRPC := appFixture(t, "0x2711")
	defer closeRPC()
	application, err := Open(context.Background(), config, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if !application.Ready() || application.Registry == nil || application.ChainCatalog == nil || application.LiveChains == nil || application.CanonicalCursors == nil || application.TrustRootObservations == nil || application.TrustView == nil || application.PathProofBuilder == nil {
		t.Fatalf("incomplete Phase 3 composition: %+v", application)
	}
	if _, err := application.Process(context.Background(), coordinator.Work{}); !errors.Is(err, coordinator.ErrInvalidObservation) {
		t.Fatalf("healthy Process did not reach coordinator validation: %v", err)
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
	if _, _, err := application.CanonicalCursors.Initialize(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if _, _, err := application.CanonicalCursors.Advance(ctx, chainID, initial.Hash, reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0x70")}); !errors.Is(err, reorg.ErrCanonicalMismatch) {
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
