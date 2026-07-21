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
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestTrustViewRepositoryActiveEvidenceSnapshotsAndPlanSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trustview.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRepo := NewEvidenceRepository(db)
	records := []evidence.Record{taggedEvidence(t, 1), taggedEvidence(t, 2), taggedEvidence(t, 3)}
	for _, record := range records {
		if _, _, err := evidenceRepo.Observe(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	chainC, _ := domain.NewChainID(103)
	chainB, _ := domain.NewChainID(102)
	chainA, _ := domain.NewChainID(101)
	height, _ := domain.NewBlockHeight(7)
	node := func(chain domain.ChainID, block, root string, record evidence.Record) trustview.TrustNode {
		return trustview.NewTrustNode(trustview.NodeKey{ChainID: chain, Height: height, BlockHash: common.HexToHash(block)}, trustview.TrustRoot{Hash: common.HexToHash(root)}, record.ID)
	}
	c := node(chainC, "0xc1", "0xcc", records[0])
	b := node(chainB, "0xb1", "0xbb", records[1])
	a := node(chainA, "0xa1", "0xaa", records[2])
	edgeCB, _ := trustview.NewTrustEdge(c.ID, b.ID, records[0].ID, nil, 100)
	edgeBA, _ := trustview.NewTrustEdge(b.ID, a.ID, records[1].ID, nil, 100)
	graph := NewTrustViewRepository(db)
	if _, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB); !errors.Is(err, ErrInactiveEvidence) {
		t.Fatalf("candidate evidence merge error = %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO trust_nodes(node_id,chain_id,block_height,block_hash,trust_root,evidence_id,evidence_state,first_observed_at)
		VALUES(?,?,?,?,?,?,?,1)`, c.ID[:], c.Key.ChainID[:], c.Key.Height[:], c.Key.BlockHash[:], c.Root.Hash[:], c.EvidenceID[:], evidence.Candidate); err == nil {
		t.Fatal("database boundary accepted a candidate-evidence TrustView node")
	}
	for _, record := range records {
		activateEvidence(t, evidenceRepo, record.ID)
	}
	if changed, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB); err != nil || !changed {
		t.Fatalf("merge C->B = %t, %v", changed, err)
	}
	if changed, err := graph.MergeActiveTrustEdge(ctx, b, a, edgeBA); err != nil || !changed {
		t.Fatalf("merge B->A = %t, %v", changed, err)
	}
	if changed, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB); err != nil || changed {
		t.Fatalf("idempotent merge = %t, %v", changed, err)
	}
	conflictingC := c
	conflictingC.Root = trustview.TrustRoot{Hash: common.HexToHash("0xdead")}
	if _, err := graph.MergeActiveTrustEdge(ctx, conflictingC, b, edgeCB); !errors.Is(err, trustview.ErrTrustRootConflict) {
		t.Fatalf("TrustRoot conflict error = %v", err)
	}

	request := testRequest(t)
	request.HomeChainID = chainC
	request.SourceChainID = chainA
	request.SourceHeight = height
	request.SourceBlockHash = a.Key.BlockHash
	request.ID, err = evidence.ComputeGatewayRequestID(request.HomeChainID, request.Gateway, request.Requester, new(big.Int).SetBytes(request.Nonce[:]), request.SourceChainID, request.SourceHeight, request.SourceBlockHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRequestRepository(db).Observe(ctx, request); err != nil {
		t.Fatal(err)
	}
	snapshotRequest := TrustViewSnapshotRequest{RequestID: request.ID, Attempt: 0, HomeChainID: chainC, ExpectedHomeTrustRoot: c.Root, StartNodeID: c.ID, TargetNodeID: a.ID}
	invalidEndpoints := snapshotRequest
	invalidEndpoints.TargetNodeID = invalidEndpoints.StartNodeID
	if _, err := graph.CreateTrustViewSnapshot(ctx, invalidEndpoints); err == nil {
		t.Fatal("snapshot accepted identical start and target")
	}
	oldSnapshot, err := graph.CreateTrustViewSnapshot(ctx, snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !oldSnapshot.Sealed || len(oldSnapshot.Edges) != 2 {
		t.Fatalf("old snapshot = %+v", oldSnapshot)
	}
	for _, edge := range oldSnapshot.Edges {
		if edge.WitnessID != nil {
			t.Fatal("snapshot unexpectedly had witness")
		}
	}

	witnessCB := trustview.NewMembershipWitness(records[0].ID, 0, []common.Hash{common.HexToHash("0x101")})
	witnessBA := trustview.NewMembershipWitness(records[1].ID, 1, []common.Hash{common.HexToHash("0x102")})
	for _, witness := range []trustview.MembershipWitness{witnessCB, witnessBA} {
		if inserted, err := graph.SaveMembershipWitness(ctx, witness); err != nil || !inserted {
			t.Fatalf("SaveMembershipWitness = %t, %v", inserted, err)
		}
	}
	loadedOld, err := graph.LoadTrustViewSnapshot(ctx, oldSnapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range loadedOld.Edges {
		if edge.WitnessID != nil {
			t.Fatal("old snapshot changed after witness insertion")
		}
	}
	snapshotRequest.Attempt = 1
	newSnapshot, err := graph.CreateTrustViewSnapshot(ctx, snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	if newSnapshot.ID == oldSnapshot.ID || newSnapshot.Revision <= oldSnapshot.Revision {
		t.Fatal("new graph revision did not produce a new snapshot")
	}
	for _, edge := range newSnapshot.Edges {
		if edge.WitnessID == nil {
			t.Fatal("new snapshot omitted membership witness")
		}
	}

	reg := calibratedRegistry(t, chainC, chainA)
	planRepo := NewPlanRepository(db)
	selected, err := planner.New(graph, planRepo, reg).Plan(ctx, planner.Request{ID: request.ID, Attempt: 1, SnapshotID: newSnapshot.ID, HomeChainID: chainC})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Type != planner.PathPlan || len(selected.Hops) != 2 || selected.PathCost == nil || *selected.PathCost != 200 {
		t.Fatalf("selected plan = %+v", selected)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reloadedSnapshot, err := NewTrustViewRepository(db).LoadTrustViewSnapshot(ctx, newSnapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	reloadedPlan, err := NewPlanRepository(db).LoadPlan(ctx, selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedSnapshot.ID != newSnapshot.ID || reloadedSnapshot.Revision != newSnapshot.Revision || len(reloadedSnapshot.Edges) != 2 {
		t.Fatalf("reloaded snapshot = %+v", reloadedSnapshot)
	}
	if reloadedPlan.ID != selected.ID || len(reloadedPlan.Hops) != 2 || reloadedPlan.Hops[0] != selected.Hops[0] || reloadedPlan.Hops[1] != selected.Hops[1] {
		t.Fatalf("reloaded plan = %+v", reloadedPlan)
	}
}

func taggedEvidence(t *testing.T, tag byte) evidence.Record {
	t.Helper()
	record := testEvidenceRecord(t, false)
	record.Locator.PayloadDigest = common.BytesToHash([]byte{tag})
	record.Locator.BlockHash = common.BytesToHash([]byte{tag, 1})
	record.Locator.TxHash = common.BytesToHash([]byte{tag, 2})
	record.ID, _ = evidence.ComputeID(record.Locator)
	return record
}

func activateEvidence(t *testing.T, repo *EvidenceRepository, id evidence.ID) {
	t.Helper()
	ctx := context.Background()
	for _, step := range []struct{ from, to evidence.State }{{evidence.Candidate, evidence.Verified}, {evidence.Verified, evidence.Confirmed}, {evidence.Confirmed, evidence.Active}} {
		if _, _, err := repo.Transition(ctx, id, step.from, step.to, evidence.TransitionMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
}

func calibratedRegistry(t *testing.T, homeID, remoteID domain.ChainID) *registry.Registry {
	t.Helper()
	signers := []common.Address{common.HexToAddress("0x1000000000000000000000000000000000000001"), common.HexToAddress("0x2000000000000000000000000000000000000002"), common.HexToAddress("0x3000000000000000000000000000000000000003")}
	parameters := registry.DirectVerifierParameters{ID: "pow-spv-3m", AuthorizedSigners: signers, SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: 3_000_096}
	profile := registry.DeploymentProfile{Configured: parameters, Deployed: parameters, Fingerprint: registry.ComputeProfileFingerprint(parameters)}
	result, err := registry.New([]registry.Chain{{Name: "home", ChainID: homeID, MapNodeID: "home", Home: true, SignerAuthority: true, TransactionAuthority: true, Profile: profile}, {Name: "remote", ChainID: remoteID, MapNodeID: "remote", Profile: profile}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
