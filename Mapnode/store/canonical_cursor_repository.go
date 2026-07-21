package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type CanonicalCursorRepository struct{ db *DB }

func NewCanonicalCursorRepository(db *DB) *CanonicalCursorRepository {
	return &CanonicalCursorRepository{db: db}
}

func (repository *CanonicalCursorRepository) Initialize(ctx context.Context, cursor reorg.CanonicalCursor) (reorg.CanonicalCursor, bool, error) {
	if repository == nil || repository.db == nil {
		return reorg.CanonicalCursor{}, false, errors.New("nil canonical cursor repository database")
	}
	if cursor.State != reorg.Healthy || cursor.Hash == (common.Hash{}) || cursor.DegradedReason != "" {
		return reorg.CanonicalCursor{}, false, errors.New("initial canonical cursor must be healthy")
	}
	result, err := repository.db.sql.ExecContext(ctx, `INSERT INTO canonical_cursors(chain_id,block_height,block_hash,state,degraded_reason,updated_at)
		VALUES(?,?,?,'healthy','',?) ON CONFLICT(chain_id) DO NOTHING`, cursor.ChainID[:], cursor.Height[:], cursor.Hash[:], time.Now().UTC().UnixNano())
	if err != nil {
		return reorg.CanonicalCursor{}, false, fmt.Errorf("initialize canonical cursor: %w", err)
	}
	affected, _ := result.RowsAffected()
	persisted, err := repository.Load(ctx, cursor.ChainID)
	if err != nil {
		return reorg.CanonicalCursor{}, false, err
	}
	if affected == 0 && persisted != cursor {
		return persisted, false, fmt.Errorf("%w: canonical cursor initialization changed", ErrRecordConflict)
	}
	return persisted, affected == 1, nil
}

func (repository *CanonicalCursorRepository) Load(ctx context.Context, chainID domain.ChainID) (reorg.CanonicalCursor, error) {
	if repository == nil || repository.db == nil {
		return reorg.CanonicalCursor{}, errors.New("nil canonical cursor repository database")
	}
	return scanCanonicalCursor(repository.db.sql.QueryRowContext(ctx, `SELECT chain_id,block_height,block_hash,state,degraded_reason FROM canonical_cursors WHERE chain_id=?`, chainID[:]))
}

func (repository *CanonicalCursorRepository) Advance(ctx context.Context, chainID domain.ChainID, recheckedHash common.Hash, next reorg.CanonicalBlock) (reorg.CanonicalCursor, bool, error) {
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return reorg.CanonicalCursor{}, false, fmt.Errorf("begin canonical cursor advance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanCanonicalCursor(tx.QueryRowContext(ctx, `SELECT chain_id,block_height,block_hash,state,degraded_reason FROM canonical_cursors WHERE chain_id=?`, chainID[:]))
	if err != nil {
		return reorg.CanonicalCursor{}, false, err
	}
	if current.State == reorg.Degraded {
		return current, false, reorg.ErrCursorDegraded
	}
	if current.Height == next.Height {
		if current.Hash != next.Hash {
			return current, false, ErrRecordConflict
		}
		if err := tx.Commit(); err != nil {
			return current, false, err
		}
		return current, false, nil
	}
	advanced, err := current.Advance(recheckedHash, next)
	if errors.Is(err, reorg.ErrCanonicalMismatch) {
		degraded := current.Degrade("persisted canonical block hash no longer matches RPC")
		if _, updateErr := tx.ExecContext(ctx, `UPDATE canonical_cursors SET state='degraded',degraded_reason=?,updated_at=? WHERE chain_id=? AND state='healthy'`, degraded.DegradedReason, time.Now().UTC().UnixNano(), chainID[:]); updateErr != nil {
			return current, false, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return current, false, commitErr
		}
		return degraded, true, reorg.ErrCanonicalMismatch
	}
	if err != nil {
		return current, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE canonical_cursors SET block_height=?,block_hash=?,updated_at=? WHERE chain_id=? AND state='healthy'`, advanced.Height[:], advanced.Hash[:], time.Now().UTC().UnixNano(), chainID[:]); err != nil {
		return current, false, err
	}
	if err := tx.Commit(); err != nil {
		return current, false, err
	}
	return advanced, true, nil
}

func scanCanonicalCursor(row rowScanner) (reorg.CanonicalCursor, error) {
	var cursor reorg.CanonicalCursor
	var chainID, height, hash []byte
	if err := row.Scan(&chainID, &height, &hash, &cursor.State, &cursor.DegradedReason); errors.Is(err, sql.ErrNoRows) {
		return reorg.CanonicalCursor{}, ErrRecordNotFound
	} else if err != nil {
		return reorg.CanonicalCursor{}, fmt.Errorf("scan canonical cursor: %w", err)
	}
	if err := copyExact(cursor.ChainID[:], chainID, "canonical cursor chain ID"); err != nil {
		return reorg.CanonicalCursor{}, err
	}
	if err := copyExact(cursor.Height[:], height, "canonical cursor height"); err != nil {
		return reorg.CanonicalCursor{}, err
	}
	if err := copyExact(cursor.Hash[:], hash, "canonical cursor hash"); err != nil {
		return reorg.CanonicalCursor{}, err
	}
	return cursor, nil
}
