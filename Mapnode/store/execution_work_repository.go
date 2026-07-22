package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type ExecutionWorkRepository struct{ db *DB }

func NewExecutionWorkRepository(db *DB) *ExecutionWorkRepository {
	return &ExecutionWorkRepository{db: db}
}

func (repository *ExecutionWorkRepository) NextRequest(ctx context.Context) (coordinator.Request, bool, error) {
	if repository == nil || repository.db == nil {
		return coordinator.Request{}, false, errors.New("nil execution work repository database")
	}
	var active int
	if err := repository.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM transaction_submissions WHERE state IN ('prepared','submitted','retryable'))`).Scan(&active); err != nil {
		return coordinator.Request{}, false, err
	}
	if active == 1 {
		return coordinator.Request{}, false, nil
	}
	request, err := scanRequest(repository.db.sql.QueryRowContext(ctx, requestSelect+` r WHERE r.state<>'rejected' AND NOT EXISTS(SELECT 1 FROM request_resolutions rr WHERE rr.request_id=r.id) AND NOT EXISTS(SELECT 1 FROM transaction_submissions ts WHERE ts.request_id=r.id AND ts.state='confirmed') ORDER BY r.created_at,r.id LIMIT 1`))
	if errors.Is(err, ErrRecordNotFound) {
		return coordinator.Request{}, false, nil
	}
	return request, err == nil, err
}

func (repository *ExecutionWorkRepository) NextAttempt(ctx context.Context, requestID domain.RequestID) (uint64, error) {
	if repository == nil || repository.db == nil {
		return 0, errors.New("nil execution work repository database")
	}
	var maxAttempt sql.NullInt64
	var state coordinator.RequestState
	if err := repository.db.sql.QueryRowContext(ctx, `SELECT r.state,(SELECT MAX(p.attempt) FROM plans p WHERE p.request_id=r.id) FROM requests r WHERE r.id=?`, requestID[:]).Scan(&state, &maxAttempt); err != nil {
		return 0, err
	}
	if !maxAttempt.Valid {
		return 0, nil
	}
	if state == coordinator.Retryable {
		return uint64(maxAttempt.Int64) + 1, nil
	}
	return uint64(maxAttempt.Int64), nil
}

func (repository *ExecutionWorkRepository) CreateSnapshot(ctx context.Context, request coordinator.Request, attempt uint64, home, target trustview.TrustNode) (trustview.TrustViewSnapshot, error) {
	return NewTrustViewRepository(repository.db).CreateTrustViewSnapshot(ctx, TrustViewSnapshotRequest{RequestID: request.ID, Attempt: attempt, HomeChainID: request.HomeChainID, ExpectedHomeTrustRoot: home.Root, StartNodeID: home.ID, TargetNodeID: target.ID})
}

func (repository *ExecutionWorkRepository) MarkRetryable(ctx context.Context, request coordinator.Request, reason string) error {
	if repository == nil || repository.db == nil || reason == "" {
		return errors.New("retryable request transition requires repository and reason")
	}
	persisted, err := NewRequestRepository(repository.db).Load(ctx, request.ID)
	if err != nil {
		return err
	}
	if persisted.State == coordinator.Retryable {
		return nil
	}
	if persisted.State != request.State {
		return ErrConcurrentUpdate
	}
	_, _, err = NewRequestRepository(repository.db).Transition(ctx, request.ID, persisted.State, coordinator.Retryable, coordinator.TransitionMetadata{At: time.Now().UTC(), Reason: reason})
	if err != nil {
		return fmt.Errorf("mark stale execution plan retryable: %w", err)
	}
	return nil
}
