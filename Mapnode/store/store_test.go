package store

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestOpenEnablesWALForeignKeysAndCreatesCompleteV1Schema(t *testing.T) {
	db := openTestDB(t)
	var journal string
	if err := db.sql.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}
	var foreignKeys int
	if err := db.sql.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	wantTables := []string{
		"schema_migrations", "evidence", "evidence_transitions", "requests", "request_transitions",
		"trust_nodes", "trust_edges", "trustview_snapshots", "snapshot_nodes", "snapshot_edges",
		"membership_witnesses", "membership_witness_siblings", "plans", "plan_hops",
		"request_current_plan", "proofs", "proof_hops",
	}
	for _, table := range wantTables {
		var strict int
		if err := db.sql.QueryRow("SELECT strict FROM pragma_table_list WHERE name = ?", table).Scan(&strict); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
		if strict != 1 {
			t.Fatalf("table %s is not STRICT", table)
		}
	}
	var version int
	if err := db.sql.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("migration version = %d, want 1", version)
	}
}

func TestMigrationsAreIdempotentAndDetectTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapnode.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if _, err := db.sql.Exec("UPDATE schema_migrations SET checksum = zeroblob(32) WHERE version = 1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(path); !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("tampered checksum Open() error = %v, want %v", err, ErrMigrationChecksum)
	}

	futurePath := filepath.Join(t.TempDir(), "future.sqlite")
	future, err := Open(futurePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := future.sql.Exec(
		"INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(2,'future',zeroblob(32),1)",
	); err != nil {
		t.Fatal(err)
	}
	_ = future.Close()
	if _, err := Open(futurePath); !errors.Is(err, ErrFutureMigration) {
		t.Fatalf("future migration Open() error = %v, want %v", err, ErrFutureMigration)
	}

	gapPath := filepath.Join(t.TempDir(), "gap.sqlite")
	gap, err := Open(gapPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gap.sql.Exec("DELETE FROM schema_migrations WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := gap.sql.Exec(
		"INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(2,'future',zeroblob(32),1)",
	); err != nil {
		t.Fatal(err)
	}
	_ = gap.Close()
	if _, err := Open(gapPath); !errors.Is(err, ErrMigrationGap) {
		t.Fatalf("migration gap Open() error = %v, want %v", err, ErrMigrationGap)
	}
}

func TestEvidenceRepositoryPersistsMaxUint256AndTransitionsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapnode.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewEvidenceRepository(db)
	record := testEvidenceRecord(t, true)
	created, inserted, err := repo.Observe(context.Background(), record)
	if err != nil || !inserted {
		t.Fatalf("Observe() = (%+v, %t, %v)", created, inserted, err)
	}
	if created.Locator.ChainID.BigInt().Cmp(maxUint256()) != 0 || created.Locator.BlockNumber.BigInt().Cmp(maxUint256()) != 0 {
		t.Fatal("Observe() truncated uint256")
	}
	if _, inserted, err := repo.Observe(context.Background(), record); err != nil || inserted {
		t.Fatalf("duplicate Observe() inserted=%t err=%v", inserted, err)
	}

	updated, changed, err := repo.Transition(context.Background(), record.ID, evidence.Candidate, evidence.Verified, evidence.TransitionMetadata{At: time.Unix(20, 0)})
	if err != nil || !changed || updated.State != evidence.Verified {
		t.Fatalf("Transition() = (%+v, %t, %v)", updated, changed, err)
	}
	before := countRows(t, db.sql, "evidence_transitions")
	if _, changed, err := repo.Transition(context.Background(), record.ID, evidence.Candidate, evidence.Verified, evidence.TransitionMetadata{}); err != nil || changed {
		t.Fatalf("idempotent concurrent-style Transition() changed=%t err=%v", changed, err)
	}
	if after := countRows(t, db.sql, "evidence_transitions"); after != before {
		t.Fatalf("idempotent transition added history: %d -> %d", before, after)
	}
	if _, changed, err := repo.Transition(context.Background(), record.ID, evidence.Active, evidence.Verified, evidence.TransitionMetadata{}); err == nil || changed {
		t.Fatalf("illegal edge masked by current target state: changed=%t err=%v", changed, err)
	}
	if after := countRows(t, db.sql, "evidence_transitions"); after != before {
		t.Fatalf("masked illegal transition added history: %d -> %d", before, after)
	}
	if _, _, err := repo.Transition(context.Background(), record.ID, evidence.Verified, evidence.Invalid, evidence.TransitionMetadata{}); err == nil {
		t.Fatal("illegal transition accepted")
	}
	if loaded, err := repo.Load(context.Background(), record.ID); err != nil || loaded.State != evidence.Verified {
		t.Fatalf("failed transition was not rolled back: state=%s err=%v", loaded.State, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, err := NewEvidenceRepository(db).Load(context.Background(), record.ID)
	if err != nil || loaded.Locator.BlockNumber.BigInt().Cmp(maxUint256()) != 0 || loaded.State != evidence.Verified {
		t.Fatalf("reopen Load() = (%+v, %v)", loaded, err)
	}
}

func TestEvidenceLocatorConflictsCannotHideContradictions(t *testing.T) {
	db := openTestDB(t)
	repo := NewEvidenceRepository(db)
	record := testEvidenceRecord(t, false)
	if _, _, err := repo.Observe(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	contradiction := record
	contradiction.ID[0] ^= 1
	if _, _, err := repo.Observe(context.Background(), contradiction); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("contradictory ID error = %v, want %v", err, ErrRecordConflict)
	}
	contradiction = record
	contradiction.Locator.PayloadDigest[0] ^= 1
	if _, _, err := repo.Observe(context.Background(), contradiction); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("contradictory locator error = %v, want %v", err, ErrRecordConflict)
	}

	// Simulate a legacy/corrupt row whose unique locator is attached to another
	// primary key. Observe must re-read every locator field instead of treating
	// ON CONFLICT DO NOTHING as idempotence.
	otherID := record.ID
	otherID[0] ^= 1
	if _, err := db.sql.Exec("UPDATE evidence SET id=? WHERE id=?", otherID[:], record.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Observe(context.Background(), record); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("unique locator conflict error = %v, want %v", err, ErrRecordConflict)
	}
}

func TestEvidenceConcurrentSameTransitionWritesAtMostOneHistoryRow(t *testing.T) {
	db := openTestDB(t)
	repo := NewEvidenceRepository(db)
	record := testEvidenceRecord(t, false)
	if _, _, err := repo.Observe(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := repo.Transition(context.Background(), record.ID, evidence.Candidate, evidence.Verified, evidence.TransitionMetadata{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Transition() error = %v", err)
		}
	}
	if got := countRows(t, db.sql, "evidence_transitions"); got != 1 {
		t.Fatalf("transition history rows = %d, want 1", got)
	}
}

func TestRequestRepositoryImplementsConsumerInterfaceAndPersists(t *testing.T) {
	db := openTestDB(t)
	var repo coordinator.RequestRepository = NewRequestRepository(db)
	request := testRequest(t)
	created, inserted, err := repo.Observe(context.Background(), request)
	if err != nil || !inserted || created.Nonce != request.Nonce {
		t.Fatalf("Observe() = (%+v, %t, %v)", created, inserted, err)
	}
	if _, inserted, err := repo.Observe(context.Background(), request); err != nil || inserted {
		t.Fatalf("duplicate Observe() inserted=%t err=%v", inserted, err)
	}
	for _, edge := range [][2]coordinator.RequestState{
		{coordinator.Observed, coordinator.EvidenceReady},
		{coordinator.EvidenceReady, coordinator.Planned},
		{coordinator.Planned, coordinator.ProofReady},
	} {
		if _, changed, err := repo.Transition(context.Background(), request.ID, edge[0], edge[1], coordinator.TransitionMetadata{}); err != nil || !changed {
			t.Fatalf("Transition(%s,%s) changed=%t err=%v", edge[0], edge[1], changed, err)
		}
	}
	before := countRows(t, db.sql, "request_transitions")
	if _, changed, err := repo.Transition(context.Background(), request.ID, coordinator.Planned, coordinator.ProofReady, coordinator.TransitionMetadata{}); err != nil || changed {
		t.Fatalf("idempotent request transition changed=%t err=%v", changed, err)
	}
	if got := countRows(t, db.sql, "request_transitions"); got != before {
		t.Fatalf("idempotent request transition history = %d, want %d", got, before)
	}
	if _, changed, err := repo.Transition(context.Background(), request.ID, coordinator.Rejected, coordinator.ProofReady, coordinator.TransitionMetadata{}); err == nil || changed {
		t.Fatalf("illegal request edge masked by current target state: changed=%t err=%v", changed, err)
	}
	if got := countRows(t, db.sql, "request_transitions"); got != before {
		t.Fatalf("masked illegal request transition history = %d, want %d", got, before)
	}
	if _, err := db.sql.Exec("UPDATE requests SET state='submitted' WHERE id=?", request.ID[:]); err == nil {
		t.Fatal("Phase 4 submitted state accepted by Phase 3 schema")
	}
}

func TestStrictBlobLengthsAndForeignKeysAreEnforced(t *testing.T) {
	db := openTestDB(t)
	_, err := db.sql.Exec(`INSERT INTO evidence(
		id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at
	) VALUES(zeroblob(31),zeroblob(32),zeroblob(20),zeroblob(32),zeroblob(32),zeroblob(32),0,0,zeroblob(32),'candidate','',1,1)`)
	if err == nil {
		t.Fatal("31-byte evidence ID accepted")
	}
	_, err = db.sql.Exec("INSERT INTO evidence_transitions(evidence_id,from_state,to_state,reason,changed_at) VALUES(zeroblob(32),'candidate','verified','',1)")
	if err == nil {
		t.Fatal("orphan transition accepted")
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "mapnode.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testEvidenceRecord(t *testing.T, maximum bool) evidence.Record {
	t.Helper()
	chainValue := big.NewInt(10)
	heightValue := big.NewInt(11)
	if maximum {
		chainValue = maxUint256()
		heightValue = maxUint256()
	}
	chainID, _ := domain.NewChainIDFromBig(chainValue)
	height, _ := domain.NewBlockHeightFromBig(heightValue)
	locator := evidence.Locator{
		ChainID: chainID, ContractAddress: common.HexToAddress("0x1000000000000000000000000000000000000001"),
		BlockNumber: height, BlockHash: common.HexToHash("0x11"), TxHash: common.HexToHash("0x22"),
		TxIndex: 3, LogIndex: 4, PayloadDigest: common.HexToHash("0x33"),
	}
	id, err := evidence.ComputeID(locator)
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Record{ID: id, Locator: locator, State: evidence.Candidate, CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(10, 0)}
}

func testRequest(t *testing.T) coordinator.Request {
	t.Helper()
	home, _ := domain.NewChainIDFromBig(maxUint256())
	source, _ := domain.NewChainIDFromBig(new(big.Int).Sub(maxUint256(), big.NewInt(1)))
	height, _ := domain.NewBlockHeightFromBig(maxUint256())
	nonce := coordinator.Uint256{}
	for index := range nonce {
		nonce[index] = 0xff
	}
	gateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	requester := common.HexToAddress("0x2000000000000000000000000000000000000002")
	blockHash := common.HexToHash("0x44")
	id, err := evidence.ComputeGatewayRequestID(home, gateway, requester, maxUint256(), source, height, blockHash)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator.Request{
		ID: id, HomeChainID: home, Gateway: gateway, Requester: requester, Nonce: nonce,
		SourceChainID: source, SourceHeight: height, SourceBlockHash: blockHash,
		State: coordinator.Observed, CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(10, 0),
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func maxUint256() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
}
