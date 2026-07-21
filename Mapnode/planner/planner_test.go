package planner

import (
	"context"
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestPlannerSelectsDirectedPathAndReportsExplicitFallbacks(t *testing.T) {
	snapshot := plannerSnapshot(t, 10, 10, true)
	profiles := profileSource{profile: registry.ValidatedDirectProfile{ID: "pow-spv-3m", Fingerprint: common.HexToHash("0x11"), DirectCost: 20, Calibrated: true}}
	repo := &memoryRepository{snapshot: snapshot}
	p := New(repo, repo, profiles)
	request := Request{ID: domain.RequestID(common.HexToHash("0x99")), Attempt: 0, SnapshotID: snapshot.ID, HomeChainID: snapshot.HomeChainID}
	plan, err := p.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Type != PathPlan || len(plan.Hops) != 2 || plan.Hops[0] != snapshot.Edges[0].ID || plan.Hops[1] != snapshot.Edges[1].ID {
		t.Fatalf("Plan() = %+v", plan)
	}
	loaded, err := repo.LoadPlan(context.Background(), plan.ID)
	if err != nil || loaded.ID != plan.ID || len(loaded.Hops) != 2 {
		t.Fatalf("LoadPlan() = %+v, %v", loaded, err)
	}

	profiles.profile.DirectCost = 19
	plan, err = New(repo, repo, profiles).Plan(context.Background(), Request{ID: domain.RequestID(common.HexToHash("0x98")), Attempt: 1, SnapshotID: snapshot.ID, HomeChainID: snapshot.HomeChainID})
	if err != nil || plan.Type != DirectPlan || plan.FallbackReason != PathCostExceedsDirect {
		t.Fatalf("expensive Plan() = %+v, %v", plan, err)
	}

	profiles.profile.DirectCost = 20
	snapshot.Edges[0].WitnessID = nil
	repo.snapshot = snapshot
	plan, err = New(repo, repo, profiles).Plan(context.Background(), Request{ID: domain.RequestID(common.HexToHash("0x97")), Attempt: 2, SnapshotID: snapshot.ID, HomeChainID: snapshot.HomeChainID})
	if err != nil || plan.FallbackReason != ProofMaterialMissing {
		t.Fatalf("missing witness Plan() = %+v, %v", plan, err)
	}
}

func TestPlannerFallbackReachabilityDirectionProfileAndCostValidation(t *testing.T) {
	base := plannerSnapshot(t, 10, 10, true)
	tests := []struct {
		name       string
		mutate     func(*trustview.TrustViewSnapshot, *registry.ValidatedDirectProfile)
		wantReason FallbackReason
		wantErr    error
	}{
		{"reverse unreachable", func(snapshot *trustview.TrustViewSnapshot, _ *registry.ValidatedDirectProfile) {
			snapshot.StartNodeID, snapshot.TargetNodeID = snapshot.TargetNodeID, snapshot.StartNodeID
			snapshot.HomeTrustRoot = snapshot.Nodes[2].Root
		}, NoPath, nil},
		{"no path", func(snapshot *trustview.TrustViewSnapshot, _ *registry.ValidatedDirectProfile) {
			snapshot.Edges = snapshot.Edges[:1]
		}, NoPath, nil},
		{"uncalibrated", func(_ *trustview.TrustViewSnapshot, profile *registry.ValidatedDirectProfile) {
			profile.Calibrated = false
			profile.DirectCost = 0
		}, UncalibratedProfile, nil},
		{"inconsistent step", func(snapshot *trustview.TrustViewSnapshot, _ *registry.ValidatedDirectProfile) {
			snapshot.Edges[1].PathStepCost = 11
			changed, _ := trustview.NewTrustEdge(snapshot.Edges[1].From, snapshot.Edges[1].To, snapshot.Edges[1].EvidenceID, snapshot.Edges[1].WitnessID, 11)
			snapshot.Edges[1] = changed
		}, "", ErrInconsistentPathStepCost},
		{"overflow", func(snapshot *trustview.TrustViewSnapshot, profile *registry.ValidatedDirectProfile) {
			for index, edge := range snapshot.Edges {
				changed, _ := trustview.NewTrustEdge(edge.From, edge.To, edge.EvidenceID, edge.WitnessID, math.MaxUint64)
				snapshot.Edges[index] = changed
			}
			profile.DirectCost = math.MaxUint64
		}, "", ErrPathCostOverflow},
		{"invalid edge identity", func(snapshot *trustview.TrustViewSnapshot, _ *registry.ValidatedDirectProfile) {
			snapshot.Edges[0].ID[0] ^= 1
		}, "", trustview.ErrInvalidTrustEdge},
		{"stale home root", func(snapshot *trustview.TrustViewSnapshot, _ *registry.ValidatedDirectProfile) {
			snapshot.HomeTrustRoot.Hash[0] ^= 1
		}, "", ErrStaleSnapshot},
		{"same endpoint", func(snapshot *trustview.TrustViewSnapshot, _ *registry.ValidatedDirectProfile) {
			snapshot.TargetNodeID = snapshot.StartNodeID
		}, "", ErrSnapshotEndpoint},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := base.Clone()
			profile := registry.ValidatedDirectProfile{ID: "pow-spv-3m", Fingerprint: common.HexToHash("0x11"), DirectCost: 20, Calibrated: true}
			tt.mutate(&snapshot, &profile)
			repo := &memoryRepository{snapshot: snapshot}
			plan, err := New(repo, repo, profileSource{profile: profile}).Plan(context.Background(), Request{ID: domain.RequestID(common.BigToHash(big.NewInt(1))), Attempt: uint64(index), SnapshotID: snapshot.ID, HomeChainID: snapshot.HomeChainID})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Plan() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || plan.Type != DirectPlan || plan.FallbackReason != tt.wantReason {
				t.Fatalf("Plan() = %+v, %v", plan, err)
			}
		})
	}
}

func TestPlannerDeterministicTieBreakIgnoresInsertionOrderAndCycles(t *testing.T) {
	chainC, _ := domain.NewChainID(103)
	chainB, _ := domain.NewChainID(102)
	chainD, _ := domain.NewChainID(104)
	chainA, _ := domain.NewChainID(101)
	height, _ := domain.NewBlockHeight(1)
	node := func(chain domain.ChainID, tag byte) trustview.TrustNode {
		return trustview.NewTrustNode(trustview.NodeKey{ChainID: chain, Height: height, BlockHash: common.BytesToHash([]byte{tag})}, trustview.TrustRoot{Hash: common.BytesToHash([]byte{tag, tag})}, evidence.ID{tag})
	}
	c, b, d, a := node(chainC, 1), node(chainB, 2), node(chainD, 3), node(chainA, 4)
	w := trustview.WitnessID(common.HexToHash("0x1"))
	edge := func(from, to trustview.TrustNode, tag byte) trustview.TrustEdge {
		result, err := trustview.NewTrustEdge(from.ID, to.ID, evidence.ID{tag}, &w, 10)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	edges := []trustview.TrustEdge{edge(c, b, 11), edge(b, a, 12), edge(c, d, 13), edge(d, a, 14), edge(b, c, 15)}
	nodes := []trustview.TrustNode{c, b, d, a}
	profile := profileSource{profile: registry.ValidatedDirectProfile{ID: "pow-spv-3m", Fingerprint: common.HexToHash("0x11"), DirectCost: 20, Calibrated: true}}
	var want []trustview.EdgeID
	random := rand.New(rand.NewSource(42))
	for iteration := 0; iteration < 40; iteration++ {
		random.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
		random.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
		snapshot := trustview.TrustViewSnapshot{ID: trustview.SnapshotID(common.HexToHash("0x77")), Revision: 1, HomeChainID: chainC, HomeTrustRoot: c.Root, StartNodeID: c.ID, TargetNodeID: a.ID, Sealed: true, Nodes: append([]trustview.TrustNode(nil), nodes...), Edges: append([]trustview.TrustEdge(nil), edges...)}
		repo := &memoryRepository{snapshot: snapshot}
		plan, err := New(repo, repo, profile).Plan(context.Background(), Request{ID: domain.RequestID(common.BigToHash(big.NewInt(1))), Attempt: uint64(iteration), SnapshotID: snapshot.ID, HomeChainID: chainC})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Type != PathPlan || len(plan.Hops) != 2 {
			t.Fatalf("plan = %+v", plan)
		}
		if iteration == 0 {
			want = append([]trustview.EdgeID(nil), plan.Hops...)
		} else if plan.Hops[0] != want[0] || plan.Hops[1] != want[1] {
			t.Fatalf("iteration %d path changed: %x, want %x", iteration, plan.Hops, want)
		}
		seen := map[trustview.NodeID]bool{c.ID: true}
		current := c.ID
		for _, hop := range plan.Hops {
			var selected trustview.TrustEdge
			for _, candidate := range snapshot.Edges {
				if candidate.ID == hop {
					selected = candidate
					break
				}
			}
			if selected.From != current || seen[selected.To] {
				t.Fatal("cycle or discontinuity in selected path")
			}
			seen[selected.To] = true
			current = selected.To
		}
	}
}

type memoryRepository struct {
	snapshot trustview.TrustViewSnapshot
	plans    []Plan
}

func (repo *memoryRepository) LoadTrustViewSnapshot(context.Context, trustview.SnapshotID) (trustview.TrustViewSnapshot, error) {
	return repo.snapshot.Clone(), nil
}
func (repo *memoryRepository) SavePlan(_ context.Context, plan Plan) error {
	repo.plans = append(repo.plans, plan.Clone())
	return nil
}
func (repo *memoryRepository) LoadPlan(_ context.Context, id PlanID) (Plan, error) {
	for _, plan := range repo.plans {
		if plan.ID == id {
			return plan.Clone(), nil
		}
	}
	return Plan{}, ErrPlanNotFound
}

type profileSource struct {
	profile registry.ValidatedDirectProfile
}

func (source profileSource) DirectProfile(domain.ChainID) (registry.ValidatedDirectProfile, bool) {
	return source.profile, true
}

func plannerSnapshot(t *testing.T, firstCost, secondCost uint64, witnesses bool) trustview.TrustViewSnapshot {
	t.Helper()
	chainC, _ := domain.NewChainID(103)
	chainB, _ := domain.NewChainID(102)
	chainA, _ := domain.NewChainID(101)
	height, _ := domain.NewBlockHeight(1)
	node := func(chain domain.ChainID, hash string, root string, tag byte) trustview.TrustNode {
		return trustview.NewTrustNode(trustview.NodeKey{ChainID: chain, Height: height, BlockHash: common.HexToHash(hash)}, trustview.TrustRoot{Hash: common.HexToHash(root)}, evidence.ID{tag})
	}
	c := node(chainC, "0xc", "0xcc", 1)
	b := node(chainB, "0xb", "0xbb", 2)
	a := node(chainA, "0xa", "0xaa", 3)
	w1 := trustview.WitnessID(common.HexToHash("0x1"))
	w2 := trustview.WitnessID(common.HexToHash("0x2"))
	var p1, p2 *trustview.WitnessID
	if witnesses {
		p1, p2 = &w1, &w2
	}
	e1, _ := trustview.NewTrustEdge(c.ID, b.ID, evidence.ID{4}, p1, firstCost)
	e2, _ := trustview.NewTrustEdge(b.ID, a.ID, evidence.ID{5}, p2, secondCost)
	return trustview.TrustViewSnapshot{ID: trustview.SnapshotID(common.HexToHash("0x55")), Revision: 1, HomeChainID: chainC, HomeTrustRoot: c.Root, StartNodeID: c.ID, TargetNodeID: a.ID, Sealed: true, Nodes: []trustview.TrustNode{c, b, a}, Edges: []trustview.TrustEdge{e1, e2}}
}
