package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
)

const ReplayImplementationVersion = "replay-v1"

// ReplayGitCommit may be set with -ldflags for published experiment binaries.
var ReplayGitCommit = "unknown"

var replayIdentityCache struct {
	sync.Once
	value string
	err   error
}

func replayImplementationIdentity() (string, error) {
	replayIdentityCache.Do(func() {
		if configured := strings.TrimSpace(ReplayGitCommit); configured != "" && configured != "unknown" {
			replayIdentityCache.value = configured
			return
		}
		if build, ok := debug.ReadBuildInfo(); ok {
			var revision string
			modified := false
			for _, setting := range build.Settings {
				switch setting.Key {
				case "vcs.revision":
					revision = strings.TrimSpace(setting.Value)
				case "vcs.modified":
					modified = setting.Value == "true"
				}
			}
			if revision != "" && !modified {
				replayIdentityCache.value = "git:" + revision
				return
			}
		}
		executable, err := os.Executable()
		if err != nil {
			replayIdentityCache.err = fmt.Errorf("locate replay executable for implementation identity: %w", err)
			return
		}
		file, err := os.Open(executable)
		if err != nil {
			replayIdentityCache.err = fmt.Errorf("open replay executable for implementation identity: %w", err)
			return
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			replayIdentityCache.err = fmt.Errorf("hash replay executable for implementation identity: %w", err)
			return
		}
		replayIdentityCache.value = "binary-sha256:" + hex.EncodeToString(digest.Sum(nil))
	})
	return replayIdentityCache.value, replayIdentityCache.err
}

type ReplayRunResult struct {
	Setting         Setting
	RunID           string
	RunDirectory    string
	CompletedEvents uint64
}

func RunReplay(ctx context.Context, config Config) ([]ReplayRunResult, error) {
	return RunReplayWithProgress(ctx, config, io.Discard)
}

// RunReplayWithProgress runs the same finite replay and emits bounded,
// line-oriented progress messages. A nil writer is treated as io.Discard.
func RunReplayWithProgress(ctx context.Context, config Config, progressWriter io.Writer) ([]ReplayRunResult, error) {
	if progressWriter == nil {
		progressWriter = io.Discard
	}
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
		coordinator, next, trustMapRows, recoverErr := recoverReplayCoordinator(ctx, repository, setting, trace.InitHeights, config.CostProfile, policy, func(item ReplayCommittedEvent) error {
			index := item.Event.Sequence
			if index >= uint64(len(trace.Events)) {
				return fmt.Errorf("%s replay progress exceeds prepared trace", setting)
			}
			if !reflect.DeepEqual(item.Event, trace.Events[index]) {
				return fmt.Errorf("%s replay committed event %d conflicts with prepared trace", setting, index)
			}
			return nil
		})
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
			if decision.Decision == ReplayDecisionTrustMap {
				trustMapRows++
			}
			completedCount := sequence + 1
			if config.LogEvery > 0 && (completedCount%config.LogEvery == 0 || completedCount == uint64(len(trace.Events))) {
				fmt.Fprintf(progressWriter, "mapnode replay progress setting=%s completed=%d/%d trustmap=%d graph_nodes=%d graph_edges=%d\n", setting, completedCount, len(trace.Events), trustMapRows, decision.GraphNodes, decision.GraphEdges)
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
	implementationIdentity, err := replayImplementationIdentity()
	if err != nil {
		return ReplayRunIdentity{}, err
	}
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
		GitCommit: implementationIdentity, CostProfileFingerprint: config.CostProfile.Fingerprint(),
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
