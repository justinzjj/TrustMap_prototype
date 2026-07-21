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

type CurrentPathProofRepository interface {
	LoadPathProofForPlan(context.Context, planner.PlanID) (pathproof.PathProof, error)
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
	proofs   CurrentPathProofRepository
}

func New(requests RequestRepository, evidence EvidenceGate, planning PlanningService, plans CurrentPlanRepository, builder PathProofBuilder, proofs CurrentPathProofRepository) *Coordinator {
	return &Coordinator{requests: requests, evidence: evidence, planner: planning, plans: plans, builder: builder, proofs: proofs}
}

// Process is restart-safe: a plan or PathProof is always persisted by its
// service before the corresponding state transition. If a transition fails,
// the next call recovers the already-durable artifact instead of rebuilding it.
func (coordinator *Coordinator) Process(ctx context.Context, work Work) (Result, error) {
	if coordinator == nil || coordinator.requests == nil || coordinator.evidence == nil || coordinator.planner == nil || coordinator.plans == nil || coordinator.builder == nil || coordinator.proofs == nil {
		return Result{}, errors.New("coordinator dependencies are required")
	}
	if work.Request.ID == (domain.RequestID{}) || work.Request.State != Observed || work.SnapshotID == (trustview.SnapshotID{}) || work.ExpectedHomeTrustRoot.Hash == ([32]byte{}) || work.Request.SourceBlockHash == ([32]byte{}) {
		return Result{}, ErrInvalidObservation
	}
	request, _, err := coordinator.requests.Observe(ctx, work.Request)
	if err != nil {
		return Result{}, err
	}
	result := Result{Request: request}

	for {
		switch result.Request.State {
		case Rejected, DirectFallback:
			if plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID); loadErr == nil {
				result.Plan = plan
			}
			return result, nil
		case ProofReady:
			plan, loadErr := coordinator.plans.LoadCurrentPlan(ctx, result.Request.ID)
			if loadErr != nil {
				return Result{}, loadErr
			}
			proofValue, loadErr := coordinator.proofs.LoadPathProofForPlan(ctx, plan.ID)
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
			proofValue, loadErr := coordinator.proofs.LoadPathProofForPlan(ctx, plan.ID)
			if errors.Is(loadErr, pathproof.ErrPathProofNotFound) {
				proofValue, loadErr = coordinator.builder.Build(ctx, pathproof.BuildRequest{PlanID: plan.ID, SourceBlockHash: result.Request.SourceBlockHash, ExpectedHomeTrustRoot: work.ExpectedHomeTrustRoot})
			}
			if loadErr != nil {
				return Result{}, loadErr
			}
			result.PathProof = proofValue
			result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Planned, ProofReady, TransitionMetadata{})
			if err != nil {
				return Result{}, err
			}
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
			proofValue, loadErr := coordinator.proofs.LoadPathProofForPlan(ctx, plan.ID)
			if errors.Is(loadErr, pathproof.ErrPathProofNotFound) {
				proofValue, loadErr = coordinator.builder.Build(ctx, pathproof.BuildRequest{PlanID: plan.ID, SourceBlockHash: result.Request.SourceBlockHash, ExpectedHomeTrustRoot: work.ExpectedHomeTrustRoot})
			}
			if loadErr != nil {
				return Result{}, loadErr
			}
			result.Plan, result.PathProof = plan, proofValue
			result.Request, _, err = coordinator.requests.Transition(ctx, result.Request.ID, Replanned, ProofReady, TransitionMetadata{})
			if err != nil {
				return Result{}, err
			}
		default:
			return Result{}, fmt.Errorf("%w: %s", ErrInvalidRequestState, result.Request.State)
		}
	}
}

func validateWorkPlan(work Work, plan planner.Plan) error {
	if plan.RequestID != work.Request.ID || plan.Attempt != work.Attempt || plan.SnapshotID != work.SnapshotID || plan.ID != planner.ComputePlanID(plan) {
		return errors.New("persisted plan does not bind coordinator work")
	}
	return nil
}
