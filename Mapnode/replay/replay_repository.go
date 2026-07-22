package replay

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const ReplaySchemaVersion = 1

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
		`CREATE TABLE IF NOT EXISTS replay_decisions (sequence INTEGER PRIMARY KEY REFERENCES replay_events(sequence), decision_json BLOB NOT NULL, direct_cost INTEGER NOT NULL, chosen_cost INTEGER NOT NULL, decision TEXT NOT NULL)`,
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_decisions(sequence, decision_json, direct_cost, chosen_cost, decision) VALUES(?, ?, ?, ?, ?)`, event.Sequence, decisionJSON, decision.DirectCost, decision.ChosenCost, decision.Decision); err != nil {
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
	rows, err := repository.db.QueryContext(ctx, `SELECT e.event_json, d.decision_json, s.snapshot_json FROM replay_events e JOIN replay_decisions d USING(sequence) LEFT JOIN replay_snapshots s USING(sequence) ORDER BY e.sequence`)
	if err != nil {
		return nil, fmt.Errorf("query completed replay events: %w", err)
	}
	defer rows.Close()
	var completed []ReplayCommittedEvent
	for rows.Next() {
		var eventJSON, decisionJSON []byte
		var snapshotJSON []byte
		if err := rows.Scan(&eventJSON, &decisionJSON, &snapshotJSON); err != nil {
			return nil, fmt.Errorf("scan completed replay event: %w", err)
		}
		var item ReplayCommittedEvent
		if err := json.Unmarshal(eventJSON, &item.Event); err != nil {
			return nil, fmt.Errorf("decode completed replay event: %w", err)
		}
		if err := json.Unmarshal(decisionJSON, &item.Decision); err != nil {
			return nil, fmt.Errorf("decode completed replay decision: %w", err)
		}
		if snapshotJSON != nil {
			item.Snapshot = new(ReplaySnapshot)
			if err := json.Unmarshal(snapshotJSON, item.Snapshot); err != nil {
				return nil, fmt.Errorf("decode replay snapshot: %w", err)
			}
		}
		completed = append(completed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate completed replay events: %w", err)
	}
	for sequence, item := range completed {
		if item.Event.Sequence != uint64(sequence) || item.Decision.Sequence != uint64(sequence) || item.Event.ID != item.Decision.EventID {
			return nil, fmt.Errorf("completed replay event sequence %d is inconsistent", sequence)
		}
	}
	return completed, nil
}

func RecoverReplayCoordinator(ctx context.Context, repository *ReplayRepository, setting Setting, initHeights map[string]uint64, profile CostProfile, policy CheckpointPolicy) (*ReplayCoordinator, uint64, error) {
	if repository.identity.Setting != setting {
		return nil, 0, fmt.Errorf("replay recovery setting conflicts with run identity")
	}
	coordinator, err := NewReplayCoordinator(setting, initHeights, profile, policy)
	if err != nil {
		return nil, 0, err
	}
	completed, err := repository.CompletedEvents(ctx)
	if err != nil {
		return nil, 0, err
	}
	if uint64(len(completed)) > repository.identity.PreparedRows {
		return nil, 0, fmt.Errorf("completed replay rows exceed run identity")
	}
	for _, item := range completed {
		if err := coordinator.Restore(item.Event, item.Decision); err != nil {
			return nil, 0, err
		}
	}
	if err := repository.verifyPersistedDerivedState(ctx, completed); err != nil {
		return nil, 0, err
	}
	return coordinator, uint64(len(completed)), nil
}

func (repository *ReplayRepository) verifyPersistedDerivedState(ctx context.Context, completed []ReplayCommittedEvent) error {
	progress, err := repository.Progress(ctx)
	if err != nil {
		return err
	}
	if progress.CompletedEvents != uint64(len(completed)) || (len(completed) > 0 && progress.LastCompletedSequence != uint64(len(completed)-1)) {
		return fmt.Errorf("persisted replay progress does not match completed events")
	}
	expected := make(map[baselineKey]uint64)
	for _, item := range completed {
		expected[baselineKey{verifier: canonicalChain(item.Event.Destination.Chain), target: canonicalChain(item.Event.Source.Chain)}] = item.Decision.BaselineAfter
	}
	rows, err := repository.db.QueryContext(ctx, `SELECT verifier_chain, target_chain, height FROM replay_baselines`)
	if err != nil {
		return fmt.Errorf("read persisted replay baselines: %w", err)
	}
	actual := make(map[baselineKey]uint64)
	for rows.Next() {
		var key baselineKey
		var height uint64
		if err := rows.Scan(&key.verifier, &key.target, &height); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan persisted replay baseline: %w", err)
		}
		actual[key] = height
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close persisted replay baselines: %w", err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("persisted baseline count does not match completed events")
	}
	for key, height := range expected {
		if actual[key] != height {
			return fmt.Errorf("persisted baseline for %s/%s does not match completed events", key.verifier, key.target)
		}
	}
	var crossEdges int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM replay_cross_edges`).Scan(&crossEdges); err != nil {
		return fmt.Errorf("count persisted replay cross edges: %w", err)
	}
	if crossEdges != len(completed) {
		return fmt.Errorf("persisted cross-edge count does not match completed events")
	}
	crossRows, err := repository.db.QueryContext(ctx, `SELECT sequence, from_chain, from_height, to_chain, to_height FROM replay_cross_edges ORDER BY sequence`)
	if err != nil {
		return fmt.Errorf("read persisted cross edges: %w", err)
	}
	crossIndex := 0
	for crossRows.Next() {
		var sequence, fromHeight, toHeight uint64
		var fromChain, toChain string
		if err := crossRows.Scan(&sequence, &fromChain, &fromHeight, &toChain, &toHeight); err != nil {
			_ = crossRows.Close()
			return fmt.Errorf("scan persisted cross edge: %w", err)
		}
		if crossIndex >= len(completed) {
			_ = crossRows.Close()
			return fmt.Errorf("persisted cross edge has no completed event")
		}
		event := completed[crossIndex].Event
		if sequence != event.Sequence || fromChain != canonicalChain(event.Destination.Chain) || fromHeight != event.Destination.OriginalHeight || toChain != canonicalChain(event.Source.Chain) || toHeight != event.Source.OriginalHeight {
			_ = crossRows.Close()
			return fmt.Errorf("persisted cross edge at sequence %d does not match completed event", sequence)
		}
		crossIndex++
	}
	if err := crossRows.Close(); err != nil {
		return fmt.Errorf("close persisted cross edges: %w", err)
	}

	expectedPaths := make(map[uint64][]byte)
	for _, item := range completed {
		if item.Decision.Decision != ReplayDecisionTrustMap {
			continue
		}
		encoded, err := json.Marshal(item.Decision.Path)
		if err != nil {
			return fmt.Errorf("encode expected replay path: %w", err)
		}
		expectedPaths[item.Event.Sequence] = encoded
	}
	pathRows, err := repository.db.QueryContext(ctx, `SELECT sequence, path_json FROM replay_paths ORDER BY sequence`)
	if err != nil {
		return fmt.Errorf("read persisted paths: %w", err)
	}
	actualPaths := 0
	for pathRows.Next() {
		var sequence uint64
		var encoded []byte
		if err := pathRows.Scan(&sequence, &encoded); err != nil {
			_ = pathRows.Close()
			return fmt.Errorf("scan persisted path: %w", err)
		}
		expected, ok := expectedPaths[sequence]
		if !ok || !bytes.Equal(expected, encoded) {
			_ = pathRows.Close()
			return fmt.Errorf("persisted path at sequence %d does not match selected decision", sequence)
		}
		actualPaths++
	}
	if err := pathRows.Close(); err != nil {
		return fmt.Errorf("close persisted paths: %w", err)
	}
	if actualPaths != len(expectedPaths) {
		return fmt.Errorf("persisted path count does not match selected decisions")
	}

	for _, item := range completed {
		expectsSnapshot := repository.identity.Setting.UsesTrustMap() && repository.identity.RecordEvery > 0 && item.Event.Sequence%repository.identity.RecordEvery == 0
		if expectsSnapshot != (item.Snapshot != nil) {
			return fmt.Errorf("persisted snapshot at sequence %d does not match record interval", item.Event.Sequence)
		}
		if item.Snapshot != nil && (item.Snapshot.Sequence != item.Event.Sequence || !item.Snapshot.Time.Equal(item.Event.SourceTime) || item.Snapshot.GraphNodes != item.Decision.GraphNodes || item.Snapshot.GraphEdges != item.Decision.GraphEdges || item.Snapshot.CrossEdgesAdded != item.Decision.CrossEdgesAdded) {
			return fmt.Errorf("persisted snapshot at sequence %d does not match decision", item.Event.Sequence)
		}
	}
	return nil
}
