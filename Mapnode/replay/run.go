package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
)

const ReplayImplementationVersion = "replay-v1"

// ReplayGitCommit may be set with -ldflags for published experiment binaries.
var ReplayGitCommit = "unknown"

type ReplayRunResult struct {
	Setting         Setting
	RunID           string
	RunDirectory    string
	CompletedEvents uint64
}

func RunReplay(ctx context.Context, config Config) ([]ReplayRunResult, error) {
	trace, err := PrepareTrace(config.InputTrace, TraceOptions{
		InvalidRowPolicy: config.InvalidRowPolicy,
		ChainAllowlist:   config.ChainAllowlist,
		MaxEvents:        config.MaxEvents,
	})
	if err != nil {
		return nil, err
	}
	if config.ExpectedDigest != "" && config.ExpectedDigest != trace.Digest {
		return nil, fmt.Errorf("input trace digest mismatch: got %s want %s", trace.Digest, config.ExpectedDigest)
	}
	if err := config.ValidatePreparedTrace(trace); err != nil {
		return nil, fmt.Errorf("validate prepared replay trace: %w", err)
	}
	results := make([]ReplayRunResult, 0, len(config.Settings))
	for _, setting := range config.Settings {
		policy := config.CheckpointPolicyForSetting(setting)
		identity, identityErr := NewReplayRunIdentity(config, trace, setting, policy)
		if identityErr != nil {
			return nil, identityErr
		}
		runDir := config.SettingRunDir(setting)
		repository, openErr := OpenReplayRepository(ctx, runDir, identity, config.Durability.Synchronous)
		if openErr != nil {
			return nil, fmt.Errorf("open %s replay run: %w", setting, openErr)
		}
		completed, readErr := repository.CompletedEvents(ctx)
		if readErr != nil {
			_ = repository.Close()
			return nil, readErr
		}
		if len(completed) > len(trace.Events) {
			_ = repository.Close()
			return nil, fmt.Errorf("%s replay progress exceeds prepared trace", setting)
		}
		for index, item := range completed {
			if !reflect.DeepEqual(item.Event, trace.Events[index]) {
				_ = repository.Close()
				return nil, fmt.Errorf("%s replay committed event %d conflicts with prepared trace", setting, index)
			}
		}
		coordinator, next, recoverErr := RecoverReplayCoordinator(ctx, repository, setting, trace.InitHeights, config.CostProfile, policy)
		if recoverErr != nil {
			_ = repository.Close()
			return nil, fmt.Errorf("recover %s replay: %w", setting, recoverErr)
		}
		for sequence := next; sequence < uint64(len(trace.Events)); sequence++ {
			if err := ctx.Err(); err != nil {
				_ = repository.Close()
				return nil, err
			}
			event := trace.Events[sequence]
			decision, processErr := coordinator.Process(event)
			if processErr != nil {
				_ = repository.Close()
				return nil, fmt.Errorf("process %s replay event %d: %w", setting, sequence, processErr)
			}
			var snapshot *ReplaySnapshot
			if setting.UsesTrustMap() && config.RecordEvery > 0 && sequence%config.RecordEvery == 0 {
				snapshot = &ReplaySnapshot{Sequence: sequence, Time: event.SourceTime, GraphNodes: decision.GraphNodes, GraphEdges: decision.GraphEdges, CrossEdgesAdded: decision.CrossEdgesAdded}
			}
			if err := repository.CommitEvent(ctx, event, decision, snapshot); err != nil {
				_ = repository.Close()
				return nil, fmt.Errorf("commit %s replay event %d: %w", setting, sequence, err)
			}
		}
		if err := ExportReplayRun(ctx, repository, trace, config, setting, policy); err != nil {
			_ = repository.Close()
			return nil, fmt.Errorf("export %s replay: %w", setting, err)
		}
		if err := repository.Close(); err != nil {
			return nil, fmt.Errorf("close %s replay database: %w", setting, err)
		}
		results = append(results, ReplayRunResult{Setting: setting, RunID: identity.RunID, RunDirectory: runDir, CompletedEvents: uint64(len(trace.Events))})
	}
	return results, nil
}

func NewReplayRunIdentity(config Config, trace PreparedTrace, setting Setting, policy CheckpointPolicy) (ReplayRunIdentity, error) {
	selection, err := json.Marshal(struct {
		Allowlist        []string         `json:"allowlist"`
		MaxEvents        uint64           `json:"max_events"`
		InvalidRowPolicy InvalidRowPolicy `json:"invalid_row_policy"`
	}{config.ChainAllowlist, config.MaxEvents, config.InvalidRowPolicy})
	if err != nil {
		return ReplayRunIdentity{}, err
	}
	selectionDigest := sha256.Sum256(selection)
	identity := ReplayRunIdentity{
		Setting: setting, TraceDigest: trace.Digest, PreparedRows: uint64(len(trace.Events)),
		ImplementationVersion: ReplayImplementationVersion, SchemaVersion: ReplaySchemaVersion,
		GitCommit: ReplayGitCommit, CostProfileFingerprint: config.CostProfile.Fingerprint(),
		CheckpointPolicyFingerprint: policy.Fingerprint(), SelectionFingerprint: hex.EncodeToString(selectionDigest[:]),
		RecordEvery: config.RecordEvery, LogEvery: config.LogEvery,
	}
	material, err := json.Marshal(identity)
	if err != nil {
		return ReplayRunIdentity{}, err
	}
	digest := sha256.Sum256(append([]byte("trustmap/replay-run/v1\n"), material...))
	identity.RunID = hex.EncodeToString(digest[:])
	return identity, nil
}
