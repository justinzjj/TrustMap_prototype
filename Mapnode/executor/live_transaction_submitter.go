package executor

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrStaleHomeTrustRoot = errors.New("stale home TrustRoot")
	ErrHomeBlockBoundary  = errors.New("home block already contains a TrustRoot update")
	ErrBroadcastAmbiguous = errors.New("transaction broadcast result is ambiguous")
)

type SubmissionRPC interface {
	PendingNonceAt(context.Context, common.Address) (uint64, error)
	SuggestGasTipCap(context.Context) (*big.Int, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	EstimateGas(context.Context, ethereum.CallMsg) (uint64, error)
	SendTransaction(context.Context, *types.Transaction) error
	TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	RequestResolvedAtHash(context.Context, common.Address, domain.RequestID, common.Hash) (bool, error)
}

type SubmissionRepository interface {
	SavePrepared(context.Context, TransactionSubmission) (TransactionSubmission, bool, error)
	Load(context.Context, SubmissionID) (TransactionSubmission, error)
	ListRecoverable(context.Context) ([]TransactionSubmission, error)
	Transition(context.Context, SubmissionID, ExecutionState, ExecutionState, string, time.Time) (TransactionSubmission, bool, error)
	Confirm(context.Context, ConfirmedTransactionReceipt) error
}

type HomeRootGuard interface {
	Validate(context.Context, trustview.TrustRoot) error
}

type SubmissionIntent struct {
	RequestID             domain.RequestID
	Attempt               uint64
	PlanID                planner.PlanID
	PlanType              planner.PlanType
	SnapshotID            trustview.SnapshotID
	ProofID               *pathproof.PathProofID
	HomeChainID           domain.ChainID
	Gateway               common.Address
	Sender                common.Address
	Calldata              []byte
	ExpectedHomeTrustRoot trustview.TrustRoot
}

type TransactionSubmitter struct {
	rpc               SubmissionRPC
	repository        SubmissionRepository
	chainID           domain.ChainID
	privateKey        *ecdsa.PrivateKey
	sender            common.Address
	confirmationDepth uint64
	guard             HomeRootGuard
	mu                sync.Mutex
}

func NewTransactionSubmitter(rpc SubmissionRPC, repository SubmissionRepository, chainID domain.ChainID, privateKey *ecdsa.PrivateKey, confirmationDepth uint64, guard HomeRootGuard) (*TransactionSubmitter, error) {
	if rpc == nil || repository == nil || chainID.Validate() != nil || privateKey == nil || confirmationDepth == 0 || guard == nil {
		return nil, errors.New("transaction submitter dependencies are required")
	}
	return &TransactionSubmitter{rpc: rpc, repository: repository, chainID: chainID, privateKey: privateKey, sender: crypto.PubkeyToAddress(privateKey.PublicKey), confirmationDepth: confirmationDepth, guard: guard}, nil
}

// Submit is serialized so this process has exactly one pending-nonce owner.
// It persists the exact signed bytes before the first network send.
func (submitter *TransactionSubmitter) Submit(ctx context.Context, intent SubmissionIntent) (TransactionSubmission, error) {
	submitter.mu.Lock()
	defer submitter.mu.Unlock()
	if intent.RequestID == (domain.RequestID{}) || intent.HomeChainID != submitter.chainID || intent.Gateway == (common.Address{}) || intent.Sender != submitter.sender || len(intent.Calldata) == 0 || (intent.PlanType == planner.DirectPlan && intent.ProofID != nil) || (intent.PlanType == planner.PathPlan && intent.ProofID == nil) {
		return TransactionSubmission{}, ErrInvalidSubmission
	}
	if err := submitter.guard.Validate(ctx, intent.ExpectedHomeTrustRoot); err != nil {
		return TransactionSubmission{}, err
	}
	nonce, err := submitter.rpc.PendingNonceAt(ctx, submitter.sender)
	if err != nil {
		return TransactionSubmission{}, err
	}
	tip, err := submitter.rpc.SuggestGasTipCap(ctx)
	if err != nil || tip == nil || tip.Sign() < 0 {
		return TransactionSubmission{}, errors.New("invalid EIP-1559 priority fee")
	}
	header, err := submitter.rpc.HeaderByNumber(ctx, nil)
	if err != nil || header == nil || header.BaseFee == nil || header.BaseFee.Sign() < 0 {
		return TransactionSubmission{}, errors.New("home chain does not provide EIP-1559 base fee")
	}
	gas, err := submitter.rpc.EstimateGas(ctx, ethereum.CallMsg{From: submitter.sender, To: &intent.Gateway, Data: append([]byte(nil), intent.Calldata...)})
	if err != nil || gas == 0 {
		return TransactionSubmission{}, fmt.Errorf("estimate verification transaction: %w", err)
	}
	if err := submitter.guard.Validate(ctx, intent.ExpectedHomeTrustRoot); err != nil {
		return TransactionSubmission{}, err
	}
	feeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
	feeCap.Add(feeCap, tip)
	unsigned := &types.DynamicFeeTx{ChainID: intent.HomeChainID.BigInt(), Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap, Gas: gas, To: &intent.Gateway, Value: new(big.Int), Data: append([]byte(nil), intent.Calldata...)}
	transaction, err := types.SignNewTx(submitter.privateKey, types.NewLondonSigner(intent.HomeChainID.BigInt()), unsigned)
	if err != nil {
		return TransactionSubmission{}, err
	}
	raw, err := transaction.MarshalBinary()
	if err != nil {
		return TransactionSubmission{}, err
	}
	now := time.Now().UTC()
	submission := TransactionSubmission{RequestID: intent.RequestID, Attempt: intent.Attempt, PlanID: intent.PlanID, PlanType: intent.PlanType, SnapshotID: intent.SnapshotID, ProofID: intent.ProofID, HomeChainID: intent.HomeChainID, Gateway: intent.Gateway, Sender: intent.Sender, Nonce: nonce, CalldataHash: crypto.Keccak256Hash(intent.Calldata), RawSignedTx: raw, TxHash: transaction.Hash(), MaxFeePerGas: feeCap, MaxPriorityFeePerGas: tip, GasLimit: gas, State: Prepared, CreatedAt: now, UpdatedAt: now}
	submission.ID = ComputeSubmissionID(submission)
	persisted, _, err := submitter.repository.SavePrepared(ctx, submission)
	if err != nil {
		return TransactionSubmission{}, err
	}
	return submitter.broadcastPrepared(ctx, persisted, transaction)
}

func (submitter *TransactionSubmitter) broadcastPrepared(ctx context.Context, submission TransactionSubmission, transaction *types.Transaction) (TransactionSubmission, error) {
	sendErr := submitter.rpc.SendTransaction(ctx, transaction)
	accepted := sendErr == nil
	if !accepted {
		visible, _, lookupErr := submitter.rpc.TransactionByHash(ctx, submission.TxHash)
		accepted = lookupErr == nil && visible != nil && visible.Hash() == submission.TxHash
	}
	if !accepted {
		return submission, fmt.Errorf("%w: %v", ErrBroadcastAmbiguous, sendErr)
	}
	updated, _, err := submitter.repository.Transition(ctx, submission.ID, submission.State, Submitted, "rpc accepted or hash visible", time.Now().UTC())
	if err != nil {
		return submission, err
	}
	return updated, nil
}

// Recover checks every durable raw transaction before considering any new
// nonce. Re-broadcast always unmarshals the saved bytes.
func (submitter *TransactionSubmitter) Recover(ctx context.Context) error {
	submitter.mu.Lock()
	defer submitter.mu.Unlock()
	items, err := submitter.repository.ListRecoverable(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := submitter.reconcile(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (submitter *TransactionSubmitter) reconcile(ctx context.Context, item TransactionSubmission) error {
	receipt, receiptErr := submitter.rpc.TransactionReceipt(ctx, item.TxHash)
	if receiptErr == nil && receipt != nil {
		return submitter.reconcileReceipt(ctx, item, receipt)
	}
	if receiptErr != nil && !errors.Is(receiptErr, ethereum.NotFound) {
		return receiptErr
	}
	visible, _, lookupErr := submitter.rpc.TransactionByHash(ctx, item.TxHash)
	if lookupErr == nil && visible != nil && visible.Hash() == item.TxHash {
		if item.State != Submitted {
			_, _, err := submitter.repository.Transition(ctx, item.ID, item.State, Submitted, "transaction hash visible during recovery", time.Now().UTC())
			return err
		}
		return nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, ethereum.NotFound) {
		return lookupErr
	}
	transaction := new(types.Transaction)
	if err := transaction.UnmarshalBinary(item.RawSignedTx); err != nil || transaction.Hash() != item.TxHash {
		return ErrInvalidSubmission
	}
	_, err := submitter.broadcastPrepared(ctx, item, transaction)
	if errors.Is(err, ErrBroadcastAmbiguous) {
		// Preserve Prepared/Retryable for the next recovery cycle. An ambiguous
		// response never authorizes signing a replacement nonce.
		return nil
	}
	return err
}

func (submitter *TransactionSubmitter) reconcileReceipt(ctx context.Context, item TransactionSubmission, receipt *types.Receipt) error {
	if receipt.TxHash != item.TxHash || receipt.BlockNumber == nil || receipt.BlockHash == (common.Hash{}) {
		return ErrInvalidSubmission
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		if item.State == Reverted {
			return nil
		}
		_, _, err := submitter.repository.Transition(ctx, item.ID, item.State, Reverted, "verification transaction reverted", time.Now().UTC())
		return err
	}
	if item.State != Submitted {
		updated, _, err := submitter.repository.Transition(ctx, item.ID, item.State, Submitted, "successful receipt observed", time.Now().UTC())
		if err != nil {
			return err
		}
		item = updated
	}
	head, err := submitter.rpc.HeaderByNumber(ctx, nil)
	if err != nil || head == nil || head.Number == nil || head.Number.Sign() < 0 || head.Number.Uint64() < receipt.BlockNumber.Uint64()+submitter.confirmationDepth-1 {
		return nil
	}
	canonical, err := submitter.rpc.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil || canonical == nil || canonical.Hash() != receipt.BlockHash {
		return nil
	}
	resolved, err := submitter.rpc.RequestResolvedAtHash(ctx, item.Gateway, item.RequestID, receipt.BlockHash)
	if err != nil {
		return err
	}
	if !resolved {
		return nil
	}
	confirmations := head.Number.Uint64() - receipt.BlockNumber.Uint64() + 1
	err = submitter.repository.Confirm(ctx, ConfirmedTransactionReceipt{SubmissionID: item.ID, RequestID: item.RequestID, TxHash: item.TxHash, BlockNumber: receipt.BlockNumber.Uint64(), BlockHash: receipt.BlockHash, Status: receipt.Status, Confirmations: confirmations, ConfirmedAt: time.Now().UTC()})
	if errors.Is(err, ErrIndexerBundlePending) {
		return nil
	}
	return err
}
