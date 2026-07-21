package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

func TestParentV1DatabaseUpgradesToV2WithoutChangingChecksumOrProofData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "parent-v1.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrationTable); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(phase3Schema); err != nil {
		t.Fatal(err)
	}
	v1Checksum := sha256.Sum256([]byte(phase3Schema))
	if got := fmt.Sprintf("%x", v1Checksum); got != "ab9d49540980805aa7f76dd84a4c99bce4df85ed61f4f9eeb3285c91ec40dc4a" {
		t.Fatalf("parent v1 checksum drifted: %s", got)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(1,'phase3_core',?,?)`, v1Checksum[:], time.Now().UTC().UnixNano()); err != nil {
		t.Fatal(err)
	}
	legacy := &DB{sql: raw}
	fixture := persistPathProofGraph(t, legacy)
	proofValue, err := pathproof.NewBuilder(NewPathProofRepository(legacy), 1).Build(ctx, pathproof.BuildRequest{PlanID: fixture.plan.ID, SourceBlockHash: fixture.sourceBlockHash, ExpectedHomeTrustRoot: fixture.homeTrustRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade parent v1: %v", err)
	}
	defer upgraded.Close()
	var versions int
	if err := upgraded.sql.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version IN (1,2)").Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("migration history count=%d err=%v", versions, err)
	}
	var persistedChecksum []byte
	if err := upgraded.sql.QueryRow("SELECT checksum FROM schema_migrations WHERE version=1").Scan(&persistedChecksum); err != nil || !equalBytes(persistedChecksum, v1Checksum[:]) {
		t.Fatalf("v1 checksum changed: %x err=%v", persistedChecksum, err)
	}
	reloaded, err := NewPathProofRepository(upgraded).LoadPathProof(ctx, proofValue.ID)
	if err != nil || reloaded.ID != proofValue.ID {
		t.Fatalf("v1 PathProof after upgrade = %+v, %v", reloaded, err)
	}
	for _, object := range []string{"prevent_proof_update", "prevent_proof_delete", "prevent_proof_hop_update", "prevent_proof_hop_delete", "unique_pathproof_per_plan"} {
		var count int
		if err := upgraded.sql.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name=?", object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v2 object %s count=%d err=%v", object, count, err)
		}
	}
}

func TestPathProofRepositoryPersistsSnapshotBoundProofAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "path-proof.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := persistPathProofGraph(t, db)
	repository := NewPathProofRepository(db)
	built, err := pathproof.NewBuilder(repository, 1).Build(ctx, pathproof.BuildRequest{
		PlanID: fixture.plan.ID, SourceBlockHash: fixture.sourceBlockHash,
		ExpectedHomeTrustRoot: fixture.homeTrustRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Hops) != 2 || built.Hops[0].PlanHopIndex != 1 || built.Hops[1].PlanHopIndex != 0 {
		t.Fatalf("persisted PathProof order = %+v", built.Hops)
	}
	tampered := built.Clone()
	tampered.ID[0] ^= 1
	if err := repository.SavePathProof(ctx, tampered); !errors.Is(err, pathproof.ErrPathProofIDMismatch) {
		t.Fatalf("tampered PathProof ID error = %v", err)
	}
	if _, err := db.sql.Exec("UPDATE proofs SET base_trust_root=zeroblob(32) WHERE proof_id=?", built.ID[:]); err == nil {
		t.Fatal("persisted PathProof core remained mutable")
	}
	if _, err := db.sql.Exec("DELETE FROM proof_hops WHERE proof_id=?", built.ID[:]); err == nil {
		t.Fatal("persisted PathProof hop remained deletable")
	}
	duplicateID := built.ID
	duplicateID[0] ^= 1
	if _, err := db.sql.Exec(`INSERT INTO proofs(proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at)
		VALUES(?,?,?,?,?,1)`, duplicateID[:], built.RequestID[:], built.PlanID[:], built.SnapshotID[:], built.BaseTrustRoot.Hash[:]); err == nil {
		t.Fatal("v2 accepted multiple PathProof rows for one plan")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reloaded, err := NewPathProofRepository(db).LoadPathProof(ctx, built.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != built.ID || pathproof.ComputePathProofID(reloaded) != built.ID || len(reloaded.Witnesses) != 2 {
		t.Fatalf("reloaded PathProof = %+v", reloaded)
	}
}

func TestLoadPathProofRejectsSemanticallyInvalidParentV1Rows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "invalid-v1-proofs.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(phase3Schema); err != nil {
		t.Fatal(err)
	}
	legacy := &DB{sql: raw}
	fixture := persistPathProofGraph(t, legacy)
	repository := NewPathProofRepository(legacy)
	valid, err := pathproof.NewBuilder(repository, 1).Build(ctx, pathproof.BuildRequest{PlanID: fixture.plan.ID, SourceBlockHash: fixture.sourceBlockHash, ExpectedHomeTrustRoot: fixture.homeTrustRoot})
	if err != nil {
		t.Fatal(err)
	}

	variants := []struct {
		name   string
		mutate func(*pathproof.PathProof)
	}{
		{"zero hops", func(value *pathproof.PathProof) { value.Hops = nil; value.BlockHashes = nil; value.Witnesses = nil }},
		{"missing hop", func(value *pathproof.PathProof) {
			value.Hops = value.Hops[:1]
			value.BlockHashes = value.BlockHashes[:1]
			value.Witnesses = value.Witnesses[:1]
		}},
		{"wrong reverse order", func(value *pathproof.PathProof) {
			value.Hops[0], value.Hops[1] = value.Hops[1], value.Hops[0]
			value.BlockHashes[0], value.BlockHashes[1] = value.BlockHashes[1], value.BlockHashes[0]
			value.Witnesses[0], value.Witnesses[1] = value.Witnesses[1], value.Witnesses[0]
		}},
		{"wrong base TrustRoot", func(value *pathproof.PathProof) { value.BaseTrustRoot.Hash[0] ^= 1 }},
	}
	for _, test := range variants {
		t.Run(test.name, func(t *testing.T) {
			changed := valid.Clone()
			test.mutate(&changed)
			changed.ID = pathproof.ComputePathProofID(changed)
			insertRawParentV1PathProof(t, raw, changed)
			if _, err := repository.LoadPathProof(ctx, changed.ID); !errors.Is(err, pathproof.ErrProofMaterialMissing) {
				t.Fatalf("LoadPathProof invalid row error = %v", err)
			}
		})
	}
	extra := valid.Clone()
	extra.BaseTrustRoot.Hash[0] ^= 0x7f
	extra.ID = pathproof.ComputePathProofID(extra)
	insertRawParentV1PathProof(t, raw, extra)
	if _, err := raw.Exec(`INSERT INTO proof_hops(proof_id,plan_id,snapshot_id,hop_index,plan_hop_index,edge_id,to_node_id,block_hash,witness_id)
		VALUES(?,?,?,?,?,?,?,?,?)`, extra.ID[:], valid.PlanID[:], valid.SnapshotID[:], 2, 2, valid.Hops[0].EdgeID[:], valid.Hops[0].ToNodeID[:], valid.Hops[0].BlockHash[:], valid.Hops[0].WitnessID[:]); err == nil {
		t.Fatal("parent v1 schema accepted proof hop beyond plan hop count")
	}
}

func insertRawParentV1PathProof(t *testing.T, raw *sql.DB, value pathproof.PathProof) {
	t.Helper()
	if _, err := raw.Exec(`INSERT INTO proofs(proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at)
		VALUES(?,?,?,?,?,?)`, value.ID[:], value.RequestID[:], value.PlanID[:], value.SnapshotID[:], value.BaseTrustRoot.Hash[:], time.Now().UTC().UnixNano()); err != nil {
		t.Fatal(err)
	}
	for index, hop := range value.Hops {
		if _, err := raw.Exec(`INSERT INTO proof_hops(proof_id,plan_id,snapshot_id,hop_index,plan_hop_index,edge_id,to_node_id,block_hash,witness_id)
			VALUES(?,?,?,?,?,?,?,?,?)`, value.ID[:], value.PlanID[:], value.SnapshotID[:], index, hop.PlanHopIndex, hop.EdgeID[:], hop.ToNodeID[:], hop.BlockHash[:], hop.WitnessID[:]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPathProofRepositoryPropagatesCanceledQueries(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repository := NewPathProofRepository(db)
	_, err := repository.LoadPathProofMaterial(ctx, planner.PlanID{1}, trustview.SnapshotID{2}, 0, trustview.EdgeID{3})
	if !errors.Is(err, context.Canceled) || errors.Is(err, pathproof.ErrProofMaterialMissing) {
		t.Fatalf("LoadPathProofMaterial cancellation = %v", err)
	}
	_, err = repository.LoadPathProof(ctx, pathproof.PathProofID{1})
	if !errors.Is(err, context.Canceled) || errors.Is(err, pathproof.ErrPathProofNotFound) {
		t.Fatalf("LoadPathProof cancellation = %v", err)
	}
}

type persistedPathProofFixture struct {
	plan            planner.Plan
	sourceBlockHash common.Hash
	homeTrustRoot   trustview.TrustRoot
}

func persistPathProofGraph(t *testing.T, db *DB) persistedPathProofFixture {
	t.Helper()
	ctx := context.Background()
	chainA, _ := domain.NewChainID(101)
	chainB, _ := domain.NewChainID(102)
	chainC, _ := domain.NewChainID(103)
	height, _ := domain.NewBlockHeight(7)
	blockA, blockB, blockC := common.HexToHash("0xa01"), common.HexToHash("0xb01"), common.HexToHash("0xc01")
	rootA := trustview.TrustRoot{Hash: common.HexToHash("0xa11")}
	siblingBA, siblingCB := common.HexToHash("0xba55"), common.HexToHash("0xcb55")
	rootBHash, _ := internalproof.RootFromWitness(domain.LeafHash(rootA.Hash, blockA), 0, []common.Hash{siblingBA}, 1)
	rootCHash, _ := internalproof.RootFromWitness(domain.LeafHash(rootBHash, blockB), 1, []common.Hash{siblingCB}, 1)
	rootB, rootC := trustview.TrustRoot{Hash: rootBHash}, trustview.TrustRoot{Hash: rootCHash}
	request := testRequest(t)
	request.HomeChainID, request.SourceChainID, request.SourceHeight, request.SourceBlockHash = chainC, chainA, height, blockA
	request.ID, _ = evidence.ComputeGatewayRequestID(chainC, request.Gateway, request.Requester, new(big.Int).SetBytes(request.Nonce[:]), chainA, height, blockA)
	dependencyCB := trustview.NewVerifiedDependency(request.ID, chainB, height, blockB, rootB, 1, evidence.ID{})
	dependencyBA := trustview.NewVerifiedDependency(request.ID, chainA, height, blockA, rootA, 0, evidence.ID{})
	keyC := trustview.NodeKey{ChainID: chainC, Height: height, BlockHash: blockC}
	keyB := trustview.NodeKey{ChainID: chainB, Height: height, BlockHash: blockB}
	keyA := trustview.NodeKey{ChainID: chainA, Height: height, BlockHash: blockA}
	recordC := evidenceForNode(t, keyC, trustview.ComputeDependencyRecordedPayloadDigest(dependencyCB), 31)
	recordB := evidenceForNode(t, keyB, trustview.ComputeDependencyRecordedPayloadDigest(dependencyBA), 32)
	recordA := evidenceForNode(t, keyA, common.HexToHash("0xaa00"), 33)
	dependencyCB.EvidenceID, dependencyBA.EvidenceID = recordC.ID, recordB.ID
	evidenceRepository := NewEvidenceRepository(db)
	for _, record := range []evidence.Record{recordC, recordB, recordA} {
		if _, _, err := evidenceRepository.Observe(ctx, record); err != nil {
			t.Fatal(err)
		}
		activateEvidence(t, evidenceRepository, record.ID)
	}
	if _, _, err := NewRequestRepository(db).Observe(ctx, request); err != nil {
		t.Fatal(err)
	}
	nodeC := trustview.NewTrustNode(keyC, rootC, recordC.ID)
	nodeB := trustview.NewTrustNode(keyB, rootB, recordB.ID)
	nodeA := trustview.NewTrustNode(keyA, rootA, recordA.ID)
	edgeCB, _ := trustview.NewTrustEdge(nodeC.ID, nodeB.ID, recordC.ID, 1, nil, 100)
	edgeBA, _ := trustview.NewTrustEdge(nodeB.ID, nodeA.ID, recordB.ID, 0, nil, 100)
	viewRepository := NewTrustViewRepository(db)
	if _, err := viewRepository.MergeActiveTrustEdge(ctx, nodeC, nodeB, edgeCB, dependencyCB); err != nil {
		t.Fatal(err)
	}
	if _, err := viewRepository.MergeActiveTrustEdge(ctx, nodeB, nodeA, edgeBA, dependencyBA); err != nil {
		t.Fatal(err)
	}
	witnessCB := trustview.NewMembershipWitness(recordC.ID, 1, []common.Hash{siblingCB})
	witnessBA := trustview.NewMembershipWitness(recordB.ID, 0, []common.Hash{siblingBA})
	for _, witness := range []trustview.MembershipWitness{witnessCB, witnessBA} {
		if _, err := viewRepository.SaveMembershipWitness(ctx, witness); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := viewRepository.CreateTrustViewSnapshot(ctx, TrustViewSnapshotRequest{
		RequestID: request.ID, Attempt: 0, HomeChainID: chainC, ExpectedHomeTrustRoot: rootC,
		StartNodeID: nodeC.ID, TargetNodeID: nodeA.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.New(viewRepository, NewPlanRepository(db), calibratedRegistry(t, chainC, chainA)).Plan(ctx, planner.Request{ID: request.ID, Attempt: 0, SnapshotID: snapshot.ID, HomeChainID: chainC})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Type != planner.PathPlan {
		t.Fatalf("plan = %+v", plan)
	}
	return persistedPathProofFixture{plan: plan, sourceBlockHash: blockA, homeTrustRoot: rootC}
}
