package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
)

type OutboxItem struct {
	Envelope      tmp2p.DependencyEvidenceEnvelope
	EvidenceID    evidence.ID
	Attempts      uint64
	NextAttemptAt time.Time
	LastError     string
}
type EvidenceOutboxRepository struct{ db *DB }

var ErrOutboxTransactionRequired = errors.New("dependency evidence outbox writes require the confirmed indexer transaction")

func NewEvidenceOutboxRepository(db *DB) *EvidenceOutboxRepository {
	return &EvidenceOutboxRepository{db: db}
}

// Enqueue is sealed so callers cannot bypass the confirmed indexer's SQL transaction.
func (*EvidenceOutboxRepository) Enqueue(context.Context, tmp2p.DependencyEvidenceEnvelope) (bool, error) {
	return false, ErrOutboxTransactionRequired
}

func (repository *EvidenceOutboxRepository) Pending(ctx context.Context, limit int, now time.Time) ([]OutboxItem, error) {
	if repository == nil || repository.db == nil || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid evidence outbox pending request")
	}
	rows, err := repository.db.sql.QueryContext(ctx, `SELECT envelope,evidence_id,attempts,next_attempt_at,last_error FROM evidence_outbox WHERE state='pending' AND next_attempt_at<=? ORDER BY created_at,message_id LIMIT ?`, toUnix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OutboxItem
	for rows.Next() {
		item, err := scanOutboxItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *EvidenceOutboxRepository) PendingEnvelopes(ctx context.Context, limit int, now time.Time) ([]tmp2p.DependencyEvidenceEnvelope, error) {
	items, err := repository.Pending(ctx, limit, now)
	if err != nil {
		return nil, err
	}
	envelopes := make([]tmp2p.DependencyEvidenceEnvelope, len(items))
	for index := range items {
		envelopes[index] = items[index].Envelope
	}
	return envelopes, nil
}
func scanOutboxItem(row rowScanner) (OutboxItem, error) {
	var encoded, eid []byte
	var attempts, next int64
	var last string
	if err := row.Scan(&encoded, &eid, &attempts, &next, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OutboxItem{}, ErrRecordNotFound
		}
		return OutboxItem{}, err
	}
	envelope, err := tmp2p.ParseDependencyEvidenceEnvelope(encoded)
	if err != nil || len(eid) != 32 {
		return OutboxItem{}, ErrRecordConflict
	}
	var evidenceID evidence.ID
	copy(evidenceID[:], eid)
	return OutboxItem{Envelope: envelope, EvidenceID: evidenceID, Attempts: uint64(attempts), NextAttemptAt: time.Unix(0, next).UTC(), LastError: last}, nil
}
func (repository *EvidenceOutboxRepository) MarkPublished(ctx context.Context, id common.Hash) error {
	now := time.Now().UTC().UnixNano()
	result, err := repository.db.sql.ExecContext(ctx, `UPDATE evidence_outbox SET state='published',attempts=attempts+1,last_error='',updated_at=?,published_at=? WHERE message_id=? AND state='pending'`, now, now, id[:])
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}
func (repository *EvidenceOutboxRepository) MarkRetryable(ctx context.Context, id common.Hash, reason string, next time.Time) error {
	if reason == "" || next.IsZero() {
		return errors.New("outbox retry requires reason and time")
	}
	result, err := repository.db.sql.ExecContext(ctx, `UPDATE evidence_outbox SET attempts=attempts+1,last_error=?,next_attempt_at=?,updated_at=? WHERE message_id=? AND state='pending'`, reason, toUnix(next), time.Now().UTC().UnixNano(), id[:])
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}
