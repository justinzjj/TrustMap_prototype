// Package coordinator owns the durable Phase 3 request state machine. It
// composes planning and PathProof construction but does not submit transactions.
package coordinator

import (
	"context"
	"errors"
	"fmt"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var ErrInvalidObservation = errors.New("invalid verification request observation")

type EvidenceGate interface {
	RequireEvidenceReady(context.Context, Request) error
}

type PlanningService interface {
	Plan(context.Context, planner.Request) (planner.Plan, error)
}

type CurrentPlanRepository interface {
	LoadCurrentPlan(context.Context, domain.RequestID) (planner.Plan, error)
}

type PathProofBuilder interface {
	Build(context.Context, pathproof.BuildRequest) (pathproof.PathProof, error)
}

type Work struct {
	Request               Request
	Attempt               uint64
	SnapshotID            trustview.SnapshotID
	ExpectedHomeTrustRoot trustview.TrustRoot
}

type Result struct {
	Request   Request
	Plan      planner.Plan
	PathProof pathproof.PathProof
}

type Coordinator struct {
	requests RequestRepository
	evidence EvidenceGate
	planner  PlanningService
	plans    CurrentPlanRepository
	builder  PathProofBuilder
}

func New(requests RequestRepository, evidence EvidenceGate, planning PlanningService, plans CurrentPlanRepository, builder PathProofBuilder) *Coordinator {
	return &Coordinator{requests: requests, evidence: evidence, planner: planning, plans: plans, builder: builder}
}

// Process is restart-safe: a plan or PathProof is always persisted by its
// service before the corresponding state transition. If a transition fails,
// the next call recovers the already-durable artifact instead of rebuilding it.
func (coordinator *Coordinator) Process(ctx context.Context, work Work) (Result, error) {
	if coordinator == nil || coordinator.requests == nil || coordinator.evidence == nil || coordinator.planner == nil || coordinator.plans == nil || coordinator.builder == nil {
		return Result{}, errors.New("coordinator dependencies are required")
	}
	if work.Request.ID == (domain.RequestID{}) || work.Request.State.Validate() != nil || work.SnapshotID == (trustview.SnapshotID{}) || work.Request.SourceBlockHash == ([32]byte{}) {
		return Result{}, ErrInvalidObservation
	}
	var request Request
	var err error
	if work.Request.State == Observed {
		request, _, err = coordinator.requests.Observe(ctx, work.Request)
	} else {
		request, err = coordinator.requests.Load(ctx, work.Request.ID)
		if err == nil && !sameRequestIdentityForResume(request, work.Request) {
			err = ErrInvalidObservation
		}
	}
	if err != nil {
		return Result{}, err
	}
	result := Result{Request: request}

	for {
		switch result.Request.State {
		case Rejected:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if errors.Is(loadErr, planner.ErrPlanNotFound) {
				return result, nil
			}
			if loadErr != nil {
				return Result{}, loadErr
			}
			result.Plan = plan
			return result, nil
		case DirectFallback:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if loadErr != nil {
				return Result{}, loadErr
			}
			if err := validateWorkPlan(work, plan); err != nil {
				return Result{}, err
			}
			if plan.Type != planner.DirectPlan || plan.FallbackReason == "" {
				return Result{}, errors.New("DirectFallback request lacks a valid durable DirectPlan")
			}
			result.Plan = plan
			return result, nil
		case ProofReady:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if loadErr != nil {
				return Result{}, loadErr
			}
			if err := validateWorkPlan(work, plan); err != nil {
				return Result{}, err
			}
			if plan.Type != planner.PathPlan {
				return Result{}, errors.New("ProofReady request lacks a durable PathPlan")
			}
			proofValue, loadErr := coordinator.builder.Build(ctx, pathproof.BuildRequest{PlanID: plan.ID, SourceBlockHash: result.Request.SourceBlockHash, ExpectedHomeTrustRoot: work.ExpectedHomeTrustRoot})
			if loadErr != nil {
				return Result{}, loadErr
			}
			result.Plan, result.PathProof = plan, proofValue
			return result, nil
		case Observed:
			if gateErr := coordinator.evidence.RequireEvidenceReady(ctx, result.Request); gateErr != nil {
				if errors.Is(gateErr, ErrInvalidObservation) {
					result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Observed, Rejected, TransitionMetadata{Reason: gateErr.Error()})
					if err != nil {
						return Result{}, err
					}
					continue
				}
				return Result{}, gateErr
			}
			result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Observed, EvidenceReady, TransitionMetadata{})
			if err != nil {
				return Result{}, err
			}
		case EvidenceReady:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if errors.Is(loadErr, planner.ErrPlanNotFound) {
				plan, loadErr = coordinator.planner.Plan(ctx, planner.Request{ID: result.Request.ID, Attempt: work.Attempt, SnapshotID: work.SnapshotID, HomeChainID: result.Request.HomeChainID})
			}
			if loadErr != nil {
				return Result{}, loadErr
			}
			if err := validateWorkPlan(work, plan); err != nil {
				return Result{}, err
			}
			result.Plan = plan
			result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, EvidenceReady, Planned, TransitionMetadata{})
			if err != nil {
				return Result{}, err
			}
		case Planned:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if loadErr != nil {
				return Result{}, loadErr
			}
			if err := validateWorkPlan(work, plan); err != nil {
				return Result{}, err
			}
			result.Plan = plan
			if plan.Type == planner.DirectPlan {
				return result, nil
			}
			proofValue, loadErr := coordinator.builder.Build(ctx, pathproof.BuildRequest{PlanID: plan.ID, SourceBlockHash: result.Request.SourceBlockHash, ExpectedHomeTrustRoot: work.ExpectedHomeTrustRoot})
			if loadErr != nil {
				return Result{}, loadErr
			}
			result.PathProof = proofValue
			result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Planned, ProofReady, TransitionMetadata{})
			if err != nil {
				return Result{}, err
			}
			return result, nil
		case Retryable:
			plan, planErr := coordinator.planner.Plan(ctx, planner.Request{ID: result.Request.ID, Attempt: work.Attempt, SnapshotID: work.SnapshotID, HomeChainID: result.Request.HomeChainID})
			if planErr != nil {
				return Result{}, planErr
			}
			if err := validateWorkPlan(work, plan); err != nil {
				return Result{}, err
			}
			result.Plan = plan
			if plan.Type == planner.DirectPlan {
				result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Retryable, DirectFallback, TransitionMetadata{Reason: string(plan.FallbackReason)})
			} else {
				result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Retryable, Replanned, TransitionMetadata{})
			}
			if err != nil {
				return Result{}, err
			}
		case Replanned:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if loadErr != nil {
				return Result{}, loadErr
			}
			if plan.Type != planner.PathPlan {
				return Result{}, fmt.Errorf("Replanned request has %s plan", plan.Type)
			}
			if err := validateWorkPlan(work, plan); err != nil {
				return Result{}, err
			}
			proofValue, loadErr := coordinator.builder.Build(ctx, pathproof.BuildRequest{PlanID: plan.ID, SourceBlockHash: result.Request.SourceBlockHash, ExpectedHomeTrustRoot: work.ExpectedHomeTrustRoot})
			if loadErr != nil {
				return Result{}, loadErr
			}
			result.Plan, result.PathProof = plan, proofValue
			result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Replanned, ProofReady, TransitionMetadata{})
			if err != nil {
				return Result{}, err
			}
			return result, nil
		default:
			return Result{}, fmt.Errorf("%w: %s", ErrInvalidRequestState, result.Request.State)
		}
	}
}

func sameRequestIdentityForResume(left, right Request) bool {
	return left.ID == right.ID && left.HomeChainID == right.HomeChainID && left.Gateway == right.Gateway && left.Requester == right.Requester && left.Nonce == right.Nonce && left.SourceChainID == right.SourceChainID && left.SourceHeight == right.SourceHeight && left.SourceBlockHash == right.SourceBlockHash
}

func validateWorkPlan(work Work, plan planner.Plan) error {
	if plan.RequestID != work.Request.ID || plan.Attempt != work.Attempt || plan.SnapshotID != work.SnapshotID || plan.ID != planner.ComputePlanID(plan) {
		return errors.New("persisted plan does not bind coordinator work")
	}
	return nil
}
