package store

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

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
