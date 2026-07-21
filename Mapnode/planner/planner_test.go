package planner

import (
	"context"
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestComputePlanIDCommitsEveryPersistedDecisionField(t *testing.T) {
	pathCost, directCost := uint64(20), uint64(3_000_096)
	base := Plan{RequestID: domain.RequestID(common.HexToHash("0x1")), Attempt: 2, SnapshotID: trustview.SnapshotID(common.HexToHash("0x2")), ProfileID: "pow-spv-3m", ProfileFingerprint: common.HexToHash("0x3"), Type: PathPlan, HomeNodeID: trustview.NodeID(common.HexToHash("0x4")), TargetNodeID: trustview.NodeID(common.HexToHash("0x5")), Hops: []trustview.EdgeID{trustview.EdgeID(common.HexToHash("0x6")), trustview.EdgeID(common.HexToHash("0x7"))}, PathStepCost: 10, PathCost: &pathCost, DirectCost: &directCost, CreatedAt: time.Unix(1, 0)}
	want := ComputePlanID(base)
	base.ID = want
	mutations := []struct {
		name  string
		apply func(*Plan)
	}{
		{"request", func(plan *Plan) { plan.RequestID[0] ^= 1 }}, {"attempt", func(plan *Plan) { plan.Attempt++ }}, {"snapshot", func(plan *Plan) { plan.SnapshotID[0] ^= 1 }},
		{"profile ID", func(plan *Plan) { plan.ProfileID += "x" }}, {"fingerprint", func(plan *Plan) { plan.ProfileFingerprint[0] ^= 1 }}, {"type", func(plan *Plan) { plan.Type = DirectPlan }},
		{"home", func(plan *Plan) { plan.HomeNodeID[0] ^= 1 }}, {"target", func(plan *Plan) { plan.TargetNodeID[0] ^= 1 }}, {"hop", func(plan *Plan) { plan.Hops[0][0] ^= 1 }},
		{"hop count", func(plan *Plan) { plan.Hops = plan.Hops[:1] }}, {"step cost", func(plan *Plan) { plan.PathStepCost++ }}, {"path cost", func(plan *Plan) { *plan.PathCost += 1 }},
		{"path cost presence", func(plan *Plan) { plan.PathCost = nil }}, {"direct cost", func(plan *Plan) { *plan.DirectCost += 1 }}, {"direct cost presence", func(plan *Plan) { plan.DirectCost = nil }},
		{"fallback", func(plan *Plan) { plan.FallbackReason = NoPath }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			changed := base.Clone()
			tt.apply(&changed)
			if ComputePlanID(changed) == want {
				t.Fatal("PlanID ignored persisted decision field")
			}
		})
	}
	createdChanged := base.Clone()
	createdChanged.CreatedAt = time.Unix(99, 0)
	if ComputePlanID(createdChanged) != want {
		t.Fatal("PlanID included non-decision CreatedAt")
	}
}

func TestPlannerSelectsDirectedPathAndReportsExplicitFallbacks(t *testing.T) {
	snapshot := plannerSnapshot(t, 10, 10, true)
	profiles := profileSource{profile: registry.ValidatedDirectProfile{ID: "pow-spv-3m", Fingerprint: common.HexToHash("0x11"), DirectCost: 20, Calibrated: true}}
	repo := &memoryRepository{snapshot: snapshot}
	p := New(repo, repo, profiles)
	request := bindSnapshotAttempt(&snapshot, domain.RequestID(common.HexToHash("0x99")), 0)
	repo.snapshot = snapshot
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
	expensiveRequest := bindSnapshotAttempt(&snapshot, domain.RequestID(common.HexToHash("0x98")), 1)
	repo.snapshot = snapshot
	plan, err = New(repo, repo, profiles).Plan(context.Background(), expensiveRequest)
	if err != nil || plan.Type != DirectPlan || plan.FallbackReason != PathCostExceedsDirect {
		t.Fatalf("expensive Plan() = %+v, %v", plan, err)
	}

	profiles.profile.DirectCost = 20
	snapshot.Edges[0].WitnessID = nil
	repo.snapshot = snapshot
	missingRequest := bindSnapshotAttempt(&snapshot, domain.RequestID(common.HexToHash("0x97")), 2)
	repo.snapshot = snapshot
	plan, err = New(repo, repo, profiles).Plan(context.Background(), missingRequest)
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
			snapshot.HomeChainID = snapshot.Nodes[2].Key.ChainID
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
			changed, _ := trustview.NewTrustEdge(snapshot.Edges[1].From, snapshot.Edges[1].To, snapshot.Edges[1].EvidenceID, snapshot.Edges[1].LeafIndex, snapshot.Edges[1].WitnessID, 11)
			snapshot.Edges[1] = changed
		}, "", ErrInconsistentPathStepCost},
		{"overflow", func(snapshot *trustview.TrustViewSnapshot, profile *registry.ValidatedDirectProfile) {
			for index, edge := range snapshot.Edges {
				changed, _ := trustview.NewTrustEdge(edge.From, edge.To, edge.EvidenceID, edge.LeafIndex, edge.WitnessID, math.MaxUint64)
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
			requestID := domain.RequestID(common.BigToHash(big.NewInt(1)))
			request := bindSnapshotAttempt(&snapshot, requestID, uint64(index))
			repo := &memoryRepository{snapshot: snapshot}
			plan, err := New(repo, repo, profileSource{profile: profile}).Plan(context.Background(), request)
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

func TestPlannerRejectsSnapshotBoundToDifferentRequestAttemptOrHomeChain(t *testing.T) {
	base := plannerSnapshot(t, 10, 10, true)
	bound := bindSnapshotAttempt(&base, domain.RequestID(common.HexToHash("0x1234")), 7)
	profile := profileSource{profile: registry.ValidatedDirectProfile{ID: "pow-spv-3m", Fingerprint: common.HexToHash("0x11"), DirectCost: 20, Calibrated: true}}
	tests := []struct {
		name   string
		mutate func(*Request, *trustview.TrustViewSnapshot)
	}{
		{"request ID", func(request *Request, _ *trustview.TrustViewSnapshot) { request.ID[0] ^= 1 }},
		{"attempt", func(request *Request, _ *trustview.TrustViewSnapshot) { request.Attempt++ }},
		{"start chain", func(request *Request, snapshot *trustview.TrustViewSnapshot) {
			other, _ := domain.NewChainID(999)
			snapshot.HomeChainID = other
			request.HomeChainID = other
			snapshot.ID = trustview.ComputeSnapshotID(request.ID, request.Attempt, snapshot.Revision, snapshot.StartNodeID, snapshot.TargetNodeID, snapshot.HomeTrustRoot)
			request.SnapshotID = snapshot.ID
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := base.Clone()
			request := bound
			tt.mutate(&request, &snapshot)
			repo := &memoryRepository{snapshot: snapshot}
			if _, err := New(repo, repo, profile).Plan(context.Background(), request); !errors.Is(err, ErrSnapshotEndpoint) {
				t.Fatalf("Plan() error = %v", err)
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
		result, err := trustview.NewTrustEdge(from.ID, to.ID, evidence.ID{tag}, uint32(tag), &w, 10)
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
		requestID := domain.RequestID(common.BigToHash(big.NewInt(1)))
		request := bindSnapshotAttempt(&snapshot, requestID, uint64(iteration))
		repo := &memoryRepository{snapshot: snapshot}
		plan, err := New(repo, repo, profile).Plan(context.Background(), request)
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
	e1, _ := trustview.NewTrustEdge(c.ID, b.ID, evidence.ID{4}, 4, p1, firstCost)
	e2, _ := trustview.NewTrustEdge(b.ID, a.ID, evidence.ID{5}, 5, p2, secondCost)
	return trustview.TrustViewSnapshot{ID: trustview.SnapshotID(common.HexToHash("0x55")), Revision: 1, HomeChainID: chainC, HomeTrustRoot: c.Root, StartNodeID: c.ID, TargetNodeID: a.ID, Sealed: true, Nodes: []trustview.TrustNode{c, b, a}, Edges: []trustview.TrustEdge{e1, e2}}
}

func bindSnapshotAttempt(snapshot *trustview.TrustViewSnapshot, requestID domain.RequestID, attempt uint64) Request {
	snapshot.ID = trustview.ComputeSnapshotID(requestID, attempt, snapshot.Revision, snapshot.StartNodeID, snapshot.TargetNodeID, snapshot.HomeTrustRoot)
	return Request{ID: requestID, Attempt: attempt, SnapshotID: snapshot.ID, HomeChainID: snapshot.HomeChainID}
}
