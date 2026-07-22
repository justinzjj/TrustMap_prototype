package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

type InboxState string

const (
	InboxPending   InboxState = "pending"
	InboxRetryable InboxState = "retryable"
	InboxValidated InboxState = "validated"
	InboxInvalid   InboxState = "invalid"
)

type InboxItem struct {
	Envelope      tmp2p.DependencyEvidenceEnvelope
	EvidenceID    evidence.ID
	OriginPeer    peer.ID
	State         InboxState
	InvalidReason string
	Attempts      uint64
	NextAttemptAt time.Time
}

type EvidenceInboxRepository struct{ db *DB }

func NewEvidenceInboxRepository(db *DB) *EvidenceInboxRepository {
	return &EvidenceInboxRepository{db: db}
}

func (repository *EvidenceInboxRepository) Receive(ctx context.Context, envelope tmp2p.DependencyEvidenceEnvelope, sender peer.ID) (evidence.Record, bool, error) {
	if repository == nil || repository.db == nil {
		return evidence.Record{}, false, errors.New("nil evidence inbox database")
	}
	if err := envelope.ValidateFrom(sender); err != nil {
		return evidence.Record{}, false, err
	}
	encoded, err := envelope.MarshalBinary()
	if err != nil {
		return evidence.Record{}, false, err
	}
	locator := envelope.Locator()
	evidenceID, err := evidence.ComputeID(locator)
	if err != nil {
		return evidence.Record{}, false, err
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return evidence.Record{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO evidence(id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'candidate','',?,?) ON CONFLICT(id) DO NOTHING`, evidenceID[:], locator.ChainID[:], locator.ContractAddress[:], locator.BlockNumber[:], locator.BlockHash[:], locator.TxHash[:], int64(locator.TxIndex), int64(locator.LogIndex), locator.PayloadDigest[:], toUnix(now), toUnix(now))
	if err != nil {
		return evidence.Record{}, false, err
	}
	_, err = oneRowChanged(result)
	if err != nil {
		return evidence.Record{}, false, err
	}
	record, err := scanEvidence(tx.QueryRowContext(ctx, evidenceSelect+" WHERE id=?", evidenceID[:]))
	if err != nil || record.Locator != locator {
		return evidence.Record{}, false, ErrRecordConflict
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO evidence_inbox(message_id,evidence_id,protocol_version,origin_peer,observed_at,envelope,state,invalid_reason,attempts,next_attempt_at,received_at,updated_at) VALUES(?,?,?,?,?,?,'pending','',0,?,?,?) ON CONFLICT(message_id) DO NOTHING`, envelope.MessageID[:], evidenceID[:], int64(envelope.ProtocolVersion), sender.String(), envelope.ObservedAt.UnixNano(), encoded, toUnix(now), toUnix(now), toUnix(now))
	if err != nil {
		return evidence.Record{}, false, err
	}
	inserted, err := oneRowChanged(result)
	if err != nil {
		return evidence.Record{}, false, err
	}
	if !inserted {
		var storedEnvelope, storedEvidence []byte
		var origin string
		if err := tx.QueryRowContext(ctx, `SELECT envelope,evidence_id,origin_peer FROM evidence_inbox WHERE message_id=?`, envelope.MessageID[:]).Scan(&storedEnvelope, &storedEvidence, &origin); err != nil || !equalBytes(storedEnvelope, encoded) || !equalBytes(storedEvidence, evidenceID[:]) || origin != sender.String() {
			return evidence.Record{}, false, ErrRecordConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return evidence.Record{}, false, err
	}
	return record, inserted, nil
}

func (repository *EvidenceInboxRepository) Pending(ctx context.Context, limit int, now time.Time) ([]InboxItem, error) {
	if repository == nil || repository.db == nil || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid evidence inbox pending request")
	}
	rows, err := repository.db.sql.QueryContext(ctx, `SELECT envelope,evidence_id,origin_peer,state,invalid_reason,attempts,next_attempt_at FROM evidence_inbox WHERE state IN ('pending','retryable') AND next_attempt_at<=? ORDER BY received_at,message_id LIMIT ?`, toUnix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []InboxItem
	for rows.Next() {
		item, err := scanInboxItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *EvidenceInboxRepository) Load(ctx context.Context, id common.Hash) (InboxItem, error) {
	if repository == nil || repository.db == nil {
		return InboxItem{}, errors.New("nil evidence inbox database")
	}
	return scanInboxItem(repository.db.sql.QueryRowContext(ctx, `SELECT envelope,evidence_id,origin_peer,state,invalid_reason,attempts,next_attempt_at FROM evidence_inbox WHERE message_id=?`, id[:]))
}

func scanInboxItem(row rowScanner) (InboxItem, error) {
	var encoded, evidenceID []byte
	var origin, state, reason string
	var attempts, next int64
	if err := row.Scan(&encoded, &evidenceID, &origin, &state, &reason, &attempts, &next); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return InboxItem{}, ErrRecordNotFound
		}
		return InboxItem{}, err
	}
	envelope, err := tmp2p.ParseDependencyEvidenceEnvelope(encoded)
	if err != nil || len(evidenceID) != 32 {
		return InboxItem{}, ErrRecordConflict
	}
	id, err := peer.Decode(origin)
	if err != nil || id != envelope.OriginPeer {
		return InboxItem{}, ErrRecordConflict
	}
	var eid evidence.ID
	copy(eid[:], evidenceID)
	return InboxItem{Envelope: envelope, EvidenceID: eid, OriginPeer: id, State: InboxState(state), InvalidReason: reason, Attempts: uint64(attempts), NextAttemptAt: time.Unix(0, next).UTC()}, nil
}

func (repository *EvidenceInboxRepository) MarkRetryable(ctx context.Context, id common.Hash, reason string, next time.Time) error {
	if reason == "" || next.IsZero() {
		return errors.New("retryable inbox failure requires reason and retry time")
	}
	result, err := repository.db.sql.ExecContext(ctx, `UPDATE evidence_inbox SET state='retryable',attempts=attempts+1,next_attempt_at=?,updated_at=? WHERE message_id=? AND state IN ('pending','retryable')`, toUnix(next), time.Now().UTC().UnixNano(), id[:])
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (repository *EvidenceInboxRepository) MarkInvalid(ctx context.Context, id common.Hash, reason string) error {
	if repository == nil || repository.db == nil || reason == "" {
		return errors.New("invalid inbox failure requires repository and reason")
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixNano()
	var evidenceID []byte
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT evidence_id,state FROM evidence_inbox WHERE message_id=?`, id[:]).Scan(&evidenceID, &state); err != nil {
		return err
	}
	if state == string(InboxValidated) {
		return ErrRecordConflict
	}
	if state == string(InboxInvalid) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE evidence_inbox SET state='invalid',invalid_reason=?,attempts=attempts+1,updated_at=? WHERE message_id=?`, reason, now, id[:]); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE evidence SET state='invalid',invalid_reason=?,updated_at=? WHERE id=? AND state='candidate'`, reason, now, evidenceID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_transitions(evidence_id,from_state,to_state,reason,changed_at) VALUES(?,'candidate','invalid',?,?)`, evidenceID, reason, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (repository *EvidenceInboxRepository) MarkValidated(ctx context.Context, id common.Hash) error {
	result, err := repository.db.sql.ExecContext(ctx, `UPDATE evidence_inbox SET state='validated',invalid_reason='',updated_at=? WHERE message_id=? AND state IN ('pending','retryable')`, time.Now().UTC().UnixNano(), id[:])
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return fmt.Errorf("%w: inbox validation", ErrConcurrentUpdate)
	}
	return nil
}
