package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type workerSourceFake struct {
	request  coordinator.Request
	found    bool
	snapshot trustview.TrustViewSnapshot
	retry    int
}

func (source *workerSourceFake) NextRequest(context.Context) (coordinator.Request, bool, error) {
	return source.request, source.found, nil
}
func (source *workerSourceFake) NextAttempt(context.Context, domain.RequestID) (uint64, error) {
	return 0, nil
}
func (source *workerSourceFake) CreateSnapshot(_ context.Context, request coordinator.Request, _ uint64, home, target trustview.TrustNode) (trustview.TrustViewSnapshot, error) {
	source.snapshot = trustview.TrustViewSnapshot{ID: trustview.SnapshotID(common.HexToHash("0x55")), HomeChainID: request.HomeChainID, HomeTrustRoot: home.Root, StartNodeID: home.ID, TargetNodeID: target.ID, Sealed: true}
	return source.snapshot, nil
}
func (source *workerSourceFake) MarkRetryable(context.Context, coordinator.Request, string) error {
	source.retry++
	return nil
}

type endpointObserverFake struct{ home, source trustview.TrustNode }

func (observer endpointObserverFake) ObserveRequestEndpoints(context.Context, coordinator.Request) (trustview.TrustNode, trustview.TrustNode, error) {
	return observer.home, observer.source, nil
}

type workerCoordinatorFake struct{ result coordinator.Result }

func (coordinator workerCoordinatorFake) Process(context.Context, coordinator.Work) (coordinator.Result, error) {
	return coordinator.result, nil
}

type directBuilderFake struct{ proof []byte }

func (builder directBuilderFake) BuildProof(context.Context, DirectProofInput) ([]byte, common.Hash, error) {
	return builder.proof, common.HexToHash("0x1"), nil
}

type workerSubmitterFake struct {
	recover int
	intents []SubmissionIntent
	err     error
}

func (submitter *workerSubmitterFake) Recover(context.Context) error { submitter.recover++; return nil }
func (submitter *workerSubmitterFake) Submit(_ context.Context, intent SubmissionIntent) (TransactionSubmission, error) {
	submitter.intents = append(submitter.intents, intent)
	return TransactionSubmission{}, submitter.err
}

func TestRequestWorkerRecoversFirstThenSubmitsDirectPlan(t *testing.T) {
	homeChain, _ := domain.NewChainID(3)
	sourceChain, _ := domain.NewChainID(1)
	height, _ := domain.NewBlockHeight(7)
	request := coordinator.Request{ID: domain.RequestID(common.HexToHash("0x11")), HomeChainID: homeChain, Gateway: common.HexToAddress("0x1234"), SourceChainID: sourceChain, SourceHeight: height, SourceBlockHash: common.HexToHash("0xaa"), State: coordinator.Observed}
	home := trustview.TrustNode{ID: trustview.NodeID(common.HexToHash("0x21")), Key: trustview.NodeKey{ChainID: homeChain}, Root: trustview.TrustRoot{}}
	sourceNode := trustview.TrustNode{ID: trustview.NodeID(common.HexToHash("0x22")), Key: trustview.NodeKey{ChainID: sourceChain, Height: height, BlockHash: request.SourceBlockHash}, Root: trustview.TrustRoot{Hash: common.HexToHash("0xbb")}}
	source := &workerSourceFake{request: request, found: true}
	plan := planner.Plan{ID: planner.PlanID(common.HexToHash("0x31")), RequestID: request.ID, Type: planner.DirectPlan}
	coordinatorResult := coordinator.Result{Request: coordinator.Request{ID: request.ID, State: coordinator.Planned}, Plan: plan}
	submitter := &workerSubmitterFake{}
	worker, err := NewRequestWorker(source, endpointObserverFake{home: home, source: sourceNode}, workerCoordinatorFake{result: coordinatorResult}, directBuilderFake{proof: []byte{1, 2}}, submitter, common.HexToAddress("0x7777"))
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.Step(context.Background())
	if err != nil || !worked || submitter.recover != 1 || len(submitter.intents) != 1 {
		t.Fatalf("worked=%v recover=%d intents=%d err=%v", worked, submitter.recover, len(submitter.intents), err)
	}
	want := chainabi.EncodeVerifyDirectAndRecordCall(request.ID, []byte{1, 2})
	if string(submitter.intents[0].Calldata) != string(want) || submitter.intents[0].ExpectedHomeTrustRoot != home.Root {
		t.Fatal("worker changed direct calldata or zero home root")
	}
}

func TestRequestWorkerStaleRootBecomesRetryableWithoutSuccess(t *testing.T) {
	homeChain, _ := domain.NewChainID(3)
	sourceChain, _ := domain.NewChainID(1)
	height, _ := domain.NewBlockHeight(7)
	request := coordinator.Request{ID: domain.RequestID(common.HexToHash("0x11")), HomeChainID: homeChain, Gateway: common.HexToAddress("0x1234"), SourceChainID: sourceChain, SourceHeight: height, SourceBlockHash: common.HexToHash("0xaa"), State: coordinator.Observed}
	home := trustview.TrustNode{ID: trustview.NodeID(common.HexToHash("0x21")), Key: trustview.NodeKey{ChainID: homeChain}}
	target := trustview.TrustNode{ID: trustview.NodeID(common.HexToHash("0x22")), Key: trustview.NodeKey{ChainID: sourceChain, Height: height, BlockHash: request.SourceBlockHash}}
	source := &workerSourceFake{request: request, found: true}
	submitter := &workerSubmitterFake{err: ErrStaleHomeTrustRoot}
	plan := planner.Plan{ID: planner.PlanID(common.HexToHash("0x31")), RequestID: request.ID, Type: planner.DirectPlan}
	worker, _ := NewRequestWorker(source, endpointObserverFake{home: home, source: target}, workerCoordinatorFake{result: coordinator.Result{Request: coordinator.Request{ID: request.ID, State: coordinator.Planned}, Plan: plan}}, directBuilderFake{proof: []byte{1}}, submitter, common.HexToAddress("0x7777"))
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.retry != 1 {
		t.Fatalf("retry count=%d", source.retry)
	}
	submitter.err = ErrHomeBlockBoundary
	source.retry = 0
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.retry != 0 {
		t.Fatal("same-block boundary was treated as stale plan")
	}
}

func TestRequestWorkerRecoveryFailureIsFatalAndDoesNotPlan(t *testing.T) {
	source := &workerSourceFake{found: true}
	submitter := &workerSubmitterFake{}
	failing := &recoverFailureSubmitter{workerSubmitterFake: submitter, err: errors.New("corrupt durable raw transaction")}
	worker, _ := NewRequestWorker(source, endpointObserverFake{}, workerCoordinatorFake{}, directBuilderFake{}, failing, common.HexToAddress("0x1"))
	if _, err := worker.Step(context.Background()); err == nil {
		t.Fatal("recovery failure hidden")
	}
}

type recoverFailureSubmitter struct {
	*workerSubmitterFake
	err error
}

func (submitter *recoverFailureSubmitter) Recover(context.Context) error { return submitter.err }
