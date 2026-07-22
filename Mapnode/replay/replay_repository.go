package replay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const ReplaySchemaVersion = 2

// ReplayRunIdentity contains every persisted input that must remain unchanged
// when a replay setting is resumed.
type ReplayRunIdentity struct {
	RunID                       string  `json:"run_id"`
	Setting                     Setting `json:"setting"`
	TraceDigest                 string  `json:"trace_digest"`
	PreparedRows                uint64  `json:"prepared_rows"`
	ImplementationVersion       string  `json:"implementation_version,omitempty"`
	SchemaVersion               int     `json:"schema_version"`
	GitCommit                   string  `json:"git_commit,omitempty"`
	CostProfileFingerprint      string  `json:"cost_profile_fingerprint,omitempty"`
	CheckpointPolicyFingerprint string  `json:"checkpoint_policy_fingerprint,omitempty"`
	SelectionFingerprint        string  `json:"selection_fingerprint,omitempty"`
	RecordEvery                 uint64  `json:"record_every"`
	LogEvery                    uint64  `json:"log_every"`
}

type ReplayProgress struct {
	CompletedEvents       uint64 `json:"completed_events"`
	LastCompletedSequence uint64 `json:"last_completed_sequence"`
}

type ReplaySnapshot struct {
	Sequence        uint64
	Time            time.Time
	GraphNodes      uint64
	GraphEdges      uint64
	CrossEdgesAdded uint64
}

type ReplayCommittedEvent struct {
	Event    ReplayEvent
	Decision ReplayDecision
	Snapshot *ReplaySnapshot
}

type ReplayRepository struct {
	db       *sql.DB
	path     string
	identity ReplayRunIdentity
}

func OpenReplayRepository(ctx context.Context, runDir string, identity ReplayRunIdentity, synchronous string) (*ReplayRepository, error) {
	if strings.TrimSpace(runDir) == "" {
		return nil, fmt.Errorf("replay run directory is required")
	}
	if identity.SchemaVersion == 0 {
		identity.SchemaVersion = ReplaySchemaVersion
	}
	if identity.SchemaVersion != ReplaySchemaVersion || identity.RunID == "" || identity.TraceDigest == "" {
		return nil, fmt.Errorf("invalid replay run identity")
	}
	if identity.Setting != SettingB0 && identity.Setting != SettingB1 && identity.Setting != SettingB2 && identity.Setting != SettingB3 {
		return nil, fmt.Errorf("invalid replay run identity setting %q", identity.Setting)
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("create replay run directory: %w", err)
	}
	cleanRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return nil, fmt.Errorf("resolve replay run directory: %w", err)
	}
	databasePath := filepath.Join(cleanRunDir, "replay.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open replay database: %w", err)
	}
	db.SetMaxOpenConns(1)
	repository := &ReplayRepository{db: db, path: databasePath, identity: identity}
	closeOnError := func(openErr error) (*ReplayRepository, error) {
		_ = db.Close()
		return nil, openErr
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return closeOnError(fmt.Errorf("enable replay WAL: %w", err))
	}
	switch strings.ToLower(strings.TrimSpace(synchronous)) {
	case "", "full":
		_, err = db.ExecContext(ctx, "PRAGMA synchronous = FULL")
	case "normal":
		_, err = db.ExecContext(ctx, "PRAGMA synchronous = NORMAL")
	default:
		return closeOnError(fmt.Errorf("invalid replay synchronous mode %q", synchronous))
	}
	if err != nil {
		return closeOnError(fmt.Errorf("configure replay durability: %w", err))
	}
	if err := repository.initialize(ctx); err != nil {
		return closeOnError(err)
	}
	return repository, nil
}

func (repository *ReplayRepository) Close() error { return repository.db.Close() }
func (repository *ReplayRepository) Path() string { return repository.path }

func (repository *ReplayRepository) initialize(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS replay_runs (run_id TEXT PRIMARY KEY, identity_json BLOB NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS replay_events (sequence INTEGER PRIMARY KEY, event_id TEXT NOT NULL UNIQUE, event_json BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS replay_progress (singleton INTEGER PRIMARY KEY CHECK(singleton = 1), completed_events INTEGER NOT NULL, last_sequence INTEGER)`,
		`CREATE TABLE IF NOT EXISTS replay_baselines (verifier_chain TEXT NOT NULL, target_chain TEXT NOT NULL, height INTEGER NOT NULL, PRIMARY KEY(verifier_chain, target_chain))`,
		`CREATE TABLE IF NOT EXISTS replay_decisions (sequence INTEGER PRIMARY KEY REFERENCES replay_events(sequence), decision_json BLOB NOT NULL, direct_cost INTEGER NOT NULL, chosen_cost INTEGER NOT NULL, decision TEXT NOT NULL, integrity_digest TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS replay_paths (sequence INTEGER PRIMARY KEY REFERENCES replay_events(sequence), path_json BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS replay_cross_edges (sequence INTEGER PRIMARY KEY REFERENCES replay_events(sequence), from_chain TEXT NOT NULL, from_height INTEGER NOT NULL, to_chain TEXT NOT NULL, to_height INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS replay_snapshots (sequence INTEGER PRIMARY KEY REFERENCES replay_events(sequence), snapshot_json BLOB NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := repository.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize replay schema: %w", err)
		}
	}
	identityJSON, err := json.Marshal(repository.identity)
	if err != nil {
		return fmt.Errorf("encode replay run identity: %w", err)
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replay run initialization: %w", err)
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT identity_json FROM replay_runs LIMIT 1`).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO replay_runs(run_id, identity_json, created_at) VALUES(?, ?, ?)`, repository.identity.RunID, identityJSON, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("persist replay run identity: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO replay_progress(singleton, completed_events, last_sequence) VALUES(1, 0, NULL)`); err != nil {
			return fmt.Errorf("initialize replay progress: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read replay run identity: %w", err)
	case !bytes.Equal(existing, identityJSON):
		return fmt.Errorf("replay run identity conflict")
	}
	var progressRows int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM replay_progress WHERE singleton = 1`).Scan(&progressRows); err != nil {
		return fmt.Errorf("verify replay progress: %w", err)
	}
	if progressRows != 1 {
		return fmt.Errorf("replay run identity exists without progress state")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replay run initialization: %w", err)
	}
	return nil
}

func (repository *ReplayRepository) CommitEvent(ctx context.Context, event ReplayEvent, decision ReplayDecision, snapshot *ReplaySnapshot) error {
	if event.ID == "" || decision.EventID != event.ID || decision.Sequence != event.Sequence || decision.Setting != repository.identity.Setting {
		return fmt.Errorf("replay event transaction identity mismatch")
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode replay event: %w", err)
	}
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		return fmt.Errorf("encode replay decision: %w", err)
	}
	integrityDigest := replayDecisionIntegrityDigest(eventJSON, decisionJSON)
	var requestedSnapshotJSON []byte
	if snapshot != nil {
		if snapshot.Sequence != event.Sequence || !snapshot.Time.Equal(event.SourceTime) || snapshot.GraphNodes != decision.GraphNodes || snapshot.GraphEdges != decision.GraphEdges || snapshot.CrossEdgesAdded != decision.CrossEdgesAdded {
			return fmt.Errorf("replay snapshot conflict: does not match event decision")
		}
		requestedSnapshotJSON, err = json.Marshal(snapshot)
		if err != nil {
			return fmt.Errorf("encode replay snapshot: %w", err)
		}
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replay event transaction: %w", err)
	}
	defer tx.Rollback()
	var existingEvent, existingDecision []byte
	var existingSnapshot []byte
	err = tx.QueryRowContext(ctx, `SELECT e.event_json, d.decision_json, s.snapshot_json FROM replay_events e JOIN replay_decisions d USING(sequence) LEFT JOIN replay_snapshots s USING(sequence) WHERE e.sequence = ?`, event.Sequence).Scan(&existingEvent, &existingDecision, &existingSnapshot)
	if err == nil {
		if bytes.Equal(existingEvent, eventJSON) && bytes.Equal(existingDecision, decisionJSON) && bytes.Equal(existingSnapshot, requestedSnapshotJSON) {
			return nil
		}
		return fmt.Errorf("replay event %d conflict", event.Sequence)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect replay event transaction: %w", err)
	}
	var completed uint64
	if err := tx.QueryRowContext(ctx, `SELECT completed_events FROM replay_progress WHERE singleton = 1`).Scan(&completed); err != nil {
		return fmt.Errorf("read replay progress: %w", err)
	}
	if event.Sequence != completed {
		return fmt.Errorf("replay sequence conflict: got %d want %d", event.Sequence, completed)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_events(sequence, event_id, event_json) VALUES(?, ?, ?)`, event.Sequence, event.ID, eventJSON); err != nil {
		return fmt.Errorf("persist replay event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_decisions(sequence, decision_json, direct_cost, chosen_cost, decision, integrity_digest) VALUES(?, ?, ?, ?, ?, ?)`, event.Sequence, decisionJSON, decision.DirectCost, decision.ChosenCost, decision.Decision, integrityDigest); err != nil {
		return fmt.Errorf("persist replay decision: %w", err)
	}
	if decision.Decision == ReplayDecisionTrustMap {
		pathJSON, marshalErr := json.Marshal(decision.Path)
		if marshalErr != nil {
			return fmt.Errorf("encode replay path: %w", marshalErr)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO replay_paths(sequence, path_json) VALUES(?, ?)`, event.Sequence, pathJSON); err != nil {
			return fmt.Errorf("persist replay path: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_baselines(verifier_chain, target_chain, height) VALUES(?, ?, ?) ON CONFLICT(verifier_chain, target_chain) DO UPDATE SET height = excluded.height`, canonicalChain(event.Destination.Chain), canonicalChain(event.Source.Chain), decision.BaselineAfter); err != nil {
		return fmt.Errorf("persist replay baseline: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_cross_edges(sequence, from_chain, from_height, to_chain, to_height) VALUES(?, ?, ?, ?, ?)`, event.Sequence, canonicalChain(event.Destination.Chain), event.Destination.OriginalHeight, canonicalChain(event.Source.Chain), event.Source.OriginalHeight); err != nil {
		return fmt.Errorf("persist replay cross edge: %w", err)
	}
	if snapshot != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO replay_snapshots(sequence, snapshot_json) VALUES(?, ?)`, event.Sequence, requestedSnapshotJSON); err != nil {
			return fmt.Errorf("persist replay snapshot: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE replay_progress SET completed_events = ?, last_sequence = ? WHERE singleton = 1`, event.Sequence+1, event.Sequence); err != nil {
		return fmt.Errorf("persist replay progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replay event transaction: %w", err)
	}
	return nil
}

func replayDecisionIntegrityDigest(eventJSON, decisionJSON []byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("trustmap/replay-committed-decision/v1\n"))
	_, _ = digest.Write(eventJSON)
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(decisionJSON)
	return hex.EncodeToString(digest.Sum(nil))
}

func (repository *ReplayRepository) Progress(ctx context.Context) (ReplayProgress, error) {
	var progress ReplayProgress
	var last sql.NullInt64
	if err := repository.db.QueryRowContext(ctx, `SELECT completed_events, last_sequence FROM replay_progress WHERE singleton = 1`).Scan(&progress.CompletedEvents, &last); err != nil {
		return ReplayProgress{}, fmt.Errorf("read replay progress: %w", err)
	}
	if last.Valid {
		progress.LastCompletedSequence = uint64(last.Int64)
	}
	return progress, nil
}

func (repository *ReplayRepository) CompletedEvents(ctx context.Context) ([]ReplayCommittedEvent, error) {
	var completed []ReplayCommittedEvent
	if err := repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
		completed = append(completed, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return completed, nil
}

// ForEachCompleted visits committed replay rows in sequence order without
// retaining prior rows. The callback must not issue another query against this
// repository: replay databases intentionally use a single SQLite connection.
func (repository *ReplayRepository) ForEachCompleted(ctx context.Context, visit func(ReplayCommittedEvent) error) error {
	if visit == nil {
		return fmt.Errorf("completed replay event visitor is required")
	}
	rows, err := repository.db.QueryContext(ctx, `SELECT e.event_json, d.decision_json, s.snapshot_json FROM replay_events e JOIN replay_decisions d USING(sequence) LEFT JOIN replay_snapshots s USING(sequence) ORDER BY e.sequence`)
	if err != nil {
		return fmt.Errorf("query completed replay events: %w", err)
	}
	defer rows.Close()
	var expectedSequence uint64
	for rows.Next() {
		var eventJSON, decisionJSON []byte
		var snapshotJSON []byte
		if err := rows.Scan(&eventJSON, &decisionJSON, &snapshotJSON); err != nil {
			return fmt.Errorf("scan completed replay event: %w", err)
		}
		var item ReplayCommittedEvent
		if err := json.Unmarshal(eventJSON, &item.Event); err != nil {
			return fmt.Errorf("decode completed replay event: %w", err)
		}
		if err := json.Unmarshal(decisionJSON, &item.Decision); err != nil {
			return fmt.Errorf("decode completed replay decision: %w", err)
		}
		if snapshotJSON != nil {
			item.Snapshot = new(ReplaySnapshot)
			if err := json.Unmarshal(snapshotJSON, item.Snapshot); err != nil {
				return fmt.Errorf("decode replay snapshot: %w", err)
			}
		}
		if item.Event.Sequence != expectedSequence || item.Decision.Sequence != expectedSequence || item.Event.ID != item.Decision.EventID {
			return fmt.Errorf("completed replay event sequence %d is inconsistent", expectedSequence)
		}
		if err := visit(item); err != nil {
			return fmt.Errorf("visit completed replay event %d: %w", expectedSequence, err)
		}
		expectedSequence++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate completed replay events: %w", err)
	}
	return nil
}

func RecoverReplayCoordinator(ctx context.Context, repository *ReplayRepository, setting Setting, initHeights map[string]uint64, profile CostProfile, policy CheckpointPolicy) (*ReplayCoordinator, uint64, error) {
	coordinator, completed, _, err := recoverReplayCoordinator(ctx, repository, setting, initHeights, profile, policy, nil)
	return coordinator, completed, err
}

func recoverReplayCoordinator(ctx context.Context, repository *ReplayRepository, setting Setting, initHeights map[string]uint64, profile CostProfile, policy CheckpointPolicy, inspect func(ReplayCommittedEvent) error) (*ReplayCoordinator, uint64, uint64, error) {
	if repository.identity.Setting != setting {
		return nil, 0, 0, fmt.Errorf("replay recovery setting conflicts with run identity")
	}
	coordinator, err := NewReplayCoordinator(setting, initHeights, profile, policy)
	if err != nil {
		return nil, 0, 0, err
	}
	// Validate all persisted projections before restoring the coordinator so
	// corruption is reported at its durable source rather than as a later
	// planner-state mismatch.
	if err := repository.verifyPersistedDerivedState(ctx); err != nil {
		return nil, 0, 0, err
	}
	var completed uint64
	var trustMapRows uint64
	err = repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
		if completed >= repository.identity.PreparedRows {
			return fmt.Errorf("completed replay rows exceed run identity")
		}
		if inspect != nil {
			if err := inspect(item); err != nil {
				return err
			}
		}
		if err := coordinator.Restore(item.Event, item.Decision); err != nil {
			return err
		}
		if item.Decision.Decision == ReplayDecisionTrustMap {
			trustMapRows++
		}
		completed++
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return coordinator, completed, trustMapRows, nil
}

func (repository *ReplayRepository) verifyPersistedDerivedState(ctx context.Context) error {
	progress, err := repository.Progress(ctx)
	if err != nil {
		return err
	}

	expected := make(map[baselineKey]uint64)
	rows, err := repository.db.QueryContext(ctx, `SELECT e.sequence, e.event_json, d.decision_json, d.direct_cost, d.chosen_cost, d.decision, d.integrity_digest, p.path_json, c.sequence, c.from_chain, c.from_height, c.to_chain, c.to_height, s.snapshot_json
		FROM replay_events e
		JOIN replay_decisions d USING(sequence)
		LEFT JOIN replay_paths p USING(sequence)
		LEFT JOIN replay_cross_edges c USING(sequence)
		LEFT JOIN replay_snapshots s USING(sequence)
		ORDER BY e.sequence`)
	if err != nil {
		return fmt.Errorf("read persisted replay derived state: %w", err)
	}
	var completed uint64
	var expectedPaths uint64
	var expectedSnapshots uint64
	for rows.Next() {
		var sequence uint64
		var eventJSON, decisionJSON, pathJSON, snapshotJSON []byte
		var directCost, chosenCost uint64
		var decisionKind, integrityDigest string
		var crossSequence sql.NullInt64
		var fromChain, toChain sql.NullString
		var fromHeight, toHeight sql.NullInt64
		if err := rows.Scan(&sequence, &eventJSON, &decisionJSON, &directCost, &chosenCost, &decisionKind, &integrityDigest, &pathJSON, &crossSequence, &fromChain, &fromHeight, &toChain, &toHeight, &snapshotJSON); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan persisted replay derived state: %w", err)
		}
		var event ReplayEvent
		var decision ReplayDecision
		if err := json.Unmarshal(eventJSON, &event); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode persisted replay event: %w", err)
		}
		if err := json.Unmarshal(decisionJSON, &decision); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode persisted replay decision: %w", err)
		}
		if sequence != completed || event.Sequence != sequence || decision.Sequence != sequence || event.ID != decision.EventID {
			_ = rows.Close()
			return fmt.Errorf("completed replay event sequence %d is inconsistent", completed)
		}
		if directCost != decision.DirectCost || chosenCost != decision.ChosenCost || decisionKind != string(decision.Decision) {
			_ = rows.Close()
			return fmt.Errorf("persisted decision scalar columns at sequence %d do not match decision JSON", sequence)
		}
		if integrityDigest != replayDecisionIntegrityDigest(eventJSON, decisionJSON) {
			_ = rows.Close()
			return fmt.Errorf("persisted decision integrity digest at sequence %d does not match event and decision JSON", sequence)
		}
		expected[baselineKey{verifier: canonicalChain(event.Destination.Chain), target: canonicalChain(event.Source.Chain)}] = decision.BaselineAfter
		if !crossSequence.Valid || uint64(crossSequence.Int64) != sequence || !fromChain.Valid || fromChain.String != canonicalChain(event.Destination.Chain) || !fromHeight.Valid || uint64(fromHeight.Int64) != event.Destination.OriginalHeight || !toChain.Valid || toChain.String != canonicalChain(event.Source.Chain) || !toHeight.Valid || uint64(toHeight.Int64) != event.Source.OriginalHeight {
			_ = rows.Close()
			return fmt.Errorf("persisted cross edge at sequence %d does not match completed event", sequence)
		}
		if decision.Decision == ReplayDecisionTrustMap {
			expectedPaths++
			expectedPath, marshalErr := json.Marshal(decision.Path)
			if marshalErr != nil {
				_ = rows.Close()
				return fmt.Errorf("encode expected replay path: %w", marshalErr)
			}
			if pathJSON == nil || !bytes.Equal(expectedPath, pathJSON) {
				_ = rows.Close()
				return fmt.Errorf("persisted path at sequence %d does not match selected decision", sequence)
			}
		} else if pathJSON != nil {
			_ = rows.Close()
			return fmt.Errorf("persisted path at sequence %d does not match selected decision", sequence)
		}
		expectsSnapshot := repository.identity.Setting.UsesTrustMap() && repository.identity.RecordEvery > 0 && sequence%repository.identity.RecordEvery == 0
		if expectsSnapshot != (snapshotJSON != nil) {
			_ = rows.Close()
			return fmt.Errorf("persisted snapshot at sequence %d does not match record interval", sequence)
		}
		if snapshotJSON != nil {
			expectedSnapshots++
			var snapshot ReplaySnapshot
			if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
				_ = rows.Close()
				return fmt.Errorf("decode persisted replay snapshot: %w", err)
			}
			if snapshot.Sequence != event.Sequence || !snapshot.Time.Equal(event.SourceTime) || snapshot.GraphNodes != decision.GraphNodes || snapshot.GraphEdges != decision.GraphEdges || snapshot.CrossEdgesAdded != decision.CrossEdgesAdded {
				_ = rows.Close()
				return fmt.Errorf("persisted snapshot at sequence %d does not match decision", sequence)
			}
		}
		completed++
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close persisted replay derived state: %w", err)
	}
	if progress.CompletedEvents != completed || (completed > 0 && progress.LastCompletedSequence != completed-1) {
		return fmt.Errorf("persisted replay progress does not match completed events")
	}
	var crossCount, pathCount, snapshotCount uint64
	if err := repository.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM replay_cross_edges),
		(SELECT count(*) FROM replay_paths),
		(SELECT count(*) FROM replay_snapshots)`).Scan(&crossCount, &pathCount, &snapshotCount); err != nil {
		return fmt.Errorf("count persisted replay derived rows: %w", err)
	}
	if crossCount != completed {
		return fmt.Errorf("persisted cross-edge count does not match completed events")
	}
	if pathCount != expectedPaths {
		return fmt.Errorf("persisted path count does not match selected decisions")
	}
	if snapshotCount != expectedSnapshots {
		return fmt.Errorf("persisted snapshot count does not match record interval")
	}

	baselineRows, err := repository.db.QueryContext(ctx, `SELECT verifier_chain, target_chain, height FROM replay_baselines`)
	if err != nil {
		return fmt.Errorf("read persisted replay baselines: %w", err)
	}
	actualCount := 0
	for baselineRows.Next() {
		var key baselineKey
		var height uint64
		if err := baselineRows.Scan(&key.verifier, &key.target, &height); err != nil {
			_ = baselineRows.Close()
			return fmt.Errorf("scan persisted replay baseline: %w", err)
		}
		if expectedHeight, ok := expected[key]; !ok || expectedHeight != height {
			_ = baselineRows.Close()
			return fmt.Errorf("persisted baseline for %s/%s does not match completed events", key.verifier, key.target)
		}
		actualCount++
	}
	if err := baselineRows.Close(); err != nil {
		return fmt.Errorf("close persisted replay baselines: %w", err)
	}
	if actualCount != len(expected) {
		return fmt.Errorf("persisted baseline count does not match completed events")
	}
	return nil
}
