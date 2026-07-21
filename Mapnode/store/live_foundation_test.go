package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestLiveFoundationMigrationIsV4AndPreservesPriorChecksums(t *testing.T) {
	db := openTestDB(t)
	for _, table := range []string{"live_chains", "canonical_cursors", "trust_root_observations"} {
		var strict int
		if err := db.sql.QueryRow("SELECT strict FROM pragma_table_list WHERE name=?", table).Scan(&strict); err != nil || strict != 1 {
			t.Fatalf("table %s strict=%d err=%v", table, strict, err)
		}
	}
	var version, count int
	if err := db.sql.QueryRow("SELECT MAX(version),COUNT(*) FROM schema_migrations").Scan(&version, &count); err != nil || version != 4 || count != 4 {
		t.Fatalf("migration history max=%d count=%d err=%v", version, count, err)
	}
	checksums := [][32]byte{sha256.Sum256([]byte(phase3Schema)), sha256.Sum256([]byte(pathProofIntegrityV2)), sha256.Sum256([]byte(liveObservationFoundationV3))}
	wants := []string{"ab9d49540980805aa7f76dd84a4c99bce4df85ed61f4f9eeb3285c91ec40dc4a", "795e28e399ac9df3d8a39a1dbf309ca964de3025a5f4090758bd98a2eda8841e", "dff57af6e30db4a26d11a45c29ac46a5425f563552b6a5430af05b9f69c3a088"}
	for index, checksum := range checksums {
		if got := common.Bytes2Hex(checksum[:]); got != wants[index] {
			t.Fatalf("v%d checksum drifted: %s", index+1, got)
		}
	}
}

func TestLiveChainRepositoryIsStaticAndDeploymentBindingFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live-chain.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewLiveChainRepository(db)
	registry := liveRegistry(t)
	if err := repository.Sync(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10002)
	deployment := chain.GatewayDeployment{ChainID: chainID, Address: common.HexToAddress("0x2000000000000000000000000000000000000002"), CodeHash: common.HexToHash("0x22")}
	block, _ := domain.NewBlockHeight(5)
	if err := repository.BindDeployment(context.Background(), deployment, block); err != nil {
		t.Fatal(err)
	}
	changed := deployment
	changed.CodeHash[31] ^= 1
	if _, err := db.sql.Exec(`UPDATE live_chains SET gateway=NULL,gateway_code_hash=NULL,deployment_block=NULL,validated_at=NULL WHERE chain_id=?`, chainID[:]); err == nil {
		t.Fatal("validated deployment binding was cleared")
	}
	if _, err := db.sql.Exec(`DELETE FROM live_chains WHERE chain_id=?`, chainID[:]); err == nil {
		t.Fatal("live chain catalog row was deleted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository = NewLiveChainRepository(db)
	entries := registry.All()
	entries[1].HTTPRPC = "http://changed-beta:8545"
	changedRegistry, err := chain.NewRegistry(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Sync(context.Background(), changedRegistry); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("restarted changed catalog error=%v", err)
	}
	if err := repository.BindDeployment(context.Background(), changed, block); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("restarted changed deployment error=%v", err)
	}
}

func TestTrustRootObservationRepositoryActivatesSyntheticEvidenceAndOnlyOneNode(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	live := NewLiveChainRepository(db)
	registry := liveRegistry(t)
	if err := live.Sync(ctx, registry); err != nil {
		t.Fatal(err)
	}
	observation := testObservation(t)
	block, _ := domain.NewBlockHeight(5)
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: observation.ChainID, Address: observation.Gateway, CodeHash: observation.GatewayCodeHash}, block); err != nil {
		t.Fatal(err)
	}
	repository := NewTrustRootObservationRepository(db)
	persisted, node, created, err := repository.Save(ctx, observation)
	if err != nil || !created || persisted.ID != observation.ID || node != observation.TrustNode() {
		t.Fatalf("Save() observation=%+v node=%+v created=%v err=%v", persisted, node, created, err)
	}
	loaded, err := repository.Load(ctx, observation.ID)
	if err != nil || loaded.ID != observation.ID {
		t.Fatalf("Load()=%+v err=%v", loaded, err)
	}
	var state evidence.State
	syntheticID := observation.SyntheticEvidence().ID
	if err := db.sql.QueryRow("SELECT state FROM evidence WHERE id=?", syntheticID[:]).Scan(&state); err != nil || state != evidence.Active {
		t.Fatalf("synthetic evidence state=%s err=%v", state, err)
	}
	if _, _, created, err := repository.Save(ctx, observation); err != nil || created {
		t.Fatalf("idempotent Save created=%v err=%v", created, err)
	}
	advancedHead := observation
	advancedHead.ID = trustview.TrustRootObservationID{}
	advancedHead.ConfirmedHeadHeight, _ = domain.NewBlockHeight(44)
	advancedHead.ConfirmedHeadHash = common.HexToHash("0x44")
	advancedHead.ID = advancedHead.ComputeID()
	reused, _, created, err := repository.Save(ctx, advancedHead)
	if err != nil || created || reused.ID != observation.ID {
		t.Fatalf("advanced-head Save reused=%x created=%v err=%v", reused.ID, created, err)
	}
	if _, err := db.sql.Exec(`INSERT INTO trust_edges(edge_id,from_node_id,to_node_id,evidence_id,active,dependency_leaf_index,path_step_cost,created_at)
		VALUES(?,?,?,?,1,0,1,1)`, blob32(0xef), node.ID[:], node.ID[:], node.EvidenceID[:]); err == nil {
		t.Fatal("TrustRootObservation synthetic evidence created a TrustEdge")
	}
}

func TestTrustRootObservationRepositoryPersistsZeroInitialTrustRoot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	live := NewLiveChainRepository(db)
	if err := live.Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	observation := testObservation(t)
	observation.TrustRoot = trustview.TrustRoot{}
	observation.ID = observation.ComputeID()
	block, _ := domain.NewBlockHeight(5)
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: observation.ChainID, Address: observation.Gateway, CodeHash: observation.GatewayCodeHash}, block); err != nil {
		t.Fatal(err)
	}
	repository := NewTrustRootObservationRepository(db)
	persisted, node, created, err := repository.Save(ctx, observation)
	if err != nil || !created || persisted.TrustRoot.Hash != (common.Hash{}) || node.Root.Hash != (common.Hash{}) {
		t.Fatalf("Save zero root persisted=%+v node=%+v created=%v err=%v", persisted, node, created, err)
	}
	loaded, err := repository.Load(ctx, persisted.ID)
	if err != nil || loaded.TrustRoot.Hash != (common.Hash{}) {
		t.Fatalf("Load zero root=%+v err=%v", loaded, err)
	}
}

func TestInactiveSyntheticObservationEvidenceCannotCreateTrustNode(t *testing.T) {
	db := openTestDB(t)
	observation := testObservation(t)
	record := observation.SyntheticEvidence()
	if _, err := db.sql.Exec(`INSERT INTO evidence(id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at)
		VALUES(?,?,?,?,?,?,0,0,?,'candidate','',1,1)`, record.ID[:], record.Locator.ChainID[:], record.Locator.ContractAddress[:], record.Locator.BlockNumber[:], record.Locator.BlockHash[:], record.Locator.TxHash[:], record.Locator.PayloadDigest[:]); err != nil {
		t.Fatal(err)
	}
	node := observation.TrustNode()
	if _, err := db.sql.Exec(`INSERT INTO trust_nodes(node_id,chain_id,block_height,block_hash,trust_root,evidence_id,evidence_state,first_observed_at)
		VALUES(?,?,?,?,?,?,'active',1)`, node.ID[:], node.Key.ChainID[:], node.Key.Height[:], node.Key.BlockHash[:], node.Root.Hash[:], node.EvidenceID[:]); err == nil {
		t.Fatal("inactive synthetic evidence created TrustNode")
	}
}

func TestCanonicalCursorMismatchPersistsDegradedAcrossRestartAndBlocksAdvance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := NewLiveChainRepository(db).Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	repository := NewCanonicalCursorRepository(db)
	if _, created, err := repository.Initialize(ctx, reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))); err != nil || !created {
		t.Fatalf("Initialize created=%v err=%v", created, err)
	}
	nextHeight, _ := domain.NewBlockHeight(8)
	cursor, _, err := repository.Advance(ctx, chainID, common.HexToHash("0xbad"), reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0xbad")})
	if !errors.Is(err, reorg.ErrCanonicalMismatch) || cursor.State != reorg.Degraded {
		t.Fatalf("Advance cursor=%+v err=%v", cursor, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository = NewCanonicalCursorRepository(db)
	loaded, err := repository.Load(ctx, chainID)
	if err != nil || loaded.State != reorg.Degraded {
		t.Fatalf("Load cursor=%+v err=%v", loaded, err)
	}
	if _, _, err := repository.Advance(ctx, chainID, loaded.Hash, reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: loaded.Hash}); !errors.Is(err, reorg.ErrCursorDegraded) {
		t.Fatalf("degraded Advance err=%v", err)
	}
}

func TestCanonicalCursorSameHeightReplacementPersistsDegraded(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := NewLiveChainRepository(db).Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	repository := NewCanonicalCursorRepository(db)
	initial := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))
	if _, _, err := repository.Initialize(ctx, initial); err != nil {
		t.Fatal(err)
	}
	cursor, changed, err := repository.Advance(ctx, chainID, initial.Hash, reorg.CanonicalBlock{Height: height, Hash: common.HexToHash("0x77"), ParentHash: common.HexToHash("0x6")})
	if !errors.Is(err, reorg.ErrCanonicalMismatch) || !changed || cursor.State != reorg.Degraded {
		t.Fatalf("same-height replacement cursor=%+v changed=%v err=%v", cursor, changed, err)
	}
	loaded, err := repository.Load(ctx, chainID)
	if err != nil || loaded.State != reorg.Degraded || loaded.Hash != initial.Hash {
		t.Fatalf("persisted cursor=%+v err=%v", loaded, err)
	}
}

func TestCanonicalCursorForkSwitchPersistsDegradedWithoutSplicing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := NewLiveChainRepository(db).Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	nextHeight, _ := domain.NewBlockHeight(8)
	repository := NewCanonicalCursorRepository(db)
	initial := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))
	if _, _, err := repository.Initialize(ctx, initial); err != nil {
		t.Fatal(err)
	}
	next := reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0x70")}
	cursor, changed, err := repository.Advance(ctx, chainID, initial.Hash, next)
	if !errors.Is(err, reorg.ErrCanonicalMismatch) || !changed || cursor.State != reorg.Degraded || cursor.Height != initial.Height || cursor.Hash != initial.Hash {
		t.Fatalf("fork-switch cursor=%+v changed=%v err=%v", cursor, changed, err)
	}
	loaded, err := repository.Load(ctx, chainID)
	if err != nil || loaded != cursor {
		t.Fatalf("persisted degraded cursor=%+v err=%v", loaded, err)
	}
}

func TestCanonicalCursorRepositoryReportsDegradedAndDatabaseFailure(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repository := NewCanonicalCursorRepository(db)
	degraded, err := repository.HasDegradedCanonicalCursor(ctx)
	if err != nil || degraded {
		t.Fatalf("empty degraded=%v err=%v", degraded, err)
	}
	if err := NewLiveChainRepository(db).Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	nextHeight, _ := domain.NewBlockHeight(8)
	initial := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))
	_, _, _ = repository.Initialize(ctx, initial)
	_, _, _ = repository.Advance(ctx, chainID, initial.Hash, reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0x70")})
	degraded, err = repository.HasDegradedCanonicalCursor(ctx)
	if err != nil || !degraded {
		t.Fatalf("persisted degraded=%v err=%v", degraded, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.HasDegradedCanonicalCursor(ctx); err == nil {
		t.Fatal("closed database was reported operational")
	}
}

func TestCanonicalCursorConcurrentDuplicateAdvanceIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := NewLiveChainRepository(db).Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	nextHeight, _ := domain.NewBlockHeight(8)
	repository := NewCanonicalCursorRepository(db)
	_, _, _ = repository.Initialize(ctx, reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7")))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := repository.Advance(ctx, chainID, common.HexToHash("0x7"), reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x8"), ParentHash: common.HexToHash("0x7")})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent advance err=%v", err)
		}
	}
}

func liveRegistry(t *testing.T) *chain.Registry {
	t.Helper()
	aID, _ := domain.NewChainID(10001)
	bID, _ := domain.NewChainID(10002)
	registry, err := chain.NewRegistry([]chain.Chain{
		{Name: "alpha", ChainID: aID, HTTPRPC: "http://geth-alpha:8545", Confirmations: 2, DeploymentManifest: "/runtime/chains/alpha/deployment/gateway-manifest.json", Home: true},
		{Name: "beta", ChainID: bID, HTTPRPC: "http://geth-beta:8545", Confirmations: 2, DeploymentManifest: "/runtime/chains/beta/deployment/gateway-manifest.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testObservation(t *testing.T) trustview.TrustRootObservation {
	t.Helper()
	chainID, _ := domain.NewChainID(10002)
	height, _ := domain.NewBlockHeight(40)
	head, _ := domain.NewBlockHeight(43)
	observation, err := trustview.NewTrustRootObservation(trustview.TrustRootObservationContent{
		ChainID: chainID, Height: height, BlockHash: common.HexToHash("0x40"), Gateway: common.HexToAddress("0x2000000000000000000000000000000000000002"),
		TrustRoot: trustview.TrustRoot{Hash: common.HexToHash("0x41")}, GatewayCodeHash: common.HexToHash("0x42"), RequiredConfirmations: 2,
		ConfirmedHeadHeight: head, ConfirmedHeadHash: common.HexToHash("0x43"),
	})
	if err != nil {
		t.Fatal(err)
	}
	observation.ObservedAt = time.Now().UTC()
	return observation
}
