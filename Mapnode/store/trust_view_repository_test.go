package store

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"sync"
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
	chainC, _ := domain.NewChainID(103)
	chainB, _ := domain.NewChainID(102)
	chainA, _ := domain.NewChainID(101)
	height, _ := domain.NewBlockHeight(7)
	keyC := trustview.NodeKey{ChainID: chainC, Height: height, BlockHash: common.HexToHash("0xc1")}
	keyB := trustview.NodeKey{ChainID: chainB, Height: height, BlockHash: common.HexToHash("0xb1")}
	keyA := trustview.NodeKey{ChainID: chainA, Height: height, BlockHash: common.HexToHash("0xa1")}
	rootC := trustview.TrustRoot{Hash: common.HexToHash("0xcc")}
	rootB := trustview.TrustRoot{Hash: common.HexToHash("0xbb")}
	rootA := trustview.TrustRoot{Hash: common.HexToHash("0xaa")}
	request := testRequest(t)
	request.HomeChainID, request.SourceChainID, request.SourceHeight, request.SourceBlockHash = chainC, chainA, height, keyA.BlockHash
	request.ID, err = evidence.ComputeGatewayRequestID(request.HomeChainID, request.Gateway, request.Requester, new(big.Int).SetBytes(request.Nonce[:]), request.SourceChainID, request.SourceHeight, request.SourceBlockHash)
	if err != nil {
		t.Fatal(err)
	}
	dependencyCB := trustview.NewVerifiedDependency(request.ID, keyB.ChainID, keyB.Height, keyB.BlockHash, rootB, 0, evidence.ID{})
	dependencyBA := trustview.NewVerifiedDependency(request.ID, keyA.ChainID, keyA.Height, keyA.BlockHash, rootA, 1, evidence.ID{})
	records := []evidence.Record{
		evidenceForNode(t, keyC, trustview.ComputeDependencyRecordedPayloadDigest(dependencyCB), 1),
		evidenceForNode(t, keyB, trustview.ComputeDependencyRecordedPayloadDigest(dependencyBA), 2),
		evidenceForNode(t, keyA, common.HexToHash("0xaabb"), 3),
	}
	dependencyCB.EvidenceID, dependencyBA.EvidenceID = records[0].ID, records[1].ID
	c := trustview.NewTrustNode(keyC, rootC, records[0].ID)
	b := trustview.NewTrustNode(keyB, rootB, records[1].ID)
	a := trustview.NewTrustNode(keyA, rootA, records[2].ID)
	for _, record := range records {
		if _, _, err := evidenceRepo.Observe(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := NewRequestRepository(db).Observe(ctx, request); err != nil {
		t.Fatal(err)
	}
	edgeCB, _ := trustview.NewTrustEdge(c.ID, b.ID, records[0].ID, nil, 100)
	edgeBA, _ := trustview.NewTrustEdge(b.ID, a.ID, records[1].ID, nil, 100)
	graph := NewTrustViewRepository(db)
	if _, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB, dependencyCB); !errors.Is(err, ErrInactiveEvidence) {
		t.Fatalf("candidate evidence merge error = %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO trust_nodes(node_id,chain_id,block_height,block_hash,trust_root,evidence_id,evidence_state,first_observed_at)
		VALUES(?,?,?,?,?,?,?,1)`, c.ID[:], c.Key.ChainID[:], c.Key.Height[:], c.Key.BlockHash[:], c.Root.Hash[:], c.EvidenceID[:], evidence.Candidate); err == nil {
		t.Fatal("database boundary accepted a candidate-evidence TrustView node")
	}
	for _, record := range records {
		activateEvidence(t, evidenceRepo, record.ID)
	}
	wrongDependencies := []trustview.VerifiedDependency{dependencyCB, dependencyCB, dependencyCB, dependencyCB, dependencyCB}
	wrongDependencies[0].DependencyKey[0] ^= 1
	wrongDependencies[1].SourceChainID = chainA
	wrongDependencies[2].SourceTrustRoot.Hash[0] ^= 1
	wrongDependencies[3].LeafIndex++
	wrongDependencies[4].RequestID[0] ^= 1
	for index, wrong := range wrongDependencies {
		if _, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB, wrong); !errors.Is(err, ErrEvidenceBinding) {
			t.Fatalf("wrong DependencyRecorded[%d] error = %v", index, err)
		}
	}
	unrelatedFrom := c
	unrelatedFrom.EvidenceID = records[1].ID
	if _, err := graph.MergeActiveTrustEdge(ctx, unrelatedFrom, b, edgeCB, dependencyCB); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("unrelated from evidence error = %v", err)
	}
	unrelatedTo := b
	unrelatedTo.EvidenceID = records[2].ID
	if _, err := graph.MergeActiveTrustEdge(ctx, c, unrelatedTo, edgeCB, dependencyCB); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("unrelated to evidence error = %v", err)
	}
	unrelatedEdge, _ := trustview.NewTrustEdge(c.ID, b.ID, records[2].ID, nil, 100)
	unrelatedDependency := dependencyCB
	unrelatedDependency.EvidenceID = records[2].ID
	if _, err := graph.MergeActiveTrustEdge(ctx, c, b, unrelatedEdge, unrelatedDependency); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("unrelated edge evidence error = %v", err)
	}
	opaqueEdgeEvidence := evidenceForNode(t, keyC, common.HexToHash("0xdeadbeef"), 9)
	if _, _, err := evidenceRepo.Observe(ctx, opaqueEdgeEvidence); err != nil {
		t.Fatal(err)
	}
	activateEvidence(t, evidenceRepo, opaqueEdgeEvidence.ID)
	opaqueEdge, _ := trustview.NewTrustEdge(c.ID, b.ID, opaqueEdgeEvidence.ID, nil, 100)
	opaqueDependency := dependencyCB
	opaqueDependency.EvidenceID = opaqueEdgeEvidence.ID
	if _, err := graph.MergeActiveTrustEdge(ctx, c, b, opaqueEdge, opaqueDependency); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("opaque same-block edge evidence error = %v", err)
	}
	if changed, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB, dependencyCB); err != nil || !changed {
		t.Fatalf("merge C->B = %t, %v", changed, err)
	}
	if changed, err := graph.MergeActiveTrustEdge(ctx, b, a, edgeBA, dependencyBA); err != nil || !changed {
		t.Fatalf("merge B->A = %t, %v", changed, err)
	}
	if changed, err := graph.MergeActiveTrustEdge(ctx, c, b, edgeCB, dependencyCB); err != nil || changed {
		t.Fatalf("idempotent merge = %t, %v", changed, err)
	}
	conflictingC := c
	conflictingC.Root = trustview.TrustRoot{Hash: common.HexToHash("0xdead")}
	if _, err := graph.MergeActiveTrustEdge(ctx, conflictingC, b, edgeCB, dependencyCB); !errors.Is(err, trustview.ErrTrustRootConflict) {
		t.Fatalf("TrustRoot conflict error = %v", err)
	}
	secondDependency := trustview.NewVerifiedDependency(domain.RequestID(common.HexToHash("0x9876")), b.Key.ChainID, b.Key.Height, b.Key.BlockHash, b.Root, 2, evidence.ID{})
	secondEdgeEvidence := evidenceForNode(t, keyC, trustview.ComputeDependencyRecordedPayloadDigest(secondDependency), 4)
	secondDependency.EvidenceID = secondEdgeEvidence.ID
	if _, _, err := evidenceRepo.Observe(ctx, secondEdgeEvidence); err != nil {
		t.Fatal(err)
	}
	activateEvidence(t, evidenceRepo, secondEdgeEvidence.ID)
	secondEdge, _ := trustview.NewTrustEdge(c.ID, b.ID, secondEdgeEvidence.ID, nil, 100)
	if changed, err := graph.MergeActiveTrustEdge(ctx, c, b, secondEdge, secondDependency); err != nil || !changed {
		t.Fatalf("second DependencyRecorded in same block = %t, %v", changed, err)
	}

	snapshotRequest := TrustViewSnapshotRequest{RequestID: request.ID, Attempt: 0, HomeChainID: chainC, ExpectedHomeTrustRoot: c.Root, StartNodeID: c.ID, TargetNodeID: a.ID}
	invalidEndpoints := snapshotRequest
	invalidEndpoints.TargetNodeID = invalidEndpoints.StartNodeID
	if _, err := graph.CreateTrustViewSnapshot(ctx, invalidEndpoints); err == nil {
		t.Fatal("snapshot accepted identical start and target")
	}
	wrongRequest := snapshotRequest
	wrongRequest.RequestID = domain.RequestID(common.HexToHash("0xffff"))
	if _, err := graph.CreateTrustViewSnapshot(ctx, wrongRequest); err == nil {
		t.Fatal("snapshot accepted an unpersisted request ID")
	}
	wrongTarget := snapshotRequest
	wrongTarget.TargetNodeID = b.ID
	if _, err := graph.CreateTrustViewSnapshot(ctx, wrongTarget); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("snapshot target tuple error = %v", err)
	}
	wrongHome := snapshotRequest
	wrongHome.HomeChainID = chainB
	if _, err := graph.CreateTrustViewSnapshot(ctx, wrongHome); err == nil {
		t.Fatal("snapshot accepted request/home mismatch")
	}
	wrongStart := snapshotRequest
	wrongStart.StartNodeID = b.ID
	wrongStart.ExpectedHomeTrustRoot = b.Root
	if _, err := graph.CreateTrustViewSnapshot(ctx, wrongStart); err == nil {
		t.Fatal("snapshot accepted non-home start chain")
	}
	oldSnapshot, err := graph.CreateTrustViewSnapshot(ctx, snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !oldSnapshot.Sealed || len(oldSnapshot.Edges) != 3 {
		t.Fatalf("old snapshot = %+v", oldSnapshot)
	}
	for _, edge := range oldSnapshot.Edges {
		if edge.WitnessID != nil {
			t.Fatal("snapshot unexpectedly had witness")
		}
	}

	witnessCB := trustview.NewMembershipWitness(records[0].ID, 0, []common.Hash{common.HexToHash("0x101")})
	witnessBA := trustview.NewMembershipWitness(records[1].ID, 1, []common.Hash{common.HexToHash("0x102")})
	witnessSecond := trustview.NewMembershipWitness(secondEdgeEvidence.ID, 2, []common.Hash{common.HexToHash("0x103")})
	for _, witness := range []trustview.MembershipWitness{witnessCB, witnessBA, witnessSecond} {
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
	conflictingAttempt := selected.Clone()
	conflictingAttempt.ID[0] ^= 1
	if err := planRepo.SavePlan(ctx, conflictingAttempt); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("same attempt different PlanID error = %v", err)
	}
	makeLaterPlan := func(attempt uint64) (planner.Plan, trustview.TrustViewSnapshot) {
		snapshotRequest.Attempt = attempt
		snapshot, createErr := graph.CreateTrustViewSnapshot(ctx, snapshotRequest)
		if createErr != nil {
			t.Fatal(createErr)
		}
		plan := selected.Clone()
		plan.ID = planner.PlanID(common.BigToHash(new(big.Int).SetUint64(0x9000 + attempt)))
		plan.Attempt = attempt
		plan.SnapshotID = snapshot.ID
		return plan, snapshot
	}
	plan2, _ := makeLaterPlan(2)
	if err := planRepo.SavePlan(ctx, plan2); err != nil {
		t.Fatal(err)
	}
	if err := planRepo.SavePlan(ctx, selected); !errors.Is(err, ErrStalePlanningAttempt) {
		t.Fatalf("attempt 1 after attempt 2 error = %v", err)
	}
	plan3, _ := makeLaterPlan(3)
	plan4, _ := makeLaterPlan(4)
	var wait sync.WaitGroup
	type saveResult struct {
		attempt uint64
		err     error
	}
	results := make(chan saveResult, 2)
	for _, candidate := range []planner.Plan{plan4, plan3} {
		wait.Add(1)
		go func(plan planner.Plan) {
			defer wait.Done()
			results <- saveResult{attempt: plan.Attempt, err: planRepo.SavePlan(ctx, plan)}
		}(candidate)
	}
	wait.Wait()
	close(results)
	for result := range results {
		if result.attempt == 4 && result.err != nil {
			t.Fatalf("highest concurrent attempt error = %v", result.err)
		}
		if result.err != nil && !errors.Is(result.err, ErrStalePlanningAttempt) {
			t.Fatalf("concurrent out-of-order SavePlan(%d) error = %v", result.attempt, result.err)
		}
	}
	current, err := planRepo.LoadCurrentPlan(ctx, request.ID)
	if err != nil || current.Attempt != 4 || current.ID != plan4.ID {
		t.Fatalf("current plan = %+v, %v", current, err)
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
	if reloadedSnapshot.ID != newSnapshot.ID || reloadedSnapshot.Revision != newSnapshot.Revision || len(reloadedSnapshot.Edges) != 3 {
		t.Fatalf("reloaded snapshot = %+v", reloadedSnapshot)
	}
	if reloadedPlan.ID != selected.ID || len(reloadedPlan.Hops) != 2 || reloadedPlan.Hops[0] != selected.Hops[0] || reloadedPlan.Hops[1] != selected.Hops[1] {
		t.Fatalf("reloaded plan = %+v", reloadedPlan)
	}
	reloadedCurrent, err := NewPlanRepository(db).LoadCurrentPlan(ctx, request.ID)
	if err != nil || reloadedCurrent.Attempt != 4 || reloadedCurrent.ID != plan4.ID {
		t.Fatalf("reopened current plan = %+v, %v", reloadedCurrent, err)
	}
}

func evidenceForNode(t *testing.T, key trustview.NodeKey, payload common.Hash, tag byte) evidence.Record {
	t.Helper()
	record := testEvidenceRecord(t, false)
	record.Locator.ChainID, record.Locator.BlockNumber, record.Locator.BlockHash = key.ChainID, key.Height, key.BlockHash
	record.Locator.PayloadDigest = payload
	record.Locator.TxHash = common.BytesToHash([]byte{tag, 2})
	record.Locator.TxIndex, record.Locator.LogIndex = uint32(tag), uint32(tag)
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
