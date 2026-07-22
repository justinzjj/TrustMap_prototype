package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/executor"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type TransactionRepository struct{ db *DB }

func NewTransactionRepository(db *DB) *TransactionRepository { return &TransactionRepository{db: db} }

const transactionSubmissionSelect = `SELECT submission_id,request_id,attempt,plan_id,plan_type,snapshot_id,proof_id,home_chain_id,gateway,sender,nonce,calldata_hash,raw_signed_tx,tx_hash,max_fee_per_gas,max_priority_fee_per_gas,gas_limit,state,reason,created_at,updated_at,submitted_at FROM transaction_submissions`

func (repository *TransactionRepository) SavePrepared(ctx context.Context, submission executor.TransactionSubmission) (executor.TransactionSubmission, bool, error) {
	if repository == nil || repository.db == nil {
		return executor.TransactionSubmission{}, false, errors.New("nil transaction repository database")
	}
	if err := submission.ValidatePrepared(); err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	now := time.Now().UTC()
	if submission.CreatedAt.IsZero() {
		submission.CreatedAt = now
	} else {
		submission.CreatedAt = submission.CreatedAt.UTC()
	}
	if submission.UpdatedAt.IsZero() {
		submission.UpdatedAt = submission.CreatedAt
	} else {
		submission.UpdatedAt = submission.UpdatedAt.UTC()
	}
	var proofID any
	if submission.ProofID != nil {
		proofID = submission.ProofID[:]
	}
	result, err := repository.db.sql.ExecContext(ctx, `INSERT INTO transaction_submissions(submission_id,request_id,attempt,plan_id,plan_type,snapshot_id,proof_id,home_chain_id,gateway,sender,nonce,calldata_hash,raw_signed_tx,tx_hash,max_fee_per_gas,max_priority_fee_per_gas,gas_limit,state,reason,created_at,updated_at,submitted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'',?,?,NULL) ON CONFLICT DO NOTHING`,
		submission.ID[:], submission.RequestID[:], int64(submission.Attempt), submission.PlanID[:], string(submission.PlanType), submission.SnapshotID[:], proofID, submission.HomeChainID[:], submission.Gateway[:], submission.Sender[:], int64(submission.Nonce), submission.CalldataHash[:], submission.RawSignedTx, submission.TxHash[:], uint256Blob(submission.MaxFeePerGas), uint256Blob(submission.MaxPriorityFeePerGas), int64(submission.GasLimit), string(executor.Prepared), toUnix(submission.CreatedAt), toUnix(submission.UpdatedAt))
	if err != nil {
		return executor.TransactionSubmission{}, false, fmt.Errorf("persist prepared transaction before broadcast: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	loaded, err := repository.Load(ctx, submission.ID)
	if err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	if executor.ComputeSubmissionID(loaded) != submission.ID || loaded.RequestID != submission.RequestID || loaded.TxHash != submission.TxHash {
		return executor.TransactionSubmission{}, false, ErrRecordConflict
	}
	return loaded, affected == 1, nil
}

func (repository *TransactionRepository) Load(ctx context.Context, id executor.SubmissionID) (executor.TransactionSubmission, error) {
	if repository == nil || repository.db == nil {
		return executor.TransactionSubmission{}, errors.New("nil transaction repository database")
	}
	return scanTransactionSubmission(repository.db.sql.QueryRowContext(ctx, transactionSubmissionSelect+` WHERE submission_id=?`, id[:]))
}

func (repository *TransactionRepository) LoadActive(ctx context.Context, requestID domain.RequestID) (executor.TransactionSubmission, error) {
	if repository == nil || repository.db == nil {
		return executor.TransactionSubmission{}, errors.New("nil transaction repository database")
	}
	return scanTransactionSubmission(repository.db.sql.QueryRowContext(ctx, transactionSubmissionSelect+` WHERE request_id=? AND state IN ('prepared','submitted','retryable') ORDER BY attempt DESC LIMIT 1`, requestID[:]))
}

func (repository *TransactionRepository) LoadLatestForRequest(ctx context.Context, requestID domain.RequestID) (executor.TransactionSubmission, error) {
	if repository == nil || repository.db == nil {
		return executor.TransactionSubmission{}, errors.New("nil transaction repository database")
	}
	return scanTransactionSubmission(repository.db.sql.QueryRowContext(ctx, transactionSubmissionSelect+` WHERE request_id=? ORDER BY attempt DESC LIMIT 1`, requestID[:]))
}

func (repository *TransactionRepository) ListRecoverable(ctx context.Context) ([]executor.TransactionSubmission, error) {
	if repository == nil || repository.db == nil {
		return nil, errors.New("nil transaction repository database")
	}
	rows, err := repository.db.sql.QueryContext(ctx, transactionSubmissionSelect+` WHERE state IN ('prepared','submitted','retryable') ORDER BY nonce,created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var submissions []executor.TransactionSubmission
	for rows.Next() {
		submission, err := scanTransactionSubmission(rows)
		if err != nil {
			return nil, err
		}
		submissions = append(submissions, submission)
	}
	return submissions, rows.Err()
}

func (repository *TransactionRepository) Transition(ctx context.Context, id executor.SubmissionID, from, to executor.ExecutionState, reason string, at time.Time) (executor.TransactionSubmission, bool, error) {
	if repository == nil || repository.db == nil {
		return executor.TransactionSubmission{}, false, errors.New("nil transaction repository database")
	}
	if err := executor.ValidateExecutionTransition(from, to); err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	if (to == executor.Retryable || to == executor.Reverted || to == executor.Superseded) && reason == "" {
		return executor.TransactionSubmission{}, false, errors.New("terminal/retryable execution transition requires reason")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanTransactionSubmission(tx.QueryRowContext(ctx, transactionSubmissionSelect+` WHERE submission_id=?`, id[:]))
	if err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	if current.State == to {
		if err := tx.Commit(); err != nil {
			return executor.TransactionSubmission{}, false, err
		}
		return current, false, nil
	}
	if current.State != from {
		return executor.TransactionSubmission{}, false, ErrConcurrentUpdate
	}
	stateReason := ""
	if to == executor.Retryable || to == executor.Reverted || to == executor.Superseded {
		stateReason = reason
	}
	var submittedAt any
	if to == executor.Submitted {
		submittedAt = toUnix(at)
	} else if current.SubmittedAt != nil {
		submittedAt = toUnix(*current.SubmittedAt)
	}
	result, err := tx.ExecContext(ctx, `UPDATE transaction_submissions SET state=?,reason=?,updated_at=?,submitted_at=? WHERE submission_id=? AND state=?`, string(to), stateReason, toUnix(at), submittedAt, id[:], string(from))
	if err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return executor.TransactionSubmission{}, false, ErrConcurrentUpdate
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_execution_transitions(request_id,submission_id,from_state,to_state,reason,changed_at) VALUES(?,?,?,?,?,?)`, current.RequestID[:], id[:], string(from), string(to), reason, toUnix(at)); err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	updated, err := scanTransactionSubmission(tx.QueryRowContext(ctx, transactionSubmissionSelect+` WHERE submission_id=?`, id[:]))
	if err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return executor.TransactionSubmission{}, false, err
	}
	return updated, true, nil
}

func (repository *TransactionRepository) Confirm(ctx context.Context, receipt executor.ConfirmedTransactionReceipt) error {
	if repository == nil || repository.db == nil || receipt.SubmissionID == (executor.SubmissionID{}) || receipt.RequestID == (domain.RequestID{}) || receipt.TxHash == (common.Hash{}) || receipt.BlockHash == (common.Hash{}) || receipt.Status != 1 || receipt.Confirmations == 0 {
		return errors.New("invalid confirmed transaction receipt")
	}
	if receipt.ConfirmedAt.IsZero() {
		receipt.ConfirmedAt = time.Now().UTC()
	} else {
		receipt.ConfirmedAt = receipt.ConfirmedAt.UTC()
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	submission, err := scanTransactionSubmission(tx.QueryRowContext(ctx, transactionSubmissionSelect+` WHERE submission_id=?`, receipt.SubmissionID[:]))
	if err != nil {
		return err
	}
	if submission.RequestID != receipt.RequestID || submission.TxHash != receipt.TxHash || (submission.State != executor.Submitted && submission.State != executor.Confirmed) {
		return ErrRecordConflict
	}
	blockNumber, _ := domain.NewBlockHeight(receipt.BlockNumber)
	var bundle int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM verification_receipts vr JOIN request_resolutions rr ON rr.request_id=vr.request_id AND rr.chain_id=vr.chain_id AND rr.tx_hash=vr.tx_hash WHERE vr.chain_id=? AND vr.tx_hash=? AND vr.block_number=? AND vr.block_hash=? AND vr.request_id=? AND vr.status=1`, submission.HomeChainID[:], receipt.TxHash[:], blockNumber[:], receipt.BlockHash[:], receipt.RequestID[:]).Scan(&bundle)
	if errors.Is(err, sql.ErrNoRows) {
		return executor.ErrIndexerBundlePending
	}
	if err != nil {
		return err
	}
	if submission.State == executor.Confirmed {
		var persistedBlock []byte
		if err := tx.QueryRowContext(ctx, `SELECT block_hash FROM confirmed_transaction_receipts WHERE submission_id=?`, receipt.SubmissionID[:]).Scan(&persistedBlock); err != nil || !equalBytes(persistedBlock, receipt.BlockHash[:]) {
			return ErrRecordConflict
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO confirmed_transaction_receipts(submission_id,request_id,tx_hash,home_chain_id,block_number,block_hash,status,confirmations,confirmed_at) VALUES(?,?,?,?,?,?,1,?,?)`, receipt.SubmissionID[:], receipt.RequestID[:], receipt.TxHash[:], submission.HomeChainID[:], blockNumber[:], receipt.BlockHash[:], int64(receipt.Confirmations), toUnix(receipt.ConfirmedAt)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE transaction_submissions SET state='confirmed',reason='',updated_at=? WHERE submission_id=? AND state='submitted'`, toUnix(receipt.ConfirmedAt), receipt.SubmissionID[:])
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrConcurrentUpdate
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_execution_transitions(request_id,submission_id,from_state,to_state,reason,changed_at) VALUES(?,?,'submitted','confirmed','',?)`, receipt.RequestID[:], receipt.SubmissionID[:], toUnix(receipt.ConfirmedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func scanTransactionSubmission(row rowScanner) (executor.TransactionSubmission, error) {
	var value executor.TransactionSubmission
	var id, requestID, planID, snapshotID, proofID, chainID, gateway, sender, calldataHash, raw, txHash, maxFee, priority []byte
	var attempt, nonce, gasLimit, createdAt, updatedAt int64
	var planType, state, reason string
	var submittedAt sql.NullInt64
	if err := row.Scan(&id, &requestID, &attempt, &planID, &planType, &snapshotID, &proofID, &chainID, &gateway, &sender, &nonce, &calldataHash, &raw, &txHash, &maxFee, &priority, &gasLimit, &state, &reason, &createdAt, &updatedAt, &submittedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, ErrRecordNotFound
		}
		return value, err
	}
	if len(id) != 32 || len(requestID) != 32 || len(planID) != 32 || len(snapshotID) != 32 || (proofID != nil && len(proofID) != 32) || len(chainID) != 32 || len(gateway) != 20 || len(sender) != 20 || len(calldataHash) != 32 || len(txHash) != 32 || len(maxFee) != 32 || len(priority) != 32 || attempt < 0 || nonce < 0 || gasLimit <= 0 {
		return value, ErrRecordConflict
	}
	copy(value.ID[:], id)
	copy(value.RequestID[:], requestID)
	copy(value.PlanID[:], planID)
	copy(value.SnapshotID[:], snapshotID)
	copy(value.HomeChainID[:], chainID)
	if proofID != nil {
		proof := pathproof.PathProofID{}
		copy(proof[:], proofID)
		value.ProofID = &proof
	}
	value.Attempt, value.PlanType = uint64(attempt), planner.PlanType(planType)
	value.Gateway, value.Sender = common.BytesToAddress(gateway), common.BytesToAddress(sender)
	value.Nonce, value.GasLimit = uint64(nonce), uint64(gasLimit)
	value.CalldataHash, value.RawSignedTx, value.TxHash = common.BytesToHash(calldataHash), append([]byte(nil), raw...), common.BytesToHash(txHash)
	value.MaxFeePerGas, value.MaxPriorityFeePerGas = new(big.Int).SetBytes(maxFee), new(big.Int).SetBytes(priority)
	value.State, value.Reason = executor.ExecutionState(state), reason
	value.CreatedAt, value.UpdatedAt = fromUnix(createdAt), fromUnix(updatedAt)
	if submittedAt.Valid {
		at := fromUnix(submittedAt.Int64)
		value.SubmittedAt = &at
	}
	return value, nil
}

func uint256Blob(value *big.Int) []byte {
	word := make([]byte, 32)
	if value != nil && value.Sign() >= 0 && value.BitLen() <= 256 {
		value.FillBytes(word)
	}
	return word
}
