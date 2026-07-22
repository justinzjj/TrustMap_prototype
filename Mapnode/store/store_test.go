package store

import (
	"context"
	"crypto/sha256"
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

func TestOpenEnablesWALForeignKeysAndCreatesCompletePhaseThreeSchema(t *testing.T) {
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
	var recursiveTriggers int
	if err := db.sql.QueryRow("PRAGMA recursive_triggers").Scan(&recursiveTriggers); err != nil {
		t.Fatal(err)
	}
	if recursiveTriggers != 1 {
		t.Fatalf("recursive_triggers = %d, want 1", recursiveTriggers)
	}
	wantTables := []string{
		"schema_migrations", "evidence", "evidence_transitions", "requests", "request_transitions", "graph_state",
		"trust_nodes", "trust_edges", "trustview_snapshots", "snapshot_nodes", "snapshot_edges",
		"membership_witnesses", "membership_witness_siblings", "plans", "plan_hops",
		"request_current_plan", "proofs", "proof_hops",
		"evidence_inbox", "evidence_outbox",
		"transaction_submissions", "confirmed_transaction_receipts", "request_execution_transitions",
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
	var version, count int
	if err := db.sql.QueryRow("SELECT MAX(version),COUNT(*) FROM schema_migrations").Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 8 || count != 8 {
		t.Fatalf("migration history = max %d count %d, want max 8 count 8", version, count)
	}
	if got := sha256.Sum256([]byte(phase3Schema)); got != [32]byte{0xab, 0x9d, 0x49, 0x54, 0x09, 0x80, 0x80, 0x5a, 0xa7, 0xf7, 0x6d, 0xd8, 0x4a, 0x4c, 0x99, 0xbc, 0xe4, 0xdf, 0x85, 0xed, 0x61, 0xf4, 0xf9, 0xee, 0xb3, 0x28, 0x5c, 0x91, 0xec, 0x40, 0xdc, 0x4a} {
		t.Fatalf("v1 migration checksum drifted: %x", got)
	}
}

func TestRequestTransitionHistoryUsesPhase3StateEnum(t *testing.T) {
	db := openTestDB(t)
	request := testRequest(t)
	if _, _, err := NewRequestRepository(db).Observe(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"submitted", "confirmed", "arbitrary"} {
		_, err := db.sql.Exec(`INSERT INTO request_transitions(
			request_id,from_state,to_state,reason,changed_at
		) VALUES(?,?,?,'',1)`, request.ID[:], invalid, coordinator.Observed)
		if err == nil {
			t.Fatalf("request transition from_state %q accepted", invalid)
		}
		_, err = db.sql.Exec(`INSERT INTO request_transitions(
			request_id,from_state,to_state,reason,changed_at
		) VALUES(?,?,?,'',1)`, request.ID[:], coordinator.Observed, invalid)
		if err == nil {
			t.Fatalf("request transition to_state %q accepted", invalid)
		}
	}
}

func TestSnapshotAndPlanSchemaEnforcesFrozenSameSnapshotReferences(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	evidenceRecord := testEvidenceRecord(t, false)
	if _, _, err := NewEvidenceRepository(db).Observe(ctx, evidenceRecord); err != nil {
		t.Fatal(err)
	}
	evidenceRecordB := evidenceRecord
	evidenceRecordB.Locator.PayloadDigest[31] ^= 1
	evidenceRecordB.ID, _ = evidence.ComputeID(evidenceRecordB.Locator)
	if _, _, err := NewEvidenceRepository(db).Observe(ctx, evidenceRecordB); err != nil {
		t.Fatal(err)
	}
	request := testRequest(t)
	if _, _, err := NewRequestRepository(db).Observe(ctx, request); err != nil {
		t.Fatal(err)
	}
	requestB := testRequestWithNonceLastByte(t, 0xfe)
	if _, _, err := NewRequestRepository(db).Observe(ctx, requestB); err != nil {
		t.Fatal(err)
	}

	var revision int64
	if err := db.sql.QueryRow("SELECT revision FROM graph_state WHERE singleton=1").Scan(&revision); err != nil {
		t.Fatalf("load graph_state singleton: %v", err)
	}
	if revision != 0 {
		t.Fatalf("initial graph revision = %d, want 0", revision)
	}

	witnessID := blob32(0x71)
	if _, err := db.sql.Exec(`INSERT INTO membership_witnesses(
		witness_id,evidence_id,leaf_index,created_at
	) VALUES(?,?,0,1)`, witnessID, evidenceRecord.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec("UPDATE membership_witnesses SET leaf_index=1 WHERE witness_id=?", witnessID); err == nil {
		t.Fatal("global witness core fields remained overwritable")
	}
	if _, err := db.sql.Exec(`INSERT OR REPLACE INTO membership_witnesses(
		witness_id,evidence_id,leaf_index,created_at
	) VALUES(?,?,1,1)`, witnessID, evidenceRecord.ID[:]); err == nil {
		t.Fatal("global witness was overwritten with INSERT OR REPLACE")
	}
	if _, err := db.sql.Exec(`INSERT INTO membership_witness_siblings(
		witness_id,sibling_index,sibling_hash
	) VALUES(?,0,zeroblob(32))`, witnessID); err != nil {
		t.Fatalf("insert witness sibling before snapshot use: %v", err)
	}
	witnessB := blob32(0x73)
	if _, err := db.sql.Exec(`INSERT INTO membership_witnesses(
		witness_id,evidence_id,leaf_index,created_at
	) VALUES(?,?,0,1)`, witnessB, evidenceRecordB.ID[:]); err != nil {
		t.Fatal(err)
	}

	snapshotA, snapshotB := blob32(0xa1), blob32(0xb1)
	homeA, targetA := blob32(0xa2), blob32(0xa3)
	homeB, targetB := blob32(0xb2), blob32(0xb3)
	homeAHash, targetAHash := blob32(0x41), blob32(0x42)
	homeBHash, targetBHash := blob32(0x51), blob32(0x52)
	chainID, _ := domain.NewChainID(99)
	height, _ := domain.NewBlockHeight(100)
	for _, snapshot := range [][]byte{snapshotA, snapshotB} {
		if _, err := db.sql.Exec(`INSERT INTO trustview_snapshots(
			snapshot_id,graph_revision,home_chain_id,home_trust_root,start_node_id,target_node_id,sealed,created_at
		) VALUES(?,0,?,zeroblob(32),NULL,NULL,0,1)`, snapshot, chainID[:]); err != nil {
			t.Fatalf("insert unsealed snapshot: %v", err)
		}
	}
	for _, row := range []struct{ snapshot, node, blockHash []byte }{
		{snapshotA, homeA, homeAHash}, {snapshotA, targetA, targetAHash},
		{snapshotB, homeB, homeBHash}, {snapshotB, targetB, targetBHash},
	} {
		if _, err := db.sql.Exec(`INSERT INTO snapshot_nodes(
			snapshot_id,node_id,chain_id,block_height,block_hash,trust_root
		) VALUES(?,?,?,?,?,zeroblob(32))`, row.snapshot, row.node, chainID[:], height[:], row.blockHash); err != nil {
			t.Fatalf("insert snapshot node: %v", err)
		}
	}
	if _, err := db.sql.Exec("UPDATE trustview_snapshots SET sealed=1 WHERE snapshot_id=?", snapshotA); err == nil {
		t.Fatal("sealed snapshot without endpoints accepted")
	}
	if _, err := db.sql.Exec(`UPDATE trustview_snapshots
		SET start_node_id=?,target_node_id=?,sealed=1 WHERE snapshot_id=?`, homeA, targetB, snapshotA); err == nil {
		t.Fatal("snapshot accepted endpoint from another snapshot")
	}

	edgeA, edgeB := blob32(0xe1), blob32(0xe2)
	missingWitnessEdge := blob32(0xe0)
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,NULL,0,1)`, snapshotA, missingWitnessEdge, homeA, targetA, evidenceRecord.ID[:]); err != nil {
		t.Fatalf("insert active edge with missing proof material: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,?,0,1)`, snapshotA, edgeA, homeA, targetA, evidenceRecord.ID[:], witnessID); err != nil {
		t.Fatalf("insert snapshot A edge: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,?,1,1)`, snapshotA, blob32(0xed), homeA, targetA, evidenceRecord.ID[:], witnessID); err == nil {
		t.Fatal("snapshot edge accepted witness with wrong dependency leaf index")
	}
	if _, err := db.sql.Exec(`INSERT INTO membership_witness_siblings(
		witness_id,sibling_index,sibling_hash
	) VALUES(?,1,zeroblob(32))`, witnessID); err == nil {
		t.Fatal("snapshot-referenced witness accepted another sibling")
	}
	if _, err := db.sql.Exec(`INSERT OR REPLACE INTO membership_witness_siblings(
		witness_id,sibling_index,sibling_hash
	) VALUES(?,0,?)`, witnessID, blob32(0x72)); err == nil {
		t.Fatal("snapshot-referenced witness sibling was overwritten")
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,?,0,1)`, snapshotB, edgeB, homeB, targetB, evidenceRecord.ID[:], witnessID); err != nil {
		t.Fatalf("insert snapshot B edge: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,?,0,1)`, snapshotA, blob32(0xee), homeA, targetA, evidenceRecord.ID[:], witnessB); err == nil {
		t.Fatal("snapshot edge accepted witness belonging to different evidence")
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,?,0,1)`, snapshotA, blob32(0xe3), homeA, targetB, evidenceRecord.ID[:], witnessID); err == nil {
		t.Fatal("snapshot edge accepted node from another snapshot")
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_edges(
		snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,dependency_leaf_index,path_step_cost
	) VALUES(?,?,?,?,?,?,0,1)`, snapshotA, blob32(0xe4), homeA, targetA, evidenceRecord.ID[:], blob32(0xff)); err == nil {
		t.Fatal("snapshot edge accepted missing witness")
	}
	if _, err := db.sql.Exec(`INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,99,'path',?,?,1,1,1,3000096,'',1)`,
		blob32(0xca), request.ID[:], snapshotA, "pow-spv-3m", blob32(0xcb), homeA, targetA,
	); err == nil {
		t.Fatal("plan accepted an unsealed snapshot")
	}
	if _, err := db.sql.Exec(`UPDATE trustview_snapshots
		SET start_node_id=?,target_node_id=?,sealed=1 WHERE snapshot_id=?`, homeA, targetA, snapshotA); err != nil {
		t.Fatalf("seal snapshot A: %v", err)
	}
	if _, err := db.sql.Exec(`UPDATE trustview_snapshots
		SET start_node_id=?,target_node_id=?,sealed=1 WHERE snapshot_id=?`, homeB, targetB, snapshotB); err != nil {
		t.Fatalf("seal snapshot B: %v", err)
	}
	var missingProofMaterial int
	if err := db.sql.QueryRow(`SELECT count(*) FROM snapshot_edges
		WHERE snapshot_id=? AND edge_id=? AND witness_id IS NULL`, snapshotA, missingWitnessEdge).Scan(&missingProofMaterial); err != nil {
		t.Fatal(err)
	}
	if missingProofMaterial != 1 {
		t.Fatal("sealed snapshot lost active edge with missing proof material")
	}
	if _, err := db.sql.Exec("UPDATE trustview_snapshots SET graph_revision=1 WHERE snapshot_id=?", snapshotA); err == nil {
		t.Fatal("sealed snapshot remained mutable")
	}
	if _, err := db.sql.Exec(`INSERT INTO snapshot_nodes(
		snapshot_id,node_id,chain_id,block_height,block_hash,trust_root
	) VALUES(?,?,?,?,zeroblob(32),zeroblob(32))`, snapshotA, blob32(0xa4), chainID[:], height[:]); err == nil {
		t.Fatal("sealed snapshot accepted another node")
	}

	if _, err := db.sql.Exec(`INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,1,'path',?,?,1,1,1,3000096,'',1)`,
		blob32(0xc0), request.ID[:], snapshotA, "pow-spv-3m", blob32(0xc2), homeB, targetA,
	); err == nil {
		t.Fatal("plan accepted home node from another snapshot")
	}
	if _, err := db.sql.Exec(`INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,2,'path',?,?,1,1,1,3000096,'',1)`,
		blob32(0xcf), request.ID[:], snapshotA, "pow-spv-3m", make([]byte, 31), homeA, targetA,
	); err == nil {
		t.Fatal("plan accepted non-32-byte profile fingerprint")
	}

	planID := blob32(0xc1)
	if _, err := db.sql.Exec(`INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,0,'path',?,?,1,1,1,3000096,'',1)`,
		planID, request.ID[:], snapshotA, "pow-spv-3m", blob32(0xc2), homeA, targetA,
	); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO plan_hops(
		plan_id,snapshot_id,hop_index,edge_id
	) VALUES(?,?,0,?)`, planID, snapshotB, edgeB); err == nil {
		t.Fatal("plan hop accepted edge from another snapshot")
	}
	if _, err := db.sql.Exec(`INSERT INTO plan_hops(
		plan_id,snapshot_id,hop_index,edge_id
	) VALUES(?,?,0,?)`, planID, snapshotA, edgeB); err == nil {
		t.Fatal("plan hop accepted nonexistent edge in plan snapshot")
	}
	if _, err := db.sql.Exec(`INSERT INTO plan_hops(
		plan_id,snapshot_id,hop_index,edge_id
	) VALUES(?,?,0,?)`, planID, snapshotA, edgeA); err != nil {
		t.Fatalf("insert same-snapshot plan hop: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO plan_hops(
		plan_id,snapshot_id,hop_index,edge_id
	) VALUES(?,?,1,?)`, planID, snapshotA, edgeA); err == nil {
		t.Fatal("plan hop index equal to hop_count accepted")
	}
	if _, err := db.sql.Exec("UPDATE plans SET fallback_reason='mutated' WHERE plan_id=?", planID); err == nil {
		t.Fatal("persisted plan remained mutable")
	}
	if _, err := db.sql.Exec("UPDATE plan_hops SET hop_index=7 WHERE plan_id=?", planID); err == nil {
		t.Fatal("persisted plan hop remained mutable")
	}
	if _, err := db.sql.Exec("DELETE FROM plan_hops WHERE plan_id=?", planID); err == nil {
		t.Fatal("persisted plan hop remained deletable")
	}
	if _, err := db.sql.Exec("DELETE FROM plans WHERE plan_id=?", planID); err == nil {
		t.Fatal("persisted plan remained deletable")
	}

	planB := blob32(0xd1)
	if _, err := db.sql.Exec(`INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,0,'direct',?,?,0,1,NULL,3000096,'no_path',1)`,
		planB, requestB.ID[:], snapshotB, "pow-spv-3m", blob32(0xd2), homeB, targetB,
	); err != nil {
		t.Fatalf("insert request B plan: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO request_current_plan(request_id,plan_id)
		VALUES(?,?)`, request.ID[:], planB); err == nil {
		t.Fatal("request A accepted request B plan as current")
	}
	if _, err := db.sql.Exec(`INSERT INTO request_current_plan(request_id,plan_id)
		VALUES(?,?)`, request.ID[:], planID); err != nil {
		t.Fatalf("bind request A current plan: %v", err)
	}

	proofID := blob32(0xf1)
	if _, err := db.sql.Exec(`INSERT INTO proofs(
		proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at
	) VALUES(?,?,?,?,zeroblob(32),1)`, blob32(0xf2), requestB.ID[:], planID, snapshotA); err == nil {
		t.Fatal("proof combined request B with request A plan")
	}
	if _, err := db.sql.Exec(`INSERT INTO proofs(
		proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at
	) VALUES(?,?,?,?,zeroblob(32),1)`, blob32(0xf3), request.ID[:], planID, snapshotB); err == nil {
		t.Fatal("proof combined plan A with snapshot B")
	}
	if _, err := db.sql.Exec(`INSERT INTO proofs(
		proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at
	) VALUES(?,?,?,?,zeroblob(32),1)`, proofID, request.ID[:], planID, snapshotA); err != nil {
		t.Fatalf("insert bound proof: %v", err)
	}
	insertProofHop := func(plan, snapshot []byte, edge []byte, toNode []byte, blockHash []byte, witness []byte) error {
		_, err := db.sql.Exec(`INSERT INTO proof_hops(
			proof_id,plan_id,snapshot_id,hop_index,plan_hop_index,edge_id,to_node_id,block_hash,witness_id
		) VALUES(?,?,?,0,0,?,?,?,?)`, proofID, plan, snapshot, edge, toNode, blockHash, witness)
		return err
	}
	for _, invalid := range []struct {
		name                                  string
		plan, snapshot, edge, node, hash, wit []byte
	}{
		{"plan", planB, snapshotA, edgeA, targetA, targetAHash, witnessID},
		{"snapshot", planID, snapshotB, edgeB, targetB, targetBHash, witnessID},
		{"edge", planID, snapshotA, edgeB, targetA, targetAHash, witnessID},
		{"witness", planID, snapshotA, edgeA, targetA, targetAHash, witnessB},
		{"block hash", planID, snapshotA, edgeA, targetA, homeAHash, witnessID},
	} {
		if err := insertProofHop(invalid.plan, invalid.snapshot, invalid.edge, invalid.node, invalid.hash, invalid.wit); err == nil {
			t.Fatalf("proof hop accepted mismatched %s", invalid.name)
		}
	}
	if err := insertProofHop(planID, snapshotA, edgeA, targetA, targetAHash, witnessID); err != nil {
		t.Fatalf("insert fully bound proof hop: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,0,'direct',?,?,0,1,NULL,3000096,'no_path',1)`,
		blob32(0xce), request.ID[:], snapshotA, "pow-spv-3m", blob32(0xcd), homeA, targetA,
	); err == nil {
		t.Fatal("duplicate request planning attempt accepted")
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
		"INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(9,'future',zeroblob(32),1)",
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
	return testRequestWithNonceLastByte(t, 0xff)
}

func testRequestWithNonceLastByte(t *testing.T, last byte) coordinator.Request {
	t.Helper()
	home, _ := domain.NewChainIDFromBig(maxUint256())
	source, _ := domain.NewChainIDFromBig(new(big.Int).Sub(maxUint256(), big.NewInt(1)))
	height, _ := domain.NewBlockHeightFromBig(maxUint256())
	nonce := coordinator.Uint256{}
	for index := range nonce {
		nonce[index] = 0xff
	}
	nonce[31] = last
	gateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	requester := common.HexToAddress("0x2000000000000000000000000000000000000002")
	blockHash := common.HexToHash("0x44")
	id, err := evidence.ComputeGatewayRequestID(home, gateway, requester, new(big.Int).SetBytes(nonce[:]), source, height, blockHash)
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

func blob32(fill byte) []byte {
	value := make([]byte, 32)
	for index := range value {
		value[index] = fill
	}
	return value
}
