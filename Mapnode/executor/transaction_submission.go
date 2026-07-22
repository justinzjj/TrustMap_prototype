package executor

import (
	"encoding/binary"
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrSubmissionIDMismatch = errors.New("TransactionSubmission ID does not match content")
	ErrInvalidSubmission    = errors.New("invalid TransactionSubmission")
	ErrInvalidTransition    = errors.New("invalid execution transition")
	ErrIndexerBundlePending = errors.New("exact confirmed indexer bundle is not durable")
)

type SubmissionID [32]byte
type ExecutionState string

const (
	Prepared   ExecutionState = "prepared"
	Submitted  ExecutionState = "submitted"
	Confirmed  ExecutionState = "confirmed"
	Retryable  ExecutionState = "retryable"
	Reverted   ExecutionState = "reverted"
	Superseded ExecutionState = "superseded"
)

type TransactionSubmission struct {
	ID                   SubmissionID
	RequestID            domain.RequestID
	Attempt              uint64
	PlanID               planner.PlanID
	PlanType             planner.PlanType
	SnapshotID           trustview.SnapshotID
	ProofID              *pathproof.PathProofID
	HomeChainID          domain.ChainID
	Gateway              common.Address
	Sender               common.Address
	Nonce                uint64
	CalldataHash         common.Hash
	RawSignedTx          []byte
	TxHash               common.Hash
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int
	GasLimit             uint64
	State                ExecutionState
	Reason               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	SubmittedAt          *time.Time
}

type ConfirmedTransactionReceipt struct {
	SubmissionID  SubmissionID
	RequestID     domain.RequestID
	TxHash        common.Hash
	BlockNumber   uint64
	BlockHash     common.Hash
	Status        uint64
	Confirmations uint64
	ConfirmedAt   time.Time
}

func (submission TransactionSubmission) Clone() TransactionSubmission {
	copy := submission
	copy.RawSignedTx = append([]byte(nil), submission.RawSignedTx...)
	copy.MaxFeePerGas = cloneBig(submission.MaxFeePerGas)
	copy.MaxPriorityFeePerGas = cloneBig(submission.MaxPriorityFeePerGas)
	if submission.ProofID != nil {
		proofID := *submission.ProofID
		copy.ProofID = &proofID
	}
	if submission.SubmittedAt != nil {
		at := *submission.SubmittedAt
		copy.SubmittedAt = &at
	}
	return copy
}

func ComputeSubmissionID(submission TransactionSubmission) SubmissionID {
	encoded := make([]byte, 0, 512+len(submission.RawSignedTx))
	encoded = appendLength(encoded, []byte("TrustMap/TransactionSubmission/ID/v1"))
	encoded = append(encoded, submission.RequestID[:]...)
	encoded = appendUint64(encoded, submission.Attempt)
	encoded = append(encoded, submission.PlanID[:]...)
	encoded = appendLength(encoded, []byte(submission.PlanType))
	encoded = append(encoded, submission.SnapshotID[:]...)
	if submission.ProofID == nil {
		encoded = append(encoded, 0)
	} else {
		encoded = append(encoded, 1)
		encoded = append(encoded, submission.ProofID[:]...)
	}
	encoded = append(encoded, submission.HomeChainID[:]...)
	encoded = append(encoded, submission.Gateway[:]...)
	encoded = append(encoded, submission.Sender[:]...)
	encoded = appendUint64(encoded, submission.Nonce)
	encoded = append(encoded, submission.CalldataHash[:]...)
	encoded = appendLength(encoded, submission.RawSignedTx)
	encoded = append(encoded, submission.TxHash[:]...)
	encoded = appendBigWord(encoded, submission.MaxFeePerGas)
	encoded = appendBigWord(encoded, submission.MaxPriorityFeePerGas)
	encoded = appendUint64(encoded, submission.GasLimit)
	return SubmissionID(crypto.Keccak256Hash(encoded))
}

func (submission TransactionSubmission) ValidatePrepared() error {
	if submission.RequestID == (domain.RequestID{}) || submission.PlanID == (planner.PlanID{}) || submission.SnapshotID == (trustview.SnapshotID{}) || submission.HomeChainID.Validate() != nil || submission.Gateway == (common.Address{}) || submission.Sender == (common.Address{}) || submission.CalldataHash == (common.Hash{}) || submission.TxHash == (common.Hash{}) || len(submission.RawSignedTx) == 0 || len(submission.RawSignedTx) > 1024*1024 || submission.GasLimit == 0 || submission.MaxFeePerGas == nil || submission.MaxPriorityFeePerGas == nil || submission.MaxFeePerGas.Sign() <= 0 || submission.MaxPriorityFeePerGas.Sign() < 0 || submission.MaxFeePerGas.Cmp(submission.MaxPriorityFeePerGas) < 0 || submission.MaxFeePerGas.BitLen() > 256 || submission.MaxPriorityFeePerGas.BitLen() > 256 || submission.State != Prepared || submission.Reason != "" {
		return ErrInvalidSubmission
	}
	if submission.PlanType == planner.PathPlan {
		if submission.ProofID == nil {
			return ErrInvalidSubmission
		}
	} else if submission.PlanType != planner.DirectPlan || submission.ProofID != nil {
		return ErrInvalidSubmission
	}
	if submission.ID != ComputeSubmissionID(submission) {
		return ErrSubmissionIDMismatch
	}
	if crypto.Keccak256Hash(submission.RawSignedTx) != submission.TxHash {
		return ErrInvalidSubmission
	}
	return nil
}

func ValidateExecutionTransition(from, to ExecutionState) error {
	legal := (from == Prepared && (to == Submitted || to == Retryable || to == Reverted || to == Superseded)) ||
		(from == Submitted && (to == Confirmed || to == Retryable || to == Reverted || to == Superseded)) ||
		(from == Retryable && (to == Submitted || to == Reverted || to == Superseded))
	if from == to || !legal {
		return ErrInvalidTransition
	}
	return nil
}

func appendLength(destination, value []byte) []byte {
	length := [4]byte{}
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	destination = append(destination, length[:]...)
	return append(destination, value...)
}

func appendUint64(destination []byte, value uint64) []byte {
	word := [8]byte{}
	binary.BigEndian.PutUint64(word[:], value)
	return append(destination, word[:]...)
}

func appendBigWord(destination []byte, value *big.Int) []byte {
	word := [32]byte{}
	if value != nil && value.Sign() >= 0 && value.BitLen() <= 256 {
		value.FillBytes(word[:])
	}
	return append(destination, word[:]...)
}

func cloneBig(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}
