package proof

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

func TestPathProofBuilderReversesTrustViewPathIntoSolidityOrder(t *testing.T) {
	fixture := newPathProofFixture(t)
	builder := NewBuilder(fixture.repository, 1)
	got, err := builder.Build(context.Background(), BuildRequest{
		PlanID: fixture.plan.ID, SourceBlockHash: fixture.nodeA.Key.BlockHash,
		ExpectedHomeTrustRoot: fixture.snapshot.HomeTrustRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseTrustRoot != fixture.nodeA.Root {
		t.Fatalf("BaseTrustRoot = %x, want source A TrustRoot %x", got.BaseTrustRoot.Hash, fixture.nodeA.Root.Hash)
	}
	if len(got.BlockHashes) != 2 || got.BlockHashes[0] != fixture.nodeA.Key.BlockHash || got.BlockHashes[1] != fixture.nodeB.Key.BlockHash {
		t.Fatalf("BlockHashes = %x, want [A,B]", got.BlockHashes)
	}
	if len(got.Witnesses) != 2 || got.Witnesses[0].LeafIndex() != fixture.witnessBA.LeafIndex || got.Witnesses[1].LeafIndex() != fixture.witnessCB.LeafIndex {
		t.Fatalf("Witnesses are not [wBA,wCB]: %+v", got.Witnesses)
	}
	if len(got.Hops) != 2 || got.Hops[0].PlanHopIndex != 1 || got.Hops[1].PlanHopIndex != 0 || got.Hops[0].EdgeID != fixture.edgeBA.ID || got.Hops[1].EdgeID != fixture.edgeCB.ID {
		t.Fatalf("PathProof hops do not retain reverse plan binding: %+v", got.Hops)
	}
	if err := internalproof.VerifyPath(got.BaseTrustRoot.Hash, got.BlockHashes, got.Witnesses, fixture.snapshot.HomeTrustRoot.Hash, 1); err != nil {
		t.Fatalf("built PathProof does not close to Home TrustRoot: %v", err)
	}
	if got.ID != ComputePathProofID(got) {
		t.Fatal("PathProof ID is not content-derived")
	}
	for index, mutate := range []func(*PathProof){
		func(value *PathProof) { value.BaseTrustRoot.Hash[0] ^= 1 },
		func(value *PathProof) { value.BlockHashes[0][0] ^= 1 },
		func(value *PathProof) {
			siblings := value.Witnesses[0].Siblings()
			siblings[0][0] ^= 1
			value.Witnesses[0], _ = domain.NewMembershipWitness(value.Witnesses[0].LeafIndex(), siblings)
		},
		func(value *PathProof) { value.Hops[0].EdgeID[0] ^= 1 },
	} {
		changed := got.Clone()
		mutate(&changed)
		if ComputePathProofID(changed) == got.ID {
			t.Fatalf("PathProofID did not commit field group %d", index)
		}
	}
	persisted, err := fixture.repository.LoadPathProof(context.Background(), got.ID)
	if err != nil || persisted.ID != got.ID {
		t.Fatalf("persisted PathProof = %+v, %v", persisted, err)
	}
}

func TestPathProofBuilderRejectsStaleAndCrossBoundMaterial(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pathProofFixture, *BuildRequest)
		want   error
	}{
		{"stale Home TrustRoot", func(f *pathProofFixture, request *BuildRequest) { request.ExpectedHomeTrustRoot.Hash[0] ^= 1 }, ErrStaleTrustViewSnapshot},
		{"wrong source block hash", func(f *pathProofFixture, request *BuildRequest) { request.SourceBlockHash[0] ^= 1 }, ErrSourceBlockHashMismatch},
		{"missing material", func(f *pathProofFixture, _ *BuildRequest) { f.repository.missingAt = 1 }, ErrProofMaterialMissing},
		{"wrong witness leaf", func(f *pathProofFixture, _ *BuildRequest) { f.repository.materials[1].Witness.LeafIndex++ }, ErrProofMaterialMissing},
		{"wrong witness siblings", func(f *pathProofFixture, _ *BuildRequest) { f.repository.materials[1].Witness.Siblings[0][0] ^= 1 }, ErrProofMaterialMissing},
		{"cross snapshot material", func(f *pathProofFixture, _ *BuildRequest) { f.repository.materials[1].SnapshotID[0] ^= 1 }, ErrProofMaterialMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPathProofFixture(t)
			request := BuildRequest{PlanID: fixture.plan.ID, SourceBlockHash: fixture.nodeA.Key.BlockHash, ExpectedHomeTrustRoot: fixture.snapshot.HomeTrustRoot}
			test.mutate(fixture, &request)
			_, err := NewBuilder(fixture.repository, 1).Build(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Build() error = %v, want %v", err, test.want)
			}
			if len(fixture.repository.saved) != 0 {
				t.Fatal("invalid PathProof was persisted")
			}
		})
	}
}

func TestPathProofBuilderRejectsReversePlannerPath(t *testing.T) {
	fixture := newPathProofFixture(t)
	fixture.plan.Hops[0], fixture.plan.Hops[1] = fixture.plan.Hops[1], fixture.plan.Hops[0]
	fixture.plan.ID = planner.ComputePlanID(fixture.plan)
	fixture.repository.plan = fixture.plan
	_, err := NewBuilder(fixture.repository, 1).Build(context.Background(), BuildRequest{
		PlanID: fixture.plan.ID, SourceBlockHash: fixture.nodeA.Key.BlockHash,
		ExpectedHomeTrustRoot: fixture.snapshot.HomeTrustRoot,
	})
	if !errors.Is(err, ErrProofMaterialMissing) {
		t.Fatalf("reverse planner path error = %v", err)
	}
}

type memoryPathProofRepository struct {
	plan      planner.Plan
	snapshot  trustview.TrustViewSnapshot
	materials []PathProofMaterial
	missingAt int
	saved     map[PathProofID]PathProof
}

func (repository *memoryPathProofRepository) LoadPathPlan(_ context.Context, id planner.PlanID) (planner.Plan, error) {
	if id != repository.plan.ID {
		return planner.Plan{}, planner.ErrPlanNotFound
	}
	return repository.plan.Clone(), nil
}
func (repository *memoryPathProofRepository) LoadTrustViewSnapshot(_ context.Context, id trustview.SnapshotID) (trustview.TrustViewSnapshot, error) {
	if id != repository.snapshot.ID {
		return trustview.TrustViewSnapshot{}, errors.New("snapshot missing")
	}
	return repository.snapshot.Clone(), nil
}
func (repository *memoryPathProofRepository) LoadPathProofMaterial(_ context.Context, planID planner.PlanID, snapshotID trustview.SnapshotID, planHopIndex int, edgeID trustview.EdgeID) (PathProofMaterial, error) {
	if repository.missingAt == planHopIndex {
		return PathProofMaterial{}, ErrProofMaterialMissing
	}
	if planID != repository.plan.ID || snapshotID != repository.snapshot.ID || planHopIndex < 0 || planHopIndex >= len(repository.materials) || edgeID != repository.plan.Hops[planHopIndex] {
		return PathProofMaterial{}, ErrProofMaterialMissing
	}
	return repository.materials[planHopIndex].Clone(), nil
}
func (repository *memoryPathProofRepository) SavePathProof(_ context.Context, value PathProof) error {
	if value.ID != ComputePathProofID(value) {
		return ErrPathProofIDMismatch
	}
	if repository.saved == nil {
		repository.saved = make(map[PathProofID]PathProof)
	}
	repository.saved[value.ID] = value.Clone()
	return nil
}
func (repository *memoryPathProofRepository) LoadPathProof(_ context.Context, id PathProofID) (PathProof, error) {
	value, ok := repository.saved[id]
	if !ok {
		return PathProof{}, ErrPathProofNotFound
	}
	return value.Clone(), nil
}

type pathProofFixture struct {
	repository           *memoryPathProofRepository
	plan                 planner.Plan
	snapshot             trustview.TrustViewSnapshot
	nodeA, nodeB, nodeC  trustview.TrustNode
	edgeCB, edgeBA       trustview.TrustEdge
	witnessCB, witnessBA trustview.MembershipWitness
}

func newPathProofFixture(t *testing.T) *pathProofFixture {
	t.Helper()
	chainA, _ := domain.NewChainID(101)
	chainB, _ := domain.NewChainID(102)
	chainC, _ := domain.NewChainID(103)
	height, _ := domain.NewBlockHeight(7)
	evidenceA, evidenceB, evidenceC := evidence.ID{1}, evidence.ID{2}, evidence.ID{3}
	rootA := trustview.TrustRoot{Hash: common.HexToHash("0xa11")}
	blockA, blockB, blockC := common.HexToHash("0xa01"), common.HexToHash("0xb01"), common.HexToHash("0xc01")
	witnessBA := trustview.NewMembershipWitness(evidenceA, 0, []common.Hash{common.HexToHash("0xba55")})
	rootBHash, err := internalproof.RootFromWitness(domain.LeafHash(rootA.Hash, blockA), witnessBA.LeafIndex, witnessBA.Siblings, 1)
	if err != nil {
		t.Fatal(err)
	}
	rootB := trustview.TrustRoot{Hash: rootBHash}
	witnessCB := trustview.NewMembershipWitness(evidenceB, 1, []common.Hash{common.HexToHash("0xcb55")})
	rootCHash, err := internalproof.RootFromWitness(domain.LeafHash(rootB.Hash, blockB), witnessCB.LeafIndex, witnessCB.Siblings, 1)
	if err != nil {
		t.Fatal(err)
	}
	nodeA := trustview.NewTrustNode(trustview.NodeKey{ChainID: chainA, Height: height, BlockHash: blockA}, rootA, evidenceA)
	nodeB := trustview.NewTrustNode(trustview.NodeKey{ChainID: chainB, Height: height, BlockHash: blockB}, rootB, evidenceB)
	nodeC := trustview.NewTrustNode(trustview.NodeKey{ChainID: chainC, Height: height, BlockHash: blockC}, trustview.TrustRoot{Hash: rootCHash}, evidenceC)
	edgeCB, _ := trustview.NewTrustEdge(nodeC.ID, nodeB.ID, evidenceB, witnessCB.LeafIndex, &witnessCB.ID, 10)
	edgeBA, _ := trustview.NewTrustEdge(nodeB.ID, nodeA.ID, evidenceA, witnessBA.LeafIndex, &witnessBA.ID, 10)
	requestID := domain.RequestID(common.HexToHash("0x1234"))
	snapshot := trustview.TrustViewSnapshot{Revision: 4, HomeChainID: chainC, HomeTrustRoot: nodeC.Root, StartNodeID: nodeC.ID, TargetNodeID: nodeA.ID, Sealed: true, Nodes: []trustview.TrustNode{nodeA, nodeB, nodeC}, Edges: []trustview.TrustEdge{edgeCB, edgeBA}}
	snapshot.ID = trustview.ComputeSnapshotID(requestID, 0, snapshot.Revision, nodeC.ID, nodeA.ID, snapshot.HomeTrustRoot)
	directCost, pathCost := uint64(3_000_096), uint64(20)
	plan := planner.Plan{RequestID: requestID, Attempt: 0, SnapshotID: snapshot.ID, ProfileID: "pow-spv-3m", Type: planner.PathPlan, HomeNodeID: nodeC.ID, TargetNodeID: nodeA.ID, Hops: []trustview.EdgeID{edgeCB.ID, edgeBA.ID}, PathStepCost: 10, PathCost: &pathCost, DirectCost: &directCost}
	plan.ID = planner.ComputePlanID(plan)
	materials := []PathProofMaterial{
		{PlanID: plan.ID, SnapshotID: snapshot.ID, PlanHopIndex: 0, Edge: edgeCB, FromNode: nodeC, ToNode: nodeB, Witness: witnessCB},
		{PlanID: plan.ID, SnapshotID: snapshot.ID, PlanHopIndex: 1, Edge: edgeBA, FromNode: nodeB, ToNode: nodeA, Witness: witnessBA},
	}
	repository := &memoryPathProofRepository{plan: plan, snapshot: snapshot, materials: materials, missingAt: -1, saved: make(map[PathProofID]PathProof)}
	return &pathProofFixture{repository: repository, plan: plan, snapshot: snapshot, nodeA: nodeA, nodeB: nodeB, nodeC: nodeC, edgeCB: edgeCB, edgeBA: edgeBA, witnessCB: witnessCB, witnessBA: witnessBA}
}
