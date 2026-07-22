package executor

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type RequestWorkSource interface {
	NextRequest(context.Context) (coordinator.Request, bool, error)
	NextAttempt(context.Context, domain.RequestID) (uint64, error)
	CreateSnapshot(context.Context, coordinator.Request, uint64, trustview.TrustNode, trustview.TrustNode) (trustview.TrustViewSnapshot, error)
	MarkRetryable(context.Context, coordinator.Request, string) error
}

type EndpointObserver interface {
	ObserveRequestEndpoints(context.Context, coordinator.Request) (trustview.TrustNode, trustview.TrustNode, error)
}

type CoordinatorProcessor interface {
	Process(context.Context, coordinator.Work) (coordinator.Result, error)
}

type DirectProofBuilder interface {
	BuildProof(context.Context, DirectProofInput) ([]byte, common.Hash, error)
}

type SubmissionWorker interface {
	Recover(context.Context) error
	Submit(context.Context, SubmissionIntent) (TransactionSubmission, error)
}

type RequestWorker struct {
	source      RequestWorkSource
	observer    EndpointObserver
	coordinator CoordinatorProcessor
	direct      DirectProofBuilder
	submitter   SubmissionWorker
	sender      common.Address
}

func NewRequestWorker(source RequestWorkSource, observer EndpointObserver, coordinator CoordinatorProcessor, direct DirectProofBuilder, submitter SubmissionWorker, sender common.Address) (*RequestWorker, error) {
	if source == nil || observer == nil || coordinator == nil || direct == nil || submitter == nil || sender == (common.Address{}) {
		return nil, errors.New("request worker dependencies are required")
	}
	return &RequestWorker{source: source, observer: observer, coordinator: coordinator, direct: direct, submitter: submitter, sender: sender}, nil
}

func (worker *RequestWorker) Step(ctx context.Context) (bool, error) {
	if err := worker.submitter.Recover(ctx); err != nil {
		return false, err
	}
	request, found, err := worker.source.NextRequest(ctx)
	if err != nil || !found {
		return false, err
	}
	attempt, err := worker.source.NextAttempt(ctx, request.ID)
	if err != nil {
		return false, err
	}
	homeNode, sourceNode, err := worker.observer.ObserveRequestEndpoints(ctx, request)
	if err != nil {
		return false, err
	}
	snapshot, err := worker.source.CreateSnapshot(ctx, request, attempt, homeNode, sourceNode)
	if err != nil {
		return false, err
	}
	result, err := worker.coordinator.Process(ctx, coordinator.Work{Request: request, Attempt: attempt, SnapshotID: snapshot.ID, ExpectedHomeTrustRoot: homeNode.Root})
	if err != nil {
		return false, err
	}
	intent := SubmissionIntent{RequestID: request.ID, Attempt: attempt, PlanID: result.Plan.ID, PlanType: result.Plan.Type, SnapshotID: snapshot.ID, HomeChainID: request.HomeChainID, Gateway: request.Gateway, Sender: worker.sender, ExpectedHomeTrustRoot: homeNode.Root}
	switch result.Plan.Type {
	case planner.DirectPlan:
		proof, _, err := worker.direct.BuildProof(ctx, DirectProofInput{SourceChainID: request.SourceChainID, SourceHeight: request.SourceHeight, SourceBlockHash: request.SourceBlockHash, SourceTrustRoot: sourceNode.Root})
		if err != nil {
			return false, err
		}
		intent.Calldata = chainabi.EncodeVerifyDirectAndRecordCall(request.ID, proof)
	case planner.PathPlan:
		calldata, err := BuildPathCall(request.ID, result.PathProof)
		if err != nil {
			return false, err
		}
		intent.Calldata = calldata
		proofID := result.PathProof.ID
		intent.ProofID = &proofID
	default:
		return false, errors.New("coordinator returned unsupported plan type")
	}
	_, err = worker.submitter.Submit(ctx, intent)
	if errors.Is(err, ErrHomeBlockBoundary) {
		return false, nil
	}
	if errors.Is(err, ErrStaleHomeTrustRoot) {
		if retryErr := worker.source.MarkRetryable(ctx, result.Request, err.Error()); retryErr != nil {
			return false, retryErr
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (worker *RequestWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := worker.Step(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
