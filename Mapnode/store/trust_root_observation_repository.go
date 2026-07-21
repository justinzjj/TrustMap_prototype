package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
)

type TrustRootObservationRepository struct{ db *DB }

func NewTrustRootObservationRepository(db *DB) *TrustRootObservationRepository {
	return &TrustRootObservationRepository{db: db}
}

func (repository *TrustRootObservationRepository) Save(ctx context.Context, observation trustview.TrustRootObservation) (trustview.TrustRootObservation, trustview.TrustNode, bool, error) {
	if repository == nil || repository.db == nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, errors.New("nil TrustRootObservation repository database")
	}
	if err := observation.Validate(); err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
	}
	if observation.ID != observation.ComputeID() {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, fmt.Errorf("%w: TrustRootObservation ID mismatch", ErrRecordConflict)
	}
	if observation.RequiredConfirmations > math.MaxInt64 {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, errors.New("observation confirmations exceed SQLite INTEGER range")
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	} else {
		observation.ObservedAt = observation.ObservedAt.UTC()
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if persisted, loadErr := loadTrustRootObservation(ctx, tx, observation.ID); loadErr == nil {
		if !sameObservationContent(persisted, observation) {
			return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, ErrRecordConflict
		}
		if err := tx.Commit(); err != nil {
			return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
		}
		return persisted, persisted.TrustNode(), false, nil
	} else if !errors.Is(loadErr, ErrRecordNotFound) {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, loadErr
	}
	if persisted, loadErr := loadTrustRootObservationByTuple(ctx, tx, observation); loadErr == nil {
		if !sameObservationTarget(persisted, observation) {
			return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, fmt.Errorf("%w: TrustRootObservation target changed", ErrRecordConflict)
		}
		if err := tx.Commit(); err != nil {
			return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
		}
		return persisted, persisted.TrustNode(), false, nil
	} else if !errors.Is(loadErr, ErrRecordNotFound) {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, loadErr
	}
	live, err := loadLiveChain(ctx, tx, observation.ChainID)
	if err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
	}
	if live.ValidatedAt.IsZero() || live.Gateway != observation.Gateway || live.GatewayCodeHash != observation.GatewayCodeHash || live.Chain.Confirmations != observation.RequiredConfirmations {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, fmt.Errorf("%w: observation differs from validated live chain", ErrRecordConflict)
	}
	record := observation.SyntheticEvidence()
	if _, err := tx.ExecContext(ctx, `INSERT INTO evidence(id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at)
		VALUES(?,?,?,?,?,?,0,0,?,'candidate','',?,?)`, record.ID[:], record.Locator.ChainID[:], record.Locator.ContractAddress[:], record.Locator.BlockNumber[:], record.Locator.BlockHash[:], record.Locator.TxHash[:], record.Locator.PayloadDigest[:], toUnix(observation.ObservedAt), toUnix(observation.ObservedAt)); err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, fmt.Errorf("insert synthetic observation evidence: %w", err)
	}
	from := evidence.Candidate
	for _, to := range []evidence.State{evidence.Verified, evidence.Confirmed, evidence.Active} {
		if _, err := tx.ExecContext(ctx, "UPDATE evidence SET state=?,updated_at=? WHERE id=? AND state=?", to, toUnix(observation.ObservedAt), record.ID[:], from); err != nil {
			return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_transitions(evidence_id,from_state,to_state,reason,changed_at) VALUES(?,?,?,'TrustRootObservation RPC validation',?)`, record.ID[:], from, to, toUnix(observation.ObservedAt)); err != nil {
			return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
		}
		from = to
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO trust_root_observations(observation_id,evidence_id,chain_id,block_height,block_hash,gateway,trust_root,gateway_code_hash,required_confirmations,confirmed_head_height,confirmed_head_hash,observed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, observation.ID[:], record.ID[:], observation.ChainID[:], observation.Height[:], observation.BlockHash[:], observation.Gateway[:], observation.TrustRoot.Hash[:], observation.GatewayCodeHash[:], int64(observation.RequiredConfirmations), observation.ConfirmedHeadHeight[:], observation.ConfirmedHeadHash[:], toUnix(observation.ObservedAt)); err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, fmt.Errorf("insert TrustRootObservation: %w", err)
	}
	node := observation.TrustNode()
	if _, err := mergeTrustNode(ctx, tx, node); err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
	}
	if err := bumpGraphRevision(ctx, tx); err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return trustview.TrustRootObservation{}, trustview.TrustNode{}, false, err
	}
	return observation, node, true, nil
}

func (repository *TrustRootObservationRepository) Load(ctx context.Context, id trustview.TrustRootObservationID) (trustview.TrustRootObservation, error) {
	if repository == nil || repository.db == nil {
		return trustview.TrustRootObservation{}, errors.New("nil TrustRootObservation repository database")
	}
	return loadTrustRootObservation(ctx, repository.db.sql, id)
}

func loadTrustRootObservation(ctx context.Context, query queryer, id trustview.TrustRootObservationID) (trustview.TrustRootObservation, error) {
	return scanTrustRootObservation(query.QueryRowContext(ctx, `SELECT observation_id,evidence_id,chain_id,block_height,block_hash,gateway,trust_root,gateway_code_hash,required_confirmations,confirmed_head_height,confirmed_head_hash,observed_at
		FROM trust_root_observations WHERE observation_id=?`, id[:]))
}

func loadTrustRootObservationByTuple(ctx context.Context, query queryer, observation trustview.TrustRootObservation) (trustview.TrustRootObservation, error) {
	return scanTrustRootObservation(query.QueryRowContext(ctx, `SELECT observation_id,evidence_id,chain_id,block_height,block_hash,gateway,trust_root,gateway_code_hash,required_confirmations,confirmed_head_height,confirmed_head_hash,observed_at
		FROM trust_root_observations WHERE chain_id=? AND block_height=? AND block_hash=? AND gateway=?`, observation.ChainID[:], observation.Height[:], observation.BlockHash[:], observation.Gateway[:]))
}

func scanTrustRootObservation(row rowScanner) (trustview.TrustRootObservation, error) {
	var result trustview.TrustRootObservation
	var rawID, evidenceID, chainID, height, blockHash, gateway, root, codeHash, headHeight, headHash []byte
	var confirmations, observedAt int64
	err := row.Scan(&rawID, &evidenceID, &chainID, &height, &blockHash, &gateway, &root, &codeHash, &confirmations, &headHeight, &headHash, &observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return trustview.TrustRootObservation{}, ErrRecordNotFound
	}
	if err != nil {
		return trustview.TrustRootObservation{}, fmt.Errorf("load TrustRootObservation: %w", err)
	}
	for _, item := range []struct {
		to    []byte
		from  []byte
		label string
	}{{result.ID[:], rawID, "observation ID"}, {result.ChainID[:], chainID, "observation chain ID"}, {result.Height[:], height, "observation height"}, {result.BlockHash[:], blockHash, "observation block hash"}, {result.Gateway[:], gateway, "observation Gateway"}, {result.TrustRoot.Hash[:], root, "observation TrustRoot"}, {result.GatewayCodeHash[:], codeHash, "observation code hash"}, {result.ConfirmedHeadHeight[:], headHeight, "observation head height"}, {result.ConfirmedHeadHash[:], headHash, "observation head hash"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return trustview.TrustRootObservation{}, err
		}
	}
	result.RequiredConfirmations = uint64(confirmations)
	result.ObservedAt = fromUnix(observedAt)
	syntheticID := result.SyntheticEvidence().ID
	if result.ID != result.ComputeID() || !equalBytes(evidenceID, syntheticID[:]) {
		return trustview.TrustRootObservation{}, ErrRecordConflict
	}
	return result, nil
}

func sameObservationContent(left, right trustview.TrustRootObservation) bool {
	return left.ID == right.ID && left.TrustRootObservationContent == right.TrustRootObservationContent
}

func sameObservationTarget(left, right trustview.TrustRootObservation) bool {
	return left.ChainID == right.ChainID && left.Height == right.Height && left.BlockHash == right.BlockHash &&
		left.Gateway == right.Gateway && left.TrustRoot == right.TrustRoot && left.GatewayCodeHash == right.GatewayCodeHash &&
		left.RequiredConfirmations == right.RequiredConfirmations
}
