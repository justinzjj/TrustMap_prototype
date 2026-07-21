package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestCoordinatorPersistsPathProofBeforeReportingProofReady(t *testing.T) {
	fixture := newCoordinatorFixture()
	result, err := fixture.coordinator.Process(context.Background(), fixture.work)
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.State != ProofReady || result.Plan.Type != planner.PathPlan || result.PathProof.ID == (pathproof.PathProofID{}) {
		t.Fatalf("Process() = %+v", result)
	}
	if fixture.requests.transitions[len(fixture.requests.transitions)-1] != (stateChange{Planned, ProofReady}) || !fixture.proofs.savedBeforeProofReady {
		t.Fatalf("state advanced before durable PathProof: transitions=%+v", fixture.requests.transitions)
	}
	second, err := fixture.coordinator.Process(context.Background(), fixture.work)
	if err != nil || second.PathProof.ID != result.PathProof.ID || fixture.planning.calls != 1 || fixture.proofs.buildCalls != 2 {
		t.Fatalf("idempotent replay = %+v, %v; planner=%d builder=%d", second, err, fixture.planning.calls, fixture.proofs.buildCalls)
	}
}

func TestCoordinatorRecoversPlanAndProofPersistedBeforeStateTransition(t *testing.T) {
	for _, stage := range []RequestState{EvidenceReady, Planned} {
		t.Run(string(stage), func(t *testing.T) {
			fixture := newCoordinatorFixture()
			fixture.requests.failTransitionTo = map[RequestState]int{}
			if stage == EvidenceReady {
				fixture.requests.failTransitionTo[Planned] = 1
			} else {
				fixture.requests.failTransitionTo[ProofReady] = 1
			}
			if _, err := fixture.coordinator.Process(context.Background(), fixture.work); err == nil {
				t.Fatal("injected transition failure was hidden")
			}
			persisted := fixture.requests.record
			if persisted.State != stage {
				t.Fatalf("state = %s, want last committed %s", persisted.State, stage)
			}
			result, err := fixture.coordinator.Process(context.Background(), fixture.work)
			if err != nil || result.Request.State != ProofReady {
				t.Fatalf("recovery = %+v, %v", result, err)
			}
			wantBuilds := 1
			if stage == Planned {
				wantBuilds = 2
			}
			if fixture.planning.calls != 1 || fixture.proofs.buildCalls != wantBuilds {
				t.Fatalf("durable artifacts rebuilt: planner=%d proof=%d", fixture.planning.calls, fixture.proofs.buildCalls)
			}
		})
	}
}

func TestCoordinatorCrashWindowRevalidatesCurrentHomeTrustRoot(t *testing.T) {
	fixture := newCoordinatorFixture()
	fixture.requests.failTransitionTo[ProofReady] = 1
	if _, err := fixture.coordinator.Process(context.Background(), fixture.work); err == nil {
		t.Fatal("injected proof-ready transition failure was hidden")
	}
	if fixture.requests.record.State != Planned || !fixture.proofs.saved {
		t.Fatalf("crash window = state %s saved %t", fixture.requests.record.State, fixture.proofs.saved)
	}
	fixture.work.ExpectedHomeTrustRoot.Hash[0] ^= 1
	_, err := fixture.coordinator.Process(context.Background(), fixture.work)
	if !errors.Is(err, pathproof.ErrStaleTrustViewSnapshot) {
		t.Fatalf("changed HomeTrustRoot replay error = %v", err)
	}
	if fixture.requests.record.State != Planned {
		t.Fatalf("stale proof advanced state to %s", fixture.requests.record.State)
	}
}

func TestCoordinatorProofReadyAndReplannedReplayRevalidateCurrentHomeTrustRoot(t *testing.T) {
	for _, state := range []RequestState{ProofReady, Replanned} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newCoordinatorFixture()
			fixture.requests.record, fixture.requests.observed = fixture.work.Request, true
			fixture.requests.record.State = state
			fixture.planning.saved, fixture.proofs.saved = true, true
			fixture.work.ExpectedHomeTrustRoot.Hash[0] ^= 1
			_, err := fixture.coordinator.Process(context.Background(), fixture.work)
			if !errors.Is(err, pathproof.ErrStaleTrustViewSnapshot) {
				t.Fatalf("changed HomeTrustRoot replay error = %v", err)
			}
			if fixture.requests.record.State != state {
				t.Fatalf("stale replay changed state to %s", fixture.requests.record.State)
			}
		})
	}
}

func TestCoordinatorPersistsInitialDirectPlanAtPlanned(t *testing.T) {
	fixture := newCoordinatorFixture()
	fixture.planning.plan.Type = planner.DirectPlan
	fixture.planning.plan.FallbackReason = planner.PathCostExceedsDirect
	fixture.planning.plan.Hops = nil
	fixture.planning.plan.ID = planner.ComputePlanID(fixture.planning.plan)
	result, err := fixture.coordinator.Process(context.Background(), fixture.work)
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.State != Planned || result.Plan.Type != planner.DirectPlan || result.Plan.FallbackReason != planner.PathCostExceedsDirect {
		t.Fatalf("initial DirectPlan result = %+v", result)
	}
	if fixture.proofs.buildCalls != 0 {
		t.Fatal("DirectPlan attempted PathProof construction")
	}
}

func TestCoordinatorUsesOnlyApprovedRetryableTransitions(t *testing.T) {
	fixture := newCoordinatorFixture()
	fixture.requests.record = fixture.work.Request
	fixture.requests.record.State = Retryable
	fixture.requests.observed = true
	fixture.planning.plan.Type = planner.DirectPlan
	fixture.planning.plan.FallbackReason = planner.ProofMaterialMissing
	fixture.planning.plan.Hops = nil
	fixture.planning.plan.ID = planner.ComputePlanID(fixture.planning.plan)
	result, err := fixture.coordinator.Process(context.Background(), fixture.work)
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.State != DirectFallback || len(fixture.requests.transitions) != 1 || fixture.requests.transitions[0] != (stateChange{Retryable, DirectFallback}) {
		t.Fatalf("Retryable Direct fallback = %+v, transitions=%+v", result, fixture.requests.transitions)
	}
}

func TestCoordinatorTerminalStatesDoNotHidePlanRepositoryErrors(t *testing.T) {
	for _, state := range []RequestState{Rejected, DirectFallback} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newCoordinatorFixture()
			fixture.requests.record, fixture.requests.observed = fixture.work.Request, true
			fixture.requests.record.State = state
			fixture.planning.loadErr = errors.New("corrupt current plan index")
			if _, err := fixture.coordinator.Process(context.Background(), fixture.work); !errors.Is(err, fixture.planning.loadErr) {
				t.Fatalf("terminal state hid plan error: %v", err)
			}
		})
	}
	fixture := newCoordinatorFixture()
	fixture.requests.record, fixture.requests.observed = fixture.work.Request, true
	fixture.requests.record.State = DirectFallback
	if _, err := fixture.coordinator.Process(context.Background(), fixture.work); !errors.Is(err, planner.ErrPlanNotFound) {
		t.Fatalf("DirectFallback without durable plan error = %v", err)
	}
}

type fakeRequestRepository struct {
	record           Request
	observed         bool
	transitions      []stateChange
	failTransitionTo map[RequestState]int
	proofSaved       func() bool
}
type stateChange struct{ from, to RequestState }

func (repo *fakeRequestRepository) Observe(_ context.Context, value Request) (Request, bool, error) {
	if repo.observed {
		return repo.record, false, nil
	}
	repo.record, repo.observed = value, true
	return value, true, nil
}
func (repo *fakeRequestRepository) Load(_ context.Context, _ domain.RequestID) (Request, error) {
	return repo.record, nil
}
func (repo *fakeRequestRepository) Transition(_ context.Context, _ domain.RequestID, from, to RequestState, _ TransitionMetadata) (Request, bool, error) {
	if repo.failTransitionTo[to] > 0 {
		repo.failTransitionTo[to]--
		return Request{}, false, errors.New("injected transition failure")
	}
	if repo.record.State != from {
		return Request{}, false, errors.New("wrong expected state")
	}
	if _, err := ValidateTransition(from, to); err != nil {
		return Request{}, false, err
	}
	if to == ProofReady && repo.proofSaved != nil && !repo.proofSaved() {
		return Request{}, false, errors.New("PathProof not durable")
	}
	repo.record.State = to
	repo.transitions = append(repo.transitions, stateChange{from, to})
	return repo.record, true, nil
}

type fakeEvidenceGate struct{ err error }

func (gate fakeEvidenceGate) RequireEvidenceReady(context.Context, Request) error { return gate.err }

type fakePlanningService struct {
	plan    planner.Plan
	calls   int
	saved   bool
	loadErr error
}

func (service *fakePlanningService) Plan(context.Context, planner.Request) (planner.Plan, error) {
	service.calls++
	service.saved = true
	return service.plan.Clone(), nil
}
func (service *fakePlanningService) LoadCurrentPlan(context.Context, domain.RequestID) (planner.Plan, error) {
	if service.loadErr != nil {
		return planner.Plan{}, service.loadErr
	}
	if !service.saved {
		return planner.Plan{}, planner.ErrPlanNotFound
	}
	return service.plan.Clone(), nil
}

type fakePathProofService struct {
	value                 pathproof.PathProof
	buildCalls            int
	saved                 bool
	savedBeforeProofReady bool
	expectedHomeTrustRoot trustview.TrustRoot
}

func (service *fakePathProofService) Build(_ context.Context, request pathproof.BuildRequest) (pathproof.PathProof, error) {
	service.buildCalls++
	if request.ExpectedHomeTrustRoot != service.expectedHomeTrustRoot {
		return pathproof.PathProof{}, pathproof.ErrStaleTrustViewSnapshot
	}
	service.saved = true
	service.savedBeforeProofReady = true
	return service.value.Clone(), nil
}
func (service *fakePathProofService) LoadPathProofForPlan(context.Context, planner.PlanID) (pathproof.PathProof, error) {
	if !service.saved {
		return pathproof.PathProof{}, pathproof.ErrPathProofNotFound
	}
	return service.value.Clone(), nil
}

type coordinatorFixture struct {
	coordinator *Coordinator
	requests    *fakeRequestRepository
	planning    *fakePlanningService
	proofs      *fakePathProofService
	work        Work
}

func newCoordinatorFixture() coordinatorFixture {
	chainID, _ := domain.NewChainID(103)
	sourceID, _ := domain.NewChainID(101)
	height, _ := domain.NewBlockHeight(7)
	request := Request{ID: domain.RequestID(common.HexToHash("0x1234")), HomeChainID: chainID, SourceChainID: sourceID, SourceHeight: height, SourceBlockHash: common.HexToHash("0xa01"), State: Observed}
	snapshotID := trustview.SnapshotID(common.HexToHash("0x5555"))
	pathCost, directCost := uint64(100), uint64(3_000_096)
	plan := planner.Plan{RequestID: request.ID, SnapshotID: snapshotID, ProfileID: "pow-spv-3m", Type: planner.PathPlan, HomeNodeID: trustview.NodeID{1}, TargetNodeID: trustview.NodeID{2}, Hops: []trustview.EdgeID{{3}}, PathStepCost: 100, PathCost: &pathCost, DirectCost: &directCost}
	plan.ID = planner.ComputePlanID(plan)
	proofValue := pathproof.PathProof{ID: pathproof.PathProofID{9}, RequestID: request.ID, PlanID: plan.ID, SnapshotID: snapshotID}
	requests := &fakeRequestRepository{failTransitionTo: make(map[RequestState]int)}
	planning := &fakePlanningService{plan: plan}
	expectedHomeTrustRoot := trustview.TrustRoot{Hash: common.HexToHash("0xc01")}
	proofs := &fakePathProofService{value: proofValue, expectedHomeTrustRoot: expectedHomeTrustRoot}
	requests.proofSaved = func() bool { return proofs.saved }
	coordinator := New(requests, fakeEvidenceGate{}, planning, planning, proofs)
	return coordinatorFixture{coordinator: coordinator, requests: requests, planning: planning, proofs: proofs, work: Work{Request: request, Attempt: 0, SnapshotID: snapshotID, ExpectedHomeTrustRoot: expectedHomeTrustRoot}}
}
