package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type PlanRepository struct{ db *DB }

func NewPlanRepository(db *DB) *PlanRepository { return &PlanRepository{db: db} }

func (repository *PlanRepository) SavePlan(ctx context.Context, plan planner.Plan) error {
	if repository == nil || repository.db == nil {
		return errors.New("nil plan repository database")
	}
	if err := validatePlanForStorage(plan); err != nil {
		return err
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plan save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentID []byte
	var currentAttempt int64
	err = tx.QueryRowContext(ctx, `SELECT p.plan_id,p.attempt FROM request_current_plan c
		JOIN plans p ON p.plan_id=c.plan_id WHERE c.request_id=?`, plan.RequestID[:]).Scan(&currentID, &currentAttempt)
	if err == nil {
		if plan.Attempt < uint64(currentAttempt) {
			return ErrStalePlanningAttempt
		}
		if plan.Attempt == uint64(currentAttempt) {
			if !equalBytes(currentID, plan.ID[:]) {
				return ErrRecordConflict
			}
			persisted, loadErr := loadPlan(ctx, tx, plan.ID)
			if loadErr != nil {
				return loadErr
			}
			if !samePlanDecision(persisted, plan) {
				return ErrRecordConflict
			}
			return tx.Commit()
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load current planning attempt: %w", err)
	}
	if err := validatePlanSnapshotBinding(ctx, tx, plan); err != nil {
		return err
	}
	if persisted, loadErr := loadPlan(ctx, tx, plan.ID); loadErr == nil {
		if !samePlanDecision(persisted, plan) {
			return ErrRecordConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_current_plan(request_id,plan_id) VALUES(?,?)
			ON CONFLICT(request_id) DO UPDATE SET plan_id=excluded.plan_id`, plan.RequestID[:], plan.ID[:]); err != nil {
			return err
		}
		return tx.Commit()
	} else if !errors.Is(loadErr, planner.ErrPlanNotFound) {
		return loadErr
	}
	var conflicting []byte
	err = tx.QueryRowContext(ctx, "SELECT plan_id FROM plans WHERE request_id=? AND attempt=?", plan.RequestID[:], int64(plan.Attempt)).Scan(&conflicting)
	if err == nil {
		return ErrRecordConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check planning attempt: %w", err)
	}
	pathCost, directCost := nullableCost(plan.PathCost), nullableCost(plan.DirectCost)
	createdAt := plan.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO plans(
		plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, plan.ID[:], plan.RequestID[:], plan.SnapshotID[:], plan.ProfileID, plan.ProfileFingerprint[:], int64(plan.Attempt),
		plan.Type, plan.HomeNodeID[:], plan.TargetNodeID[:], len(plan.Hops), int64(plan.PathStepCost), pathCost, directCost, plan.FallbackReason, toUnix(createdAt))
	if err != nil {
		return fmt.Errorf("insert plan: %w", err)
	}
	for index, edgeID := range plan.Hops {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plan_hops(plan_id,snapshot_id,hop_index,edge_id) VALUES(?,?,?,?)`,
			plan.ID[:], plan.SnapshotID[:], index, edgeID[:]); err != nil {
			return fmt.Errorf("insert plan hop %d: %w", index, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_current_plan(request_id,plan_id) VALUES(?,?)
		ON CONFLICT(request_id) DO UPDATE SET plan_id=excluded.plan_id`, plan.RequestID[:], plan.ID[:]); err != nil {
		return fmt.Errorf("bind current plan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plan: %w", err)
	}
	return nil
}

func validatePlanForStorage(plan planner.Plan) error {
	if plan.ID == (planner.PlanID{}) || plan.RequestID == ([32]byte{}) || plan.SnapshotID == (trustview.SnapshotID{}) ||
		plan.HomeNodeID == (trustview.NodeID{}) || plan.TargetNodeID == (trustview.NodeID{}) || plan.ProfileID == "" {
		return errors.New("plan identity fields are required")
	}
	if plan.Attempt > math.MaxInt64 || plan.PathStepCost > math.MaxInt64 {
		return errors.New("plan integer exceeds SQLite range")
	}
	for _, cost := range []*uint64{plan.PathCost, plan.DirectCost} {
		if cost != nil && *cost > math.MaxInt64 {
			return errors.New("plan cost exceeds SQLite range")
		}
	}
	if len(plan.Hops) > 0 {
		if plan.PathStepCost == 0 || plan.PathCost == nil || uint64(len(plan.Hops)) > math.MaxUint64/plan.PathStepCost || *plan.PathCost != uint64(len(plan.Hops))*plan.PathStepCost {
			return errors.New("plan path cost does not match hop count and trusted step cost")
		}
	} else if plan.PathCost != nil || plan.PathStepCost != 0 {
		return errors.New("zero-hop plan must not persist a path cost")
	}

	switch plan.Type {
	case planner.PathPlan:
		if len(plan.Hops) == 0 || plan.PathCost == nil || plan.DirectCost == nil || plan.FallbackReason != "" {
			return errors.New("invalid PathPlan persistence fields")
		}
	case planner.DirectPlan:
		if plan.FallbackReason == "" {
			return errors.New("DirectPlan requires fallback reason")
		}
	default:
		return errors.New("invalid plan type")
	}
	return nil
}

func nullableCost(value *uint64) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func (repository *PlanRepository) LoadPlan(ctx context.Context, id planner.PlanID) (planner.Plan, error) {
	if repository == nil || repository.db == nil {
		return planner.Plan{}, errors.New("nil plan repository database")
	}
	return loadPlan(ctx, repository.db.sql, id)
}

func (repository *PlanRepository) LoadCurrentPlan(ctx context.Context, requestID domain.RequestID) (planner.Plan, error) {
	if repository == nil || repository.db == nil {
		return planner.Plan{}, errors.New("nil plan repository database")
	}
	var raw []byte
	if err := repository.db.sql.QueryRowContext(ctx, "SELECT plan_id FROM request_current_plan WHERE request_id=?", requestID[:]).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return planner.Plan{}, planner.ErrPlanNotFound
	} else if err != nil {
		return planner.Plan{}, err
	}
	var id planner.PlanID
	if err := copyExact(id[:], raw, "current plan ID"); err != nil {
		return planner.Plan{}, err
	}
	return repository.LoadPlan(ctx, id)
}

func loadPlan(ctx context.Context, query queryer, id planner.PlanID) (planner.Plan, error) {
	var plan planner.Plan
	var planID, requestID, snapshotID, fingerprint, home, target []byte
	var attempt, hopCount, stepCost int64
	var pathCost, directCost sql.NullInt64
	var createdAt int64
	err := query.QueryRowContext(ctx, `SELECT plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,
		home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at FROM plans WHERE plan_id=?`, id[:]).Scan(
		&planID, &requestID, &snapshotID, &plan.ProfileID, &fingerprint, &attempt, &plan.Type, &home, &target, &hopCount, &stepCost, &pathCost, &directCost, &plan.FallbackReason, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return planner.Plan{}, planner.ErrPlanNotFound
	}
	if err != nil {
		return planner.Plan{}, fmt.Errorf("load plan: %w", err)
	}
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{plan.ID[:], planID, "plan ID"}, {plan.RequestID[:], requestID, "request ID"}, {plan.SnapshotID[:], snapshotID, "snapshot ID"}, {plan.ProfileFingerprint[:], fingerprint, "profile fingerprint"}, {plan.HomeNodeID[:], home, "home node"}, {plan.TargetNodeID[:], target, "target node"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return planner.Plan{}, err
		}
	}
	plan.Attempt, plan.PathStepCost, plan.CreatedAt = uint64(attempt), uint64(stepCost), fromUnix(createdAt)
	if pathCost.Valid {
		value := uint64(pathCost.Int64)
		plan.PathCost = &value
	}
	if directCost.Valid {
		value := uint64(directCost.Int64)
		plan.DirectCost = &value
	}
	rows, err := query.QueryContext(ctx, "SELECT edge_id FROM plan_hops WHERE plan_id=? ORDER BY hop_index", id[:])
	if err != nil {
		return planner.Plan{}, err
	}
	for rows.Next() {
		var raw []byte
		var edgeID trustview.EdgeID
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return planner.Plan{}, err
		}
		if err := copyExact(edgeID[:], raw, "plan edge ID"); err != nil {
			_ = rows.Close()
			return planner.Plan{}, err
		}
		plan.Hops = append(plan.Hops, edgeID)
	}
	if err := rows.Close(); err != nil {
		return planner.Plan{}, err
	}
	if len(plan.Hops) != int(hopCount) {
		return planner.Plan{}, errors.New("corrupt plan hop count")
	}
	return plan, nil
}

func validatePlanSnapshotBinding(ctx context.Context, tx *sql.Tx, plan planner.Plan) error {
	var revision int64
	var root, start, target []byte
	var sealed int
	if err := tx.QueryRowContext(ctx, `SELECT graph_revision,home_trust_root,start_node_id,target_node_id,sealed
		FROM trustview_snapshots WHERE snapshot_id=?`, plan.SnapshotID[:]).Scan(&revision, &root, &start, &target, &sealed); err != nil {
		return fmt.Errorf("load plan TrustViewSnapshot: %w", err)
	}
	if sealed != 1 {
		return planner.ErrSnapshotUnsealed
	}
	var homeRoot trustview.TrustRoot
	var startID, targetID trustview.NodeID
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{homeRoot.Hash[:], root, "home TrustRoot"}, {startID[:], start, "start node"}, {targetID[:], target, "target node"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return err
		}
	}
	expected := trustview.ComputeSnapshotID(plan.RequestID, plan.Attempt, uint64(revision), startID, targetID, homeRoot)
	if expected != plan.SnapshotID || startID != plan.HomeNodeID || targetID != plan.TargetNodeID {
		return planner.ErrSnapshotEndpoint
	}
	return nil
}

func samePlanDecision(left, right planner.Plan) bool {
	if left.ID != right.ID || left.RequestID != right.RequestID || left.Attempt != right.Attempt || left.SnapshotID != right.SnapshotID || left.ProfileID != right.ProfileID || left.ProfileFingerprint != right.ProfileFingerprint || left.Type != right.Type || left.HomeNodeID != right.HomeNodeID || left.TargetNodeID != right.TargetNodeID || left.PathStepCost != right.PathStepCost || left.FallbackReason != right.FallbackReason || !sameOptionalCost(left.PathCost, right.PathCost) || !sameOptionalCost(left.DirectCost, right.DirectCost) || len(left.Hops) != len(right.Hops) {
		return false
	}
	for i := range left.Hops {
		if left.Hops[i] != right.Hops[i] {
			return false
		}
	}
	return true
}
func sameOptionalCost(left, right *uint64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

var _ planner.PlanRepository = (*PlanRepository)(nil)
