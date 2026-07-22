package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

type RemoteDependencyRepository struct{ db *DB }

func NewRemoteDependencyRepository(db *DB) *RemoteDependencyRepository {
	return &RemoteDependencyRepository{db: db}
}

func (repository *RemoteDependencyRepository) Activate(ctx context.Context, messageID common.Hash, validation evidence.RemoteDependencyValidation) (bool, error) {
	if repository == nil || repository.db == nil {
		return false, errors.New("nil remote dependency repository database")
	}
	if validation.Record.State != evidence.Candidate || validation.Record.InvalidReason != "" || len(validation.WitnessSiblings) < 1 || len(validation.WitnessSiblings) > int(internalproof.MaxTreeDepth) || validation.PathStepCostGas == 0 {
		return false, ErrEvidenceBinding
	}
	wantID, err := evidence.ComputeID(validation.Record.Locator)
	if err != nil || wantID != validation.Record.ID {
		return false, ErrEvidenceBinding
	}
	dependency := trustview.NewVerifiedDependency(validation.Dependency.RequestID, validation.Dependency.SourceChainID, validation.Dependency.SourceHeight, validation.Dependency.SourceBlockHash, trustview.TrustRoot{Hash: validation.Dependency.SourceTrustRoot}, validation.Dependency.LeafIndex, validation.Record.ID)
	if dependency.DependencyKey != validation.Dependency.DependencyKey {
		return false, ErrEvidenceBinding
	}
	from := trustview.NewTrustNode(trustview.NodeKey{ChainID: validation.HomeObservation.ChainID, Height: validation.HomeObservation.Height, BlockHash: validation.HomeObservation.BlockHash}, trustview.TrustRoot{Hash: validation.HomeObservation.TrustRoot}, validation.HomeObservation.EvidenceID)
	to := trustview.NewTrustNode(trustview.NodeKey{ChainID: validation.SourceObservation.ChainID, Height: validation.SourceObservation.Height, BlockHash: validation.SourceObservation.BlockHash}, trustview.TrustRoot{Hash: validation.SourceObservation.TrustRoot}, validation.SourceObservation.EvidenceID)
	if validation.HomeTrustRoot != from.Root.Hash || from.Key.ChainID != validation.Record.Locator.ChainID || from.Key.Height != validation.Record.Locator.BlockNumber || from.Key.BlockHash != validation.Record.Locator.BlockHash || to.Key.ChainID != dependency.SourceChainID || to.Key.Height != dependency.SourceHeight || to.Key.BlockHash != dependency.SourceBlockHash || to.Root != dependency.SourceTrustRoot {
		return false, ErrEvidenceBinding
	}
	if err := dependency.Validate(to, validation.Record.Locator.PayloadDigest); err != nil {
		return false, ErrEvidenceBinding
	}
	witness := trustview.NewMembershipWitness(validation.Record.ID, dependency.LeafIndex, validation.WitnessSiblings)
	leaf := domain.LeafHash(dependency.SourceTrustRoot.Hash, dependency.SourceBlockHash)
	root, err := internalproof.RootFromWitness(leaf, witness.LeafIndex, witness.Siblings, uint8(len(witness.Siblings)))
	if err != nil || root != from.Root.Hash {
		return false, ErrEvidenceBinding
	}
	edge, err := trustview.NewTrustEdge(from.ID, to.ID, validation.Record.ID, dependency.LeafIndex, &witness.ID, validation.PathStepCostGas)
	if err != nil {
		return false, err
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var inboxEvidence []byte
	var inboxState string
	if err := tx.QueryRowContext(ctx, `SELECT evidence_id,state FROM evidence_inbox WHERE message_id=?`, messageID[:]).Scan(&inboxEvidence, &inboxState); err != nil || !equalBytes(inboxEvidence, validation.Record.ID[:]) {
		return false, ErrEvidenceBinding
	}
	if inboxState == string(InboxInvalid) {
		return false, ErrEvidenceBinding
	}
	record, err := scanEvidence(tx.QueryRowContext(ctx, evidenceSelect+" WHERE id=?", validation.Record.ID[:]))
	if err != nil || record.Locator != validation.Record.Locator {
		return false, ErrEvidenceBinding
	}
	if record.State != evidence.Candidate && record.State != evidence.Active {
		return false, ErrEvidenceBinding
	}
	if err := verifyActiveNodeContent(ctx, tx, from); err != nil {
		return false, err
	}
	if err := verifyActiveNodeContent(ctx, tx, to); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	changed := false
	if record.State == evidence.Candidate {
		fromState := evidence.Candidate
		for _, toState := range []evidence.State{evidence.Verified, evidence.Confirmed, evidence.Active} {
			result, err := tx.ExecContext(ctx, `UPDATE evidence SET state=?,updated_at=? WHERE id=? AND state=?`, toState, toUnix(now), validation.Record.ID[:], fromState)
			if err != nil {
				return false, err
			}
			affected, _ := result.RowsAffected()
			if affected != 1 {
				return false, ErrConcurrentUpdate
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_transitions(evidence_id,from_state,to_state,reason,changed_at) VALUES(?,?,?,'remote canonical Gateway receipt validation',?)`, validation.Record.ID[:], fromState, toState, toUnix(now)); err != nil {
				return false, err
			}
			fromState = toState
		}
		changed = true
	}
	var existingWitness []byte
	err = tx.QueryRowContext(ctx, `SELECT witness_id FROM membership_witnesses WHERE evidence_id=?`, validation.Record.ID[:]).Scan(&existingWitness)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO membership_witnesses(witness_id,evidence_id,leaf_index,created_at) VALUES(?,?,?,?)`, witness.ID[:], witness.EvidenceID[:], int64(witness.LeafIndex), toUnix(now)); err != nil {
			return false, err
		}
		for index, sibling := range witness.Siblings {
			if _, err := tx.ExecContext(ctx, `INSERT INTO membership_witness_siblings(witness_id,sibling_index,sibling_hash) VALUES(?,?,?)`, witness.ID[:], index, sibling[:]); err != nil {
				return false, err
			}
		}
		changed = true
	} else if err != nil || !equalBytes(existingWitness, witness.ID[:]) {
		return false, ErrGraphConflict
	}
	edgeChanged, err := mergeTrustEdge(ctx, tx, edge)
	if err != nil {
		return false, err
	}
	if edgeChanged {
		changed = true
		if err := bumpGraphRevision(ctx, tx); err != nil {
			return false, err
		}
	}
	if inboxState != string(InboxValidated) {
		result, err := tx.ExecContext(ctx, `UPDATE evidence_inbox SET state='validated',invalid_reason='',updated_at=? WHERE message_id=? AND state IN ('pending','retryable')`, toUnix(now), messageID[:])
		if err != nil {
			return false, err
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return false, ErrConcurrentUpdate
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit remote dependency activation: %w", err)
	}
	return changed, nil
}
