package replay

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayRepositoryEventTransactionIsIdempotentAndConflictsFail(t *testing.T) {
	ctx := context.Background()
	runDir := filepath.Join(t.TempDir(), "b2")
	identity := ReplayRunIdentity{RunID: "run-1", Setting: SettingB2, TraceDigest: strings.Repeat("a", 64), PreparedRows: 1}
	repository, err := OpenReplayRepository(ctx, runDir, identity, "full")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	event := ReplayEvent{ID: "event-1", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}}
	decision := ReplayDecision{EventID: event.ID, Sequence: 0, Setting: SettingB2, Decision: ReplayDecisionDirect, BaselineBefore: 0, BaselineAfter: 10, DirectCost: 1_000, ChosenCost: 1_000, GraphNodes: 2, GraphEdges: 1, CrossEdgesAdded: 1}
	snapshot := &ReplaySnapshot{Sequence: 0, GraphNodes: 2, GraphEdges: 1, CrossEdgesAdded: 1}
	if err := repository.CommitEvent(ctx, event, decision, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitEvent(ctx, event, decision, snapshot); err != nil {
		t.Fatalf("exact replay must be idempotent: %v", err)
	}
	changedSnapshot := *snapshot
	changedSnapshot.GraphNodes++
	if err := repository.CommitEvent(ctx, event, decision, &changedSnapshot); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting snapshot error = %v", err)
	}
	conflict := decision
	conflict.ChosenCost++
	if err := repository.CommitEvent(ctx, event, conflict, nil); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting replay error = %v", err)
	}
	progress, err := repository.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if progress.CompletedEvents != 1 || progress.LastCompletedSequence != 0 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestReplayRepositoryStoresPathsOnlyForSelectedTrustMapDecisions(t *testing.T) {
	ctx := context.Background()
	identity := ReplayRunIdentity{RunID: "selected-path", Setting: SettingB2, TraceDigest: strings.Repeat("e", 64), PreparedRows: 1}
	repository, err := OpenReplayRepository(ctx, filepath.Join(t.TempDir(), "b2"), identity, "full")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	event := ReplayEvent{ID: "event-1", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}}
	decision := ReplayDecision{EventID: event.ID, Sequence: 0, Setting: SettingB2, Decision: ReplayDecisionDirect, PathFound: true, Path: ReplayPath{Cost: 100}, BaselineAfter: 10, GraphNodes: 2, GraphEdges: 1, CrossEdgesAdded: 1}
	if err := repository.CommitEvent(ctx, event, decision, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM replay_paths`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("direct decision persisted %d replay paths", count)
	}
}

func TestReplayRepositoryReopensAndRejectsChangedRunIdentity(t *testing.T) {
	ctx := context.Background()
	runDir := filepath.Join(t.TempDir(), "b0")
	identity := ReplayRunIdentity{RunID: "run-1", Setting: SettingB0, TraceDigest: strings.Repeat("b", 64), PreparedRows: 2}
	repository, err := OpenReplayRepository(ctx, runDir, identity, "normal")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReplayRepository(ctx, runDir, identity, "normal")
	if err != nil {
		t.Fatalf("reopen identical run: %v", err)
	}
	_ = reopened.Close()
	changed := identity
	changed.PreparedRows++
	if _, err := OpenReplayRepository(ctx, runDir, changed, "normal"); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("changed identity error = %v", err)
	}
	if got, want := filepath.Clean(filepath.Join(runDir, "replay.db")), filepath.Clean(filepath.Join(runDir, "replay.db")); got != want {
		t.Fatalf("database path mismatch: %s", got)
	}
}

func TestRecoverReplayCoordinatorRebuildsDerivedStateWithoutPlanning(t *testing.T) {
	ctx := context.Background()
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, err := NewCheckpointPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	initHeights := map[string]uint64{"a": 0, "b": 0, "c": 0}
	events := []ReplayEvent{
		{ID: "e0", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}},
		{ID: "e1", Sequence: 1, Source: ReplayBlock{Chain: "b", OriginalHeight: 1}, Destination: ReplayBlock{Chain: "c", OriginalHeight: 1}},
		{ID: "e2", Sequence: 2, Source: ReplayBlock{Chain: "a", OriginalHeight: 20}, Destination: ReplayBlock{Chain: "c", OriginalHeight: 1}},
	}
	identity := ReplayRunIdentity{RunID: "recovery", Setting: SettingB2, TraceDigest: strings.Repeat("c", 64), PreparedRows: uint64(len(events))}
	repository, err := OpenReplayRepository(ctx, filepath.Join(t.TempDir(), "b2"), identity, "full")
	if err != nil {
		t.Fatal(err)
	}
	original, err := NewReplayCoordinator(SettingB2, initHeights, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events[:2] {
		decision, processErr := original.Process(event)
		if processErr != nil {
			t.Fatal(processErr)
		}
		if err := repository.CommitEvent(ctx, event, decision, nil); err != nil {
			t.Fatal(err)
		}
	}
	recovered, next, err := RecoverReplayCoordinator(ctx, repository, SettingB2, initHeights, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 || recovered.TrustView().NodeCount() != original.TrustView().NodeCount() || recovered.TrustView().EdgeCount() != original.TrustView().EdgeCount() {
		t.Fatalf("recovered next=%d nodes=%d edges=%d", next, recovered.TrustView().NodeCount(), recovered.TrustView().EdgeCount())
	}
	want, err := original.Process(events[2])
	if err != nil {
		t.Fatal(err)
	}
	got, err := recovered.Process(events[2])
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != want.Decision || got.DirectCost != want.DirectCost || got.PathCost != want.PathCost || got.ChosenCost != want.ChosenCost {
		t.Fatalf("resumed decision = %#v, want %#v", got, want)
	}
	_ = repository.Close()
}

func TestRecoverReplayCoordinatorFailsOnCommittedStateMismatch(t *testing.T) {
	ctx := context.Background()
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, _ := NewCheckpointPolicy(nil)
	identity := ReplayRunIdentity{RunID: "corrupt", Setting: SettingB0, TraceDigest: strings.Repeat("d", 64), PreparedRows: 1}
	repository, err := OpenReplayRepository(ctx, filepath.Join(t.TempDir(), "b0"), identity, "full")
	if err != nil {
		t.Fatal(err)
	}
	event := ReplayEvent{ID: "e0", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}}
	decision := ReplayDecision{EventID: event.ID, Sequence: 0, Setting: SettingB0, Decision: ReplayDecisionDirect, BaselineBefore: 99, BaselineAfter: 10, GraphNodes: 2, GraphEdges: 1, CrossEdgesAdded: 1}
	if err := repository.CommitEvent(ctx, event, decision, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecoverReplayCoordinator(ctx, repository, SettingB0, map[string]uint64{"a": 0, "b": 0}, profile, policy); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("recovery mismatch error = %v", err)
	}
	_ = repository.Close()
}

func TestRecoverReplayCoordinatorVerifiesPersistedDerivedTables(t *testing.T) {
	ctx := context.Background()
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, _ := NewCheckpointPolicy(nil)
	identity := ReplayRunIdentity{RunID: "derived-corrupt", Setting: SettingB0, TraceDigest: strings.Repeat("f", 64), PreparedRows: 1}
	repository, err := OpenReplayRepository(ctx, filepath.Join(t.TempDir(), "b0"), identity, "full")
	if err != nil {
		t.Fatal(err)
	}
	event := ReplayEvent{ID: "e0", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}}
	coordinator, _ := NewReplayCoordinator(SettingB0, map[string]uint64{"a": 0, "b": 0}, profile, policy)
	decision, err := coordinator.Process(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitEvent(ctx, event, decision, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `UPDATE replay_baselines SET height = height + 1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecoverReplayCoordinator(ctx, repository, SettingB0, map[string]uint64{"a": 0, "b": 0}, profile, policy); err == nil || !strings.Contains(err.Error(), "persisted baseline") {
		t.Fatalf("derived table mismatch error = %v", err)
	}
	_ = repository.Close()
}

func TestRecoverReplayCoordinatorRejectsCorruptCrossPathAndSnapshotRows(t *testing.T) {
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, _ := NewCheckpointPolicy(nil)
	for _, corrupt := range []string{"cross", "path", "snapshot"} {
		t.Run(corrupt, func(t *testing.T) {
			ctx := context.Background()
			identity := ReplayRunIdentity{RunID: "corrupt-" + corrupt, Setting: SettingB2, TraceDigest: strings.Repeat("1", 64), PreparedRows: 1, RecordEvery: 1}
			repository, err := OpenReplayRepository(ctx, filepath.Join(t.TempDir(), "b2"), identity, "full")
			if err != nil {
				t.Fatal(err)
			}
			defer repository.Close()
			event := ReplayEvent{ID: "e0", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}}
			decision := ReplayDecision{
				EventID: event.ID, Sequence: 0, Setting: SettingB2, Decision: ReplayDecisionTrustMap,
				BaselineBefore: 0, BaselineAfter: 10, DirectCost: 1_000, PathCost: 10,
				TrustMapTotalCost: 15, ChosenCost: 15, GraphNodes: 2, GraphEdges: 1, CrossEdgesAdded: 1,
				Path: ReplayPath{Cost: 10, Nodes: []ReplayBlockKey{{Chain: "b", Height: 1}, {Chain: "a", Height: 10}}},
			}
			snapshot := &ReplaySnapshot{Sequence: 0, Time: event.SourceTime, GraphNodes: 2, GraphEdges: 1, CrossEdgesAdded: 1}
			if err := repository.CommitEvent(ctx, event, decision, snapshot); err != nil {
				t.Fatal(err)
			}
			var statement string
			switch corrupt {
			case "cross":
				statement = `UPDATE replay_cross_edges SET to_chain = 'wrong'`
			case "path":
				statement = `UPDATE replay_paths SET path_json = '{}'`
			case "snapshot":
				statement = `DELETE FROM replay_snapshots`
			}
			if _, err := repository.db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
			if _, _, err := RecoverReplayCoordinator(ctx, repository, SettingB2, map[string]uint64{"a": 0, "b": 0}, profile, policy); err == nil || !strings.Contains(err.Error(), "persisted "+corrupt) {
				t.Fatalf("%s corruption error = %v", corrupt, err)
			}
		})
	}
}
