package executor

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type submissionRepositoryFake struct {
	mu          sync.Mutex
	items       map[SubmissionID]TransactionSubmission
	events      []string
	confirmErr  error
	confirmCall int
}

func (repo *submissionRepositoryFake) SavePrepared(_ context.Context, item TransactionSubmission) (TransactionSubmission, bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.items == nil {
		repo.items = make(map[SubmissionID]TransactionSubmission)
	}
	repo.events = append(repo.events, "persist")
	if existing, ok := repo.items[item.ID]; ok {
		return existing.Clone(), false, nil
	}
	repo.items[item.ID] = item.Clone()
	return item.Clone(), true, nil
}
func (repo *submissionRepositoryFake) Load(_ context.Context, id SubmissionID) (TransactionSubmission, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	item, ok := repo.items[id]
	if !ok {
		return TransactionSubmission{}, errors.New("not found")
	}
	return item.Clone(), nil
}
func (repo *submissionRepositoryFake) ListRecoverable(context.Context) ([]TransactionSubmission, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	result := make([]TransactionSubmission, 0, len(repo.items))
	for _, item := range repo.items {
		if item.State == Prepared || item.State == Submitted || item.State == Retryable {
			result = append(result, item.Clone())
		}
	}
	return result, nil
}
func (repo *submissionRepositoryFake) Transition(_ context.Context, id SubmissionID, from, to ExecutionState, reason string, at time.Time) (TransactionSubmission, bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	item := repo.items[id]
	if item.State == to {
		return item.Clone(), false, nil
	}
	if item.State != from {
		return TransactionSubmission{}, false, errors.New("state mismatch")
	}
	item.State, item.UpdatedAt = to, at
	if to == Submitted {
		item.Reason = ""
		item.SubmittedAt = &at
	} else {
		item.Reason = reason
	}
	repo.items[id] = item
	repo.events = append(repo.events, "transition:"+string(to))
	return item.Clone(), true, nil
}
func (repo *submissionRepositoryFake) Confirm(_ context.Context, receipt ConfirmedTransactionReceipt) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.confirmCall++
	if repo.confirmErr != nil {
		return repo.confirmErr
	}
	item := repo.items[receipt.SubmissionID]
	item.State = Confirmed
	repo.items[item.ID] = item
	repo.events = append(repo.events, "confirm")
	return nil
}

type submissionRPCFake struct {
	nonce     uint64
	tip       *big.Int
	header    *types.Header
	estimate  uint64
	sendErr   error
	found     bool
	pending   bool
	receipt   *types.Receipt
	canonical *types.Header
	resolved  bool
	events    *[]string
	sentRaw   [][]byte
}

func (rpc *submissionRPCFake) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	return rpc.nonce, nil
}
func (rpc *submissionRPCFake) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return new(big.Int).Set(rpc.tip), nil
}
func (rpc *submissionRPCFake) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	if number == nil {
		return rpc.header, nil
	}
	return rpc.canonical, nil
}
func (rpc *submissionRPCFake) EstimateGas(context.Context, ethereum.CallMsg) (uint64, error) {
	return rpc.estimate, nil
}
func (rpc *submissionRPCFake) SendTransaction(_ context.Context, tx *types.Transaction) error {
	raw, _ := tx.MarshalBinary()
	rpc.sentRaw = append(rpc.sentRaw, raw)
	if rpc.events != nil {
		*rpc.events = append(*rpc.events, "broadcast")
	}
	return rpc.sendErr
}
func (rpc *submissionRPCFake) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	if !rpc.found {
		return nil, false, ethereum.NotFound
	}
	tx := new(types.Transaction)
	_ = tx.UnmarshalBinary(rpc.sentRaw[len(rpc.sentRaw)-1])
	return tx, rpc.pending, nil
}
func (rpc *submissionRPCFake) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	if rpc.receipt == nil {
		return nil, ethereum.NotFound
	}
	return rpc.receipt, nil
}
func (rpc *submissionRPCFake) RequestResolvedAtHash(context.Context, common.Address, domain.RequestID, common.Hash) (bool, error) {
	return rpc.resolved, nil
}

type rootGuardFake struct {
	calls  int
	failAt int
	err    error
}

func (guard *rootGuardFake) Validate(context.Context, trustview.TrustRoot) error {
	guard.calls++
	if guard.calls == guard.failAt {
		return guard.err
	}
	return nil
}

func TestSubmitterPersistsExactEIP1559RawBeforeBroadcast(t *testing.T) {
	repository := &submissionRepositoryFake{}
	events := &repository.events
	rpc := &submissionRPCFake{nonce: 9, tip: big.NewInt(2), header: &types.Header{Number: big.NewInt(10), BaseFee: big.NewInt(100)}, estimate: 123456, events: events}
	key := mustPrivateKey(t)
	submitter := mustSubmitter(t, rpc, repository, key, &rootGuardFake{})
	submission, err := submitter.Submit(context.Background(), submissionIntent(t, crypto.PubkeyToAddress(key.PublicKey)))
	if err != nil || submission.State != Submitted || len(rpc.sentRaw) != 1 {
		t.Fatalf("submission=%+v sends=%d err=%v", submission, len(rpc.sentRaw), err)
	}
	if len(repository.events) < 3 || repository.events[0] != "persist" || repository.events[1] != "broadcast" || repository.events[2] != "transition:submitted" {
		t.Fatalf("event order=%v", repository.events)
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(submission.RawSignedTx); err != nil || tx.Type() != types.DynamicFeeTxType || tx.Nonce() != 9 || tx.Hash() != submission.TxHash {
		t.Fatalf("persisted raw transaction type=%d nonce=%d hash=%s err=%v", tx.Type(), tx.Nonce(), tx.Hash(), err)
	}
}

func TestAmbiguousBroadcastNeverAllocatesOrSignsANewNonce(t *testing.T) {
	repository := &submissionRepositoryFake{}
	rpc := &submissionRPCFake{nonce: 4, tip: big.NewInt(1), header: &types.Header{Number: big.NewInt(10), BaseFee: big.NewInt(10)}, estimate: 100000, sendErr: errors.New("response lost"), found: true}
	key := mustPrivateKey(t)
	submitter := mustSubmitter(t, rpc, repository, key, &rootGuardFake{})
	first, err := submitter.Submit(context.Background(), submissionIntent(t, crypto.PubkeyToAddress(key.PublicKey)))
	if err != nil || first.State != Submitted {
		t.Fatalf("hash-visible ambiguity state=%s err=%v", first.State, err)
	}
	raw := append([]byte(nil), first.RawSignedTx...)
	if err := submitter.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, _ := repository.Load(context.Background(), first.ID)
	if loaded.Nonce != 4 || string(loaded.RawSignedTx) != string(raw) {
		t.Fatal("recovery changed signed transaction identity")
	}
}

func TestReceiptNeedsCanonicalConfirmationsResolutionAndIndexerBundle(t *testing.T) {
	repository := &submissionRepositoryFake{confirmErr: ErrIndexerBundlePending}
	rpc := &submissionRPCFake{nonce: 1, tip: big.NewInt(1), header: &types.Header{Number: big.NewInt(15), BaseFee: big.NewInt(10)}, estimate: 100000}
	key := mustPrivateKey(t)
	submitter := mustSubmitter(t, rpc, repository, key, &rootGuardFake{})
	item, err := submitter.Submit(context.Background(), submissionIntent(t, crypto.PubkeyToAddress(key.PublicKey)))
	if err != nil {
		t.Fatal(err)
	}
	blockHash := common.HexToHash("0xcafe")
	rpc.receipt = &types.Receipt{TxHash: item.TxHash, BlockNumber: big.NewInt(12), BlockHash: blockHash, Status: 1}
	rpc.canonical = &types.Header{Number: big.NewInt(12), Extra: []byte("wrong")}
	rpc.resolved = true
	if err := submitter.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.confirmCall != 0 {
		t.Fatal("non-canonical receipt reached confirmation")
	}
	rpc.canonical = &types.Header{Number: big.NewInt(12)}
	rpc.receipt.BlockHash = rpc.canonical.Hash()
	rpc.resolved = false
	if err := submitter.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.confirmCall != 0 {
		t.Fatal("unresolved Gateway request reached confirmation")
	}
	rpc.resolved = true
	if err := submitter.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, _ := repository.Load(context.Background(), item.ID)
	if repository.confirmCall != 1 || loaded.State != Submitted {
		t.Fatalf("receipt-before-indexer calls=%d state=%s", repository.confirmCall, loaded.State)
	}
	repository.confirmErr = nil
	if err := submitter.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, _ = repository.Load(context.Background(), item.ID)
	if loaded.State != Confirmed {
		t.Fatalf("state=%s", loaded.State)
	}
}

func TestStaleRootAfterEstimateDoesNotPersistOrBroadcast(t *testing.T) {
	repository := &submissionRepositoryFake{}
	rpc := &submissionRPCFake{nonce: 1, tip: big.NewInt(1), header: &types.Header{Number: big.NewInt(3), BaseFee: big.NewInt(1)}, estimate: 100000}
	guard := &rootGuardFake{failAt: 2, err: ErrStaleHomeTrustRoot}
	key := mustPrivateKey(t)
	submitter := mustSubmitter(t, rpc, repository, key, guard)
	if _, err := submitter.Submit(context.Background(), submissionIntent(t, crypto.PubkeyToAddress(key.PublicKey))); !errors.Is(err, ErrStaleHomeTrustRoot) {
		t.Fatalf("error=%v", err)
	}
	if len(repository.events) != 0 || len(rpc.sentRaw) != 0 {
		t.Fatalf("events=%v sends=%d", repository.events, len(rpc.sentRaw))
	}
}

func mustSubmitter(t *testing.T, rpc SubmissionRPC, repository SubmissionRepository, key *ecdsa.PrivateKey, guard HomeRootGuard) *TransactionSubmitter {
	t.Helper()
	chainID, _ := domain.NewChainID(10002)
	submitter, err := NewTransactionSubmitter(rpc, repository, chainID, key, 2, guard)
	if err != nil {
		t.Fatal(err)
	}
	return submitter
}
func submissionIntent(t *testing.T, sender common.Address) SubmissionIntent {
	t.Helper()
	requestID := domain.RequestID(common.HexToHash("0x11"))
	chainID, _ := domain.NewChainID(10002)
	return SubmissionIntent{RequestID: requestID, Attempt: 0, PlanID: planner.PlanID(common.HexToHash("0x12")), PlanType: planner.DirectPlan, SnapshotID: trustview.SnapshotID(common.HexToHash("0x13")), HomeChainID: chainID, Gateway: common.HexToAddress("0x2222222222222222222222222222222222222222"), Sender: sender, Calldata: []byte{1, 2, 3}, ExpectedHomeTrustRoot: trustview.TrustRoot{Hash: common.HexToHash("0x14")}}
}
func mustPrivateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}
