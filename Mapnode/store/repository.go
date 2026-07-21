package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type EvidenceRepository struct{ db *DB }

func NewEvidenceRepository(db *DB) *EvidenceRepository {
	return &EvidenceRepository{db: db}
}

func (repository *EvidenceRepository) Observe(ctx context.Context, record evidence.Record) (evidence.Record, bool, error) {
	if repository == nil || repository.db == nil {
		return evidence.Record{}, false, errors.New("nil evidence repository database")
	}
	if record.State != evidence.Candidate || record.InvalidReason != "" {
		return evidence.Record{}, false, fmt.Errorf("new evidence must be candidate without an invalid reason")
	}
	wantID, err := evidence.ComputeID(record.Locator)
	if err != nil {
		return evidence.Record{}, false, err
	}
	if record.ID != wantID {
		return evidence.Record{}, false, fmt.Errorf("%w: evidence ID does not match locator", ErrRecordConflict)
	}
	record.CreatedAt, record.UpdatedAt = normalizedTimes(record.CreatedAt, record.UpdatedAt)
	result, err := repository.db.sql.ExecContext(ctx, `INSERT INTO evidence(
		id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		record.ID[:], record.Locator.ChainID[:], record.Locator.ContractAddress[:], record.Locator.BlockNumber[:],
		record.Locator.BlockHash[:], record.Locator.TxHash[:], int64(record.Locator.TxIndex), int64(record.Locator.LogIndex),
		record.Locator.PayloadDigest[:], record.State, record.InvalidReason, toUnix(record.CreatedAt), toUnix(record.UpdatedAt),
	)
	if err != nil {
		return evidence.Record{}, false, fmt.Errorf("insert evidence: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return evidence.Record{}, false, fmt.Errorf("read evidence insert result: %w", err)
	}
	if affected == 1 {
		return record, true, nil
	}
	persisted, err := repository.Load(ctx, record.ID)
	if err != nil {
		if errors.Is(err, ErrRecordNotFound) {
			byLocator, locatorErr := repository.loadByLocator(ctx, record.Locator)
			if locatorErr != nil {
				return evidence.Record{}, false, fmt.Errorf("resolve evidence uniqueness conflict: %w", locatorErr)
			}
			if byLocator.Locator != record.Locator || byLocator.ID == record.ID {
				return evidence.Record{}, false, fmt.Errorf("%w: inconsistent evidence locator conflict", ErrRecordConflict)
			}
			return evidence.Record{}, false, fmt.Errorf("%w: locator belongs to evidence %x", ErrRecordConflict, byLocator.ID)
		}
		return evidence.Record{}, false, err
	}
	if persisted.ID != record.ID || persisted.Locator != record.Locator {
		return evidence.Record{}, false, fmt.Errorf("%w: persisted evidence locator differs", ErrRecordConflict)
	}
	return persisted, false, nil
}

func (repository *EvidenceRepository) loadByLocator(ctx context.Context, locator evidence.Locator) (evidence.Record, error) {
	return scanEvidence(repository.db.sql.QueryRowContext(ctx, evidenceSelect+` WHERE
		chain_id=? AND contract_address=? AND block_number=? AND block_hash=? AND tx_hash=? AND
		tx_index=? AND log_index=? AND payload_digest=?`,
		locator.ChainID[:], locator.ContractAddress[:], locator.BlockNumber[:], locator.BlockHash[:], locator.TxHash[:],
		int64(locator.TxIndex), int64(locator.LogIndex), locator.PayloadDigest[:],
	))
}

func (repository *EvidenceRepository) Load(ctx context.Context, id evidence.ID) (evidence.Record, error) {
	if repository == nil || repository.db == nil {
		return evidence.Record{}, errors.New("nil evidence repository database")
	}
	return scanEvidence(repository.db.sql.QueryRowContext(ctx, evidenceSelect+" WHERE id=?", id[:]))
}

func (repository *EvidenceRepository) Transition(
	ctx context.Context,
	id evidence.ID,
	expectedFrom evidence.State,
	to evidence.State,
	metadata evidence.TransitionMetadata,
) (evidence.Record, bool, error) {
	if repository == nil || repository.db == nil {
		return evidence.Record{}, false, errors.New("nil evidence repository database")
	}
	if err := to.Validate(); err != nil {
		return evidence.Record{}, false, err
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return evidence.Record{}, false, fmt.Errorf("begin evidence transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanEvidence(tx.QueryRowContext(ctx, evidenceSelect+" WHERE id=?", id[:]))
	if err != nil {
		return evidence.Record{}, false, err
	}
	if current.State == to {
		if err := tx.Commit(); err != nil {
			return evidence.Record{}, false, fmt.Errorf("commit idempotent evidence transition: %w", err)
		}
		return current, false, nil
	}
	if current.State != expectedFrom {
		return evidence.Record{}, false, fmt.Errorf("%w: evidence state is %s, expected %s", ErrConcurrentUpdate, current.State, expectedFrom)
	}
	changed, err := evidence.ValidateTransition(expectedFrom, to, metadata.Reason)
	if err != nil {
		return evidence.Record{}, false, err
	}
	if !changed {
		return current, false, nil
	}
	at := metadata.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	invalidReason := ""
	if to == evidence.Invalid {
		invalidReason = metadata.Reason
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE evidence SET state=?,invalid_reason=?,updated_at=? WHERE id=? AND state=?",
		to, invalidReason, toUnix(at), id[:], expectedFrom,
	)
	if err != nil {
		return evidence.Record{}, false, fmt.Errorf("update evidence state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return evidence.Record{}, false, fmt.Errorf("%w: evidence update affected %d rows", ErrConcurrentUpdate, affected)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO evidence_transitions(evidence_id,from_state,to_state,reason,changed_at) VALUES(?,?,?,?,?)",
		id[:], expectedFrom, to, metadata.Reason, toUnix(at),
	); err != nil {
		return evidence.Record{}, false, fmt.Errorf("insert evidence transition: %w", err)
	}
	updated, err := scanEvidence(tx.QueryRowContext(ctx, evidenceSelect+" WHERE id=?", id[:]))
	if err != nil {
		return evidence.Record{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return evidence.Record{}, false, fmt.Errorf("commit evidence transition: %w", err)
	}
	return updated, true, nil
}

type RequestRepository struct{ db *DB }

func NewRequestRepository(db *DB) *RequestRepository {
	return &RequestRepository{db: db}
}

func (repository *RequestRepository) Observe(ctx context.Context, request coordinator.Request) (coordinator.Request, bool, error) {
	if repository == nil || repository.db == nil {
		return coordinator.Request{}, false, errors.New("nil request repository database")
	}
	if request.State != coordinator.Observed {
		return coordinator.Request{}, false, fmt.Errorf("new request must be observed")
	}
	nonce := new(big.Int).SetBytes(request.Nonce[:])
	wantID, err := evidence.ComputeGatewayRequestID(
		request.HomeChainID, request.Gateway, request.Requester, nonce,
		request.SourceChainID, request.SourceHeight, request.SourceBlockHash,
	)
	if err != nil {
		return coordinator.Request{}, false, err
	}
	if request.ID != wantID {
		return coordinator.Request{}, false, fmt.Errorf("%w: request ID does not match fields", ErrRecordConflict)
	}
	request.CreatedAt, request.UpdatedAt = normalizedTimes(request.CreatedAt, request.UpdatedAt)
	result, err := repository.db.sql.ExecContext(ctx, `INSERT INTO requests(
		id,home_chain_id,gateway,requester,nonce,source_chain_id,source_height,source_block_hash,state,reason,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		request.ID[:], request.HomeChainID[:], request.Gateway[:], request.Requester[:], request.Nonce[:],
		request.SourceChainID[:], request.SourceHeight[:], request.SourceBlockHash[:], request.State, request.Reason,
		toUnix(request.CreatedAt), toUnix(request.UpdatedAt),
	)
	if err != nil {
		return coordinator.Request{}, false, fmt.Errorf("insert request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return coordinator.Request{}, false, fmt.Errorf("read request insert result: %w", err)
	}
	if affected == 1 {
		return request, true, nil
	}
	persisted, err := repository.Load(ctx, request.ID)
	if err != nil {
		if errors.Is(err, ErrRecordNotFound) {
			byFields, fieldsErr := repository.loadByFields(ctx, request)
			if fieldsErr != nil {
				return coordinator.Request{}, false, fmt.Errorf("resolve request uniqueness conflict: %w", fieldsErr)
			}
			if !sameRequestFields(byFields, request) || byFields.ID == request.ID {
				return coordinator.Request{}, false, fmt.Errorf("%w: inconsistent request fields conflict", ErrRecordConflict)
			}
			return coordinator.Request{}, false, fmt.Errorf("%w: request fields belong to request %x", ErrRecordConflict, byFields.ID)
		}
		return coordinator.Request{}, false, err
	}
	if !sameRequestIdentity(persisted, request) {
		return coordinator.Request{}, false, fmt.Errorf("%w: persisted request fields differ", ErrRecordConflict)
	}
	return persisted, false, nil
}

func (repository *RequestRepository) loadByFields(ctx context.Context, request coordinator.Request) (coordinator.Request, error) {
	return scanRequest(repository.db.sql.QueryRowContext(ctx, requestSelect+` WHERE
		home_chain_id=? AND gateway=? AND requester=? AND nonce=? AND source_chain_id=? AND source_height=? AND source_block_hash=?`,
		request.HomeChainID[:], request.Gateway[:], request.Requester[:], request.Nonce[:],
		request.SourceChainID[:], request.SourceHeight[:], request.SourceBlockHash[:],
	))
}

func (repository *RequestRepository) Load(ctx context.Context, id domain.RequestID) (coordinator.Request, error) {
	if repository == nil || repository.db == nil {
		return coordinator.Request{}, errors.New("nil request repository database")
	}
	return scanRequest(repository.db.sql.QueryRowContext(ctx, requestSelect+" WHERE id=?", id[:]))
}

func (repository *RequestRepository) Transition(
	ctx context.Context,
	id domain.RequestID,
	expectedFrom coordinator.RequestState,
	to coordinator.RequestState,
	metadata coordinator.TransitionMetadata,
) (coordinator.Request, bool, error) {
	if repository == nil || repository.db == nil {
		return coordinator.Request{}, false, errors.New("nil request repository database")
	}
	if err := to.Validate(); err != nil {
		return coordinator.Request{}, false, err
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return coordinator.Request{}, false, fmt.Errorf("begin request transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanRequest(tx.QueryRowContext(ctx, requestSelect+" WHERE id=?", id[:]))
	if err != nil {
		return coordinator.Request{}, false, err
	}
	if current.State == to {
		if err := tx.Commit(); err != nil {
			return coordinator.Request{}, false, fmt.Errorf("commit idempotent request transition: %w", err)
		}
		return current, false, nil
	}
	if current.State != expectedFrom {
		return coordinator.Request{}, false, fmt.Errorf("%w: request state is %s, expected %s", ErrConcurrentUpdate, current.State, expectedFrom)
	}
	changed, err := coordinator.ValidateTransition(expectedFrom, to)
	if err != nil {
		return coordinator.Request{}, false, err
	}
	if !changed {
		return current, false, nil
	}
	at := metadata.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE requests SET state=?,reason=?,updated_at=? WHERE id=? AND state=?",
		to, metadata.Reason, toUnix(at), id[:], expectedFrom,
	)
	if err != nil {
		return coordinator.Request{}, false, fmt.Errorf("update request state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return coordinator.Request{}, false, fmt.Errorf("%w: request update affected %d rows", ErrConcurrentUpdate, affected)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO request_transitions(request_id,from_state,to_state,reason,changed_at) VALUES(?,?,?,?,?)",
		id[:], expectedFrom, to, metadata.Reason, toUnix(at),
	); err != nil {
		return coordinator.Request{}, false, fmt.Errorf("insert request transition: %w", err)
	}
	updated, err := scanRequest(tx.QueryRowContext(ctx, requestSelect+" WHERE id=?", id[:]))
	if err != nil {
		return coordinator.Request{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return coordinator.Request{}, false, fmt.Errorf("commit request transition: %w", err)
	}
	return updated, true, nil
}

const evidenceSelect = `SELECT id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,
	payload_digest,state,invalid_reason,created_at,updated_at FROM evidence`

type rowScanner interface{ Scan(...any) error }

func scanEvidence(row rowScanner) (evidence.Record, error) {
	var record evidence.Record
	var id, chainID, address, height, blockHash, txHash, payload []byte
	var txIndex, logIndex int64
	var createdAt, updatedAt int64
	if err := row.Scan(
		&id, &chainID, &address, &height, &blockHash, &txHash, &txIndex, &logIndex,
		&payload, &record.State, &record.InvalidReason, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return evidence.Record{}, ErrRecordNotFound
		}
		return evidence.Record{}, fmt.Errorf("scan evidence: %w", err)
	}
	if err := copyExact(record.ID[:], id, "evidence ID"); err != nil {
		return evidence.Record{}, err
	}
	if err := copyExact(record.Locator.ChainID[:], chainID, "evidence chain ID"); err != nil {
		return evidence.Record{}, err
	}
	if err := copyExact(record.Locator.ContractAddress[:], address, "evidence contract address"); err != nil {
		return evidence.Record{}, err
	}
	if err := copyExact(record.Locator.BlockNumber[:], height, "evidence block number"); err != nil {
		return evidence.Record{}, err
	}
	if err := copyExact(record.Locator.BlockHash[:], blockHash, "evidence block hash"); err != nil {
		return evidence.Record{}, err
	}
	if err := copyExact(record.Locator.TxHash[:], txHash, "evidence transaction hash"); err != nil {
		return evidence.Record{}, err
	}
	if err := copyExact(record.Locator.PayloadDigest[:], payload, "evidence payload digest"); err != nil {
		return evidence.Record{}, err
	}
	record.Locator.TxIndex = uint32(txIndex)
	record.Locator.LogIndex = uint32(logIndex)
	record.CreatedAt, record.UpdatedAt = fromUnix(createdAt), fromUnix(updatedAt)
	return record, nil
}

const requestSelect = `SELECT id,home_chain_id,gateway,requester,nonce,source_chain_id,source_height,
	source_block_hash,state,reason,created_at,updated_at FROM requests`

func scanRequest(row rowScanner) (coordinator.Request, error) {
	var request coordinator.Request
	var id, home, gateway, requester, nonce, source, height, blockHash []byte
	var createdAt, updatedAt int64
	if err := row.Scan(
		&id, &home, &gateway, &requester, &nonce, &source, &height, &blockHash,
		&request.State, &request.Reason, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return coordinator.Request{}, ErrRecordNotFound
		}
		return coordinator.Request{}, fmt.Errorf("scan request: %w", err)
	}
	for _, target := range []struct {
		to    []byte
		from  []byte
		label string
	}{
		{request.ID[:], id, "request ID"}, {request.HomeChainID[:], home, "home chain ID"},
		{request.Gateway[:], gateway, "gateway"}, {request.Requester[:], requester, "requester"},
		{request.Nonce[:], nonce, "nonce"}, {request.SourceChainID[:], source, "source chain ID"},
		{request.SourceHeight[:], height, "source height"}, {request.SourceBlockHash[:], blockHash, "source block hash"},
	} {
		if err := copyExact(target.to, target.from, target.label); err != nil {
			return coordinator.Request{}, err
		}
	}
	request.CreatedAt, request.UpdatedAt = fromUnix(createdAt), fromUnix(updatedAt)
	return request, nil
}

func sameRequestIdentity(left, right coordinator.Request) bool {
	return left.ID == right.ID && sameRequestFields(left, right)
}

func sameRequestFields(left, right coordinator.Request) bool {
	return left.HomeChainID == right.HomeChainID && left.Gateway == right.Gateway &&
		left.Requester == right.Requester && left.Nonce == right.Nonce && left.SourceChainID == right.SourceChainID &&
		left.SourceHeight == right.SourceHeight && left.SourceBlockHash == right.SourceBlockHash
}

func copyExact(target, source []byte, label string) error {
	if len(target) != len(source) {
		return fmt.Errorf("corrupt %s length: got %d, want %d", label, len(source), len(target))
	}
	copy(target, source)
	return nil
}

func normalizedTimes(createdAt, updatedAt time.Time) (time.Time, time.Time) {
	now := time.Now().UTC()
	if createdAt.IsZero() {
		createdAt = now
	} else {
		createdAt = createdAt.UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	} else {
		updatedAt = updatedAt.UTC()
	}
	return createdAt, updatedAt
}

func toUnix(value time.Time) int64   { return value.UTC().UnixNano() }
func fromUnix(value int64) time.Time { return time.Unix(0, value).UTC() }

var _ coordinator.RequestRepository = (*RequestRepository)(nil)
