package mapnode_test

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

func TestPhaseThreeDurableTrustViewPathProofCheckpoint(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "phase3-checkpoint.sqlite")
	database, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRepository := store.NewEvidenceRepository(database)
	requestRepository := store.NewRequestRepository(database)
	viewRepository := store.NewTrustViewRepository(database)
	planRepository := store.NewPlanRepository(database)
	proofRepository := store.NewPathProofRepository(database)

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

	missingRequest := checkpointRequest(t, chainC, chainA, height, blockA, 1)
	successRequest := checkpointRequest(t, chainC, chainA, height, blockA, 2)
	for _, request := range []coordinator.Request{missingRequest, successRequest} {
		if _, _, err := requestRepository.Observe(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	dependencyCB := trustview.NewVerifiedDependency(missingRequest.ID, chainB, height, blockB, rootB, 1, evidence.ID{})
	dependencyBA := trustview.NewVerifiedDependency(missingRequest.ID, chainA, height, blockA, rootA, 0, evidence.ID{})
	keyC := trustview.NodeKey{ChainID: chainC, Height: height, BlockHash: blockC}
	keyB := trustview.NodeKey{ChainID: chainB, Height: height, BlockHash: blockB}
	keyA := trustview.NodeKey{ChainID: chainA, Height: height, BlockHash: blockA}
	recordC := checkpointEvidence(t, keyC, trustview.ComputeDependencyRecordedPayloadDigest(dependencyCB), 1)
	recordB := checkpointEvidence(t, keyB, trustview.ComputeDependencyRecordedPayloadDigest(dependencyBA), 2)
	recordA := checkpointEvidence(t, keyA, common.HexToHash("0xaa01"), 3)
	dependencyCB.EvidenceID, dependencyBA.EvidenceID = recordC.ID, recordB.ID
	for _, record := range []evidence.Record{recordC, recordB, recordA} {
		if _, _, err := evidenceRepository.Observe(ctx, record); err != nil {
			t.Fatal(err)
		}
		checkpointActivateEvidence(t, evidenceRepository, record.ID)
	}
	nodeC := trustview.NewTrustNode(keyC, rootC, recordC.ID)
	nodeB := trustview.NewTrustNode(keyB, rootB, recordB.ID)
	nodeA := trustview.NewTrustNode(keyA, rootA, recordA.ID)
	edgeCB, _ := trustview.NewTrustEdge(nodeC.ID, nodeB.ID, recordC.ID, 1, nil, 100)
	edgeBA, _ := trustview.NewTrustEdge(nodeB.ID, nodeA.ID, recordB.ID, 0, nil, 100)
	if _, err := viewRepository.MergeActiveTrustEdge(ctx, nodeC, nodeB, edgeCB, dependencyCB); err != nil {
		t.Fatal(err)
	}
	if _, err := viewRepository.MergeActiveTrustEdge(ctx, nodeB, nodeA, edgeBA, dependencyBA); err != nil {
		t.Fatal(err)
	}

	inactiveDependency := trustview.NewVerifiedDependency(missingRequest.ID, chainA, height, blockA, rootA, 2, evidence.ID{})
	inactiveRecord := checkpointEvidence(t, keyC, trustview.ComputeDependencyRecordedPayloadDigest(inactiveDependency), 4)
	inactiveDependency.EvidenceID = inactiveRecord.ID
	if _, _, err := evidenceRepository.Observe(ctx, inactiveRecord); err != nil {
		t.Fatal(err)
	}
	inactiveEdge, _ := trustview.NewTrustEdge(nodeC.ID, nodeA.ID, inactiveRecord.ID, 2, nil, 100)
	if _, err := viewRepository.MergeActiveTrustEdge(ctx, nodeC, nodeA, inactiveEdge, inactiveDependency); !errors.Is(err, store.ErrInactiveEvidence) {
		t.Fatalf("inactive evidence entered TrustView: %v", err)
	}

	calibrated := checkpointRegistry(t, chainC)
	missingSnapshot, err := viewRepository.CreateTrustViewSnapshot(ctx, store.TrustViewSnapshotRequest{RequestID: missingRequest.ID, Attempt: 0, HomeChainID: chainC, ExpectedHomeTrustRoot: rootC, StartNodeID: nodeC.ID, TargetNodeID: nodeA.ID})
	if err != nil {
		t.Fatal(err)
	}
	missingPlan, err := planner.New(viewRepository, planRepository, calibrated).Plan(ctx, planner.Request{ID: missingRequest.ID, Attempt: 0, SnapshotID: missingSnapshot.ID, HomeChainID: chainC})
	if err != nil || missingPlan.Type != planner.DirectPlan || missingPlan.FallbackReason != planner.ProofMaterialMissing {
		t.Fatalf("missing witness fallback = %+v, %v", missingPlan, err)
	}

	witnessCB := trustview.NewMembershipWitness(recordC.ID, 1, []common.Hash{siblingCB})
	witnessBA := trustview.NewMembershipWitness(recordB.ID, 0, []common.Hash{siblingBA})
	for _, witness := range []trustview.MembershipWitness{witnessCB, witnessBA} {
		if _, err := viewRepository.SaveMembershipWitness(ctx, witness); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := viewRepository.CreateTrustViewSnapshot(ctx, store.TrustViewSnapshotRequest{RequestID: successRequest.ID, Attempt: 0, HomeChainID: chainC, ExpectedHomeTrustRoot: rootC, StartNodeID: nodeC.ID, TargetNodeID: nodeA.ID})
	if err != nil {
		t.Fatal(err)
	}
	planning := planner.New(viewRepository, planRepository, calibrated)
	builder := pathproof.NewBuilder(proofRepository, 1)
	coordination := coordinator.New(requestRepository, viewRepository, planning, planRepository, builder)
	result, err := coordination.Process(ctx, coordinator.Work{Request: successRequest, Attempt: 0, SnapshotID: snapshot.ID, ExpectedHomeTrustRoot: rootC})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.State != coordinator.ProofReady || result.Plan.Type != planner.PathPlan || len(result.Plan.Hops) != 2 {
		t.Fatalf("C->B->A coordination = %+v", result)
	}
	if len(result.PathProof.BlockHashes) != 2 || result.PathProof.BlockHashes[0] != blockA || result.PathProof.BlockHashes[1] != blockB || result.PathProof.Hops[0].EdgeID != edgeBA.ID || result.PathProof.Hops[1].EdgeID != edgeCB.ID {
		t.Fatalf("PathProof Solidity order = %+v", result.PathProof)
	}
	if err := internalproof.VerifyPath(result.PathProof.BaseTrustRoot.Hash, result.PathProof.BlockHashes, result.PathProof.Witnesses, rootC.Hash, 1); err != nil {
		t.Fatalf("PathProof did not close to C TrustRoot: %v", err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reopenedRequest, err := store.NewRequestRepository(database).Load(ctx, successRequest.ID)
	if err != nil || reopenedRequest.State != coordinator.ProofReady {
		t.Fatalf("reopened request = %+v, %v", reopenedRequest, err)
	}
	reopenedPlan, err := store.NewPlanRepository(database).LoadCurrentPlan(ctx, successRequest.ID)
	if err != nil || reopenedPlan.ID != result.Plan.ID {
		t.Fatalf("reopened plan = %+v, %v", reopenedPlan, err)
	}
	reopenedProof, err := store.NewPathProofRepository(database).LoadPathProofForPlan(ctx, reopenedPlan.ID)
	if err != nil || reopenedProof.ID != result.PathProof.ID || pathproof.ComputePathProofID(reopenedProof) != reopenedProof.ID {
		t.Fatalf("reopened PathProof = %+v, %v", reopenedProof, err)
	}
	reopenedSnapshot, err := store.NewTrustViewRepository(database).LoadTrustViewSnapshot(ctx, snapshot.ID)
	if err != nil || len(reopenedSnapshot.Edges) != 2 {
		t.Fatalf("reopened TrustViewSnapshot = %+v, %v", reopenedSnapshot, err)
	}
}

func TestPhaseThreeCheckpointPersistsCostBoundDirectFallback(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(filepath.Join(t.TempDir(), "cost-fallback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	chainA, _ := domain.NewChainID(201)
	chainC, _ := domain.NewChainID(203)
	height, _ := domain.NewBlockHeight(9)
	blockA, blockC := common.HexToHash("0x2a01"), common.HexToHash("0x2c01")
	rootA, rootC := trustview.TrustRoot{Hash: common.HexToHash("0x2a11")}, trustview.TrustRoot{Hash: common.HexToHash("0x2c11")}
	request := checkpointRequest(t, chainC, chainA, height, blockA, 9)
	requestRepository := store.NewRequestRepository(database)
	if _, _, err := requestRepository.Observe(ctx, request); err != nil {
		t.Fatal(err)
	}
	dependency := trustview.NewVerifiedDependency(request.ID, chainA, height, blockA, rootA, 0, evidence.ID{})
	keyC := trustview.NodeKey{ChainID: chainC, Height: height, BlockHash: blockC}
	keyA := trustview.NodeKey{ChainID: chainA, Height: height, BlockHash: blockA}
	recordC := checkpointEvidence(t, keyC, trustview.ComputeDependencyRecordedPayloadDigest(dependency), 41)
	recordA := checkpointEvidence(t, keyA, common.HexToHash("0x2aaa"), 42)
	dependency.EvidenceID = recordC.ID
	evidenceRepository := store.NewEvidenceRepository(database)
	for _, record := range []evidence.Record{recordC, recordA} {
		if _, _, err := evidenceRepository.Observe(ctx, record); err != nil {
			t.Fatal(err)
		}
		checkpointActivateEvidence(t, evidenceRepository, record.ID)
	}
	nodeC := trustview.NewTrustNode(keyC, rootC, recordC.ID)
	nodeA := trustview.NewTrustNode(keyA, rootA, recordA.ID)
	edge, _ := trustview.NewTrustEdge(nodeC.ID, nodeA.ID, recordC.ID, 0, nil, 3_000_097)
	viewRepository := store.NewTrustViewRepository(database)
	if _, err := viewRepository.MergeActiveTrustEdge(ctx, nodeC, nodeA, edge, dependency); err != nil {
		t.Fatal(err)
	}
	snapshot, err := viewRepository.CreateTrustViewSnapshot(ctx, store.TrustViewSnapshotRequest{RequestID: request.ID, Attempt: 0, HomeChainID: chainC, ExpectedHomeTrustRoot: rootC, StartNodeID: nodeC.ID, TargetNodeID: nodeA.ID})
	if err != nil {
		t.Fatal(err)
	}
	planRepository := store.NewPlanRepository(database)
	decision, err := planner.New(viewRepository, planRepository, checkpointRegistry(t, chainC)).Plan(ctx, planner.Request{ID: request.ID, Attempt: 0, SnapshotID: snapshot.ID, HomeChainID: chainC})
	if err != nil || decision.Type != planner.DirectPlan || decision.FallbackReason != planner.PathCostExceedsDirect {
		t.Fatalf("cost-bound decision = %+v, %v", decision, err)
	}
	persisted, err := planRepository.LoadCurrentPlan(ctx, request.ID)
	if err != nil || persisted.ID != decision.ID || persisted.FallbackReason != planner.PathCostExceedsDirect {
		t.Fatalf("persisted cost fallback = %+v, %v", persisted, err)
	}
}

func checkpointRequest(t *testing.T, home, source domain.ChainID, height domain.BlockHeight, blockHash common.Hash, nonceByte byte) coordinator.Request {
	t.Helper()
	request := coordinator.Request{HomeChainID: home, Gateway: common.HexToAddress("0x1111111111111111111111111111111111111111"), Requester: common.HexToAddress("0x2222222222222222222222222222222222222222"), SourceChainID: source, SourceHeight: height, SourceBlockHash: blockHash, State: coordinator.Observed}
	request.Nonce[31] = nonceByte
	id, err := evidence.ComputeGatewayRequestID(home, request.Gateway, request.Requester, new(big.Int).SetBytes(request.Nonce[:]), source, height, blockHash)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = id
	return request
}

func checkpointEvidence(t *testing.T, key trustview.NodeKey, payload common.Hash, tag byte) evidence.Record {
	t.Helper()
	record := evidence.Record{Locator: evidence.Locator{ChainID: key.ChainID, ContractAddress: common.HexToAddress("0x3333333333333333333333333333333333333333"), BlockNumber: key.Height, BlockHash: key.BlockHash, TxHash: common.BytesToHash([]byte{tag}), TxIndex: uint32(tag), LogIndex: uint32(tag), PayloadDigest: payload}, State: evidence.Candidate}
	id, err := evidence.ComputeID(record.Locator)
	if err != nil {
		t.Fatal(err)
	}
	record.ID = id
	return record
}

func checkpointActivateEvidence(t *testing.T, repository *store.EvidenceRepository, id evidence.ID) {
	t.Helper()
	for _, step := range []struct{ from, to evidence.State }{{evidence.Candidate, evidence.Verified}, {evidence.Verified, evidence.Confirmed}, {evidence.Confirmed, evidence.Active}} {
		if _, _, err := repository.Transition(context.Background(), id, step.from, step.to, evidence.TransitionMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
}

func checkpointRegistry(t *testing.T, home domain.ChainID) *registry.Registry {
	t.Helper()
	parameters := registry.DirectVerifierParameters{ID: "pow-spv-3m", AuthorizedSigners: []common.Address{common.HexToAddress("0x1000000000000000000000000000000000000001"), common.HexToAddress("0x2000000000000000000000000000000000000002"), common.HexToAddress("0x3000000000000000000000000000000000000003")}, SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: 3_000_096}
	profile := registry.DeploymentProfile{Configured: parameters, Deployed: parameters, Fingerprint: registry.ComputeProfileFingerprint(parameters)}
	result, err := registry.New([]registry.Chain{{Name: "chain-c", ChainID: home, MapNodeID: "mapnode-c", Home: true, SignerAuthority: true, TransactionAuthority: true, Profile: profile}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
