package replay

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func ExportReplayRun(ctx context.Context, repository *ReplayRepository, trace PreparedTrace, config Config, setting Setting, policy CheckpointPolicy) error {
	runDir := config.SettingRunDir(setting)
	if err := writeCSVAtomic(filepath.Join(runDir, "decisions.csv"), legacyDecisionHeader(setting), func(writer *csv.Writer) error {
		return repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
			if err := writer.Write(legacyDecisionRow(setting, item.Event, item.Decision)); err != nil {
				return err
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if setting.UsesTrustMap() {
		if err := writeCSVAtomic(filepath.Join(runDir, "paths.csv"), legacyPathHeader(setting), func(writer *csv.Writer) error {
			return repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
				if item.Decision.Decision == ReplayDecisionTrustMap {
					if err := writer.Write(legacyPathRow(setting, item.Event, item.Decision)); err != nil {
						return err
					}
				}
				return nil
			})
		}); err != nil {
			return err
		}
		if err := writeCSVAtomic(filepath.Join(runDir, "paths_extended.csv"), extendedPathHeader(), func(writer *csv.Writer) error {
			return repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
				if item.Decision.Decision == ReplayDecisionTrustMap {
					if err := writer.Write(extendedPathRow(item.Event, item.Decision, config.CostProfile)); err != nil {
						return err
					}
				}
				return nil
			})
		}); err != nil {
			return err
		}
		if config.RecordEvery > 0 {
			if err := writeCSVAtomic(filepath.Join(runDir, "map_snapshots.csv"), []string{"nodes", "edges_total", "edges_cross_added", "idx", "time"}, func(writer *csv.Writer) error {
				return repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
					if item.Snapshot != nil {
						record := []string{uintText(item.Snapshot.GraphNodes), uintText(item.Snapshot.GraphEdges), uintText(item.Snapshot.CrossEdgesAdded), uintText(item.Snapshot.Sequence), legacyTime(item.Snapshot.Time)}
						if err := writer.Write(record); err != nil {
							return err
						}
					}
					return nil
				})
			}); err != nil {
				return err
			}
		}
	}
	if err := writeCSVAtomic(filepath.Join(runDir, "decisions_extended.csv"), extendedDecisionHeader(), func(writer *csv.Writer) error {
		return repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
			if err := writer.Write(extendedDecisionRow(item.Event, item.Decision, config.CostProfile)); err != nil {
				return err
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if err := writeCSVAtomic(filepath.Join(runDir, "init_heights.csv"), []string{"chain", "init_height"}, func(writer *csv.Writer) error {
		for _, chain := range trace.Chains {
			if err := writer.Write([]string{chain, uintText(trace.InitHeights[chain])}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if setting == SettingB1 {
		if err := writeJSONAtomic(filepath.Join(runDir, "checkpoint_config_used.json"), policy.Periods()); err != nil {
			return err
		}
	}
	if setting == SettingB3 {
		if err := writeCSVAtomic(filepath.Join(runDir, "checkpoint_periods.csv"), []string{"chain", "checkpoint_period"}, func(writer *csv.Writer) error {
			periods := policy.Periods()
			chains := sortedKeys(periods)
			for _, chain := range chains {
				if err := writer.Write([]string{chain, uintText(periods[chain])}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	progress, err := repository.Progress(ctx)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "progress.json"), progress); err != nil {
		return err
	}
	summary, err := replaySummary(ctx, repository, setting, config, policy)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "summary.json"), summary); err != nil {
		return err
	}
	manifest := map[string]any{
		"run_identity":                         repository.identity,
		"input_digest":                         trace.Digest,
		"source_rows":                          trace.SourceRows,
		"prepared_rows":                        len(trace.Events),
		"chain_count":                          len(trace.Chains),
		"chains":                               trace.Chains,
		"cost_profile":                         config.CostProfile,
		"cost_profile_fingerprint":             config.CostProfile.Fingerprint(),
		"checkpoint_configured_chains":         policy.ConfiguredChainCount(),
		"checkpoint_effective_workload_chains": policy.EffectiveWorkloadChainCount(trace.Chains),
		"progress":                             progress,
	}
	return writeJSONAtomic(filepath.Join(runDir, "run_manifest.json"), manifest)
}

func legacyDecisionHeader(setting Setting) []string {
	base := []string{"idx", "time", "src_chain", "dst_chain", "src_block_number", "dst_block_number", "bridge_name", "tx_count", "volume_usd", "baseline_before", "baseline_after"}
	switch setting {
	case SettingB0, SettingB1:
		return append(base, "direct_cost", "trust_cost", "decision", "chosen_cost", "saving")
	case SettingB2:
		return append(base, "direct_cost", "trust_cost", "trustmap_update_cost", "trust_cost_total", "decision", "chosen_cost", "saving")
	default:
		return append(base, "direct_start_h", "direct_blocks", "direct_used_checkpoint", "checkpoint_period", "checkpoint_h", "direct_cost", "trust_cost", "trustmap_update_cost", "trust_cost_total", "decision", "chosen_cost", "saving")
	}
}

func legacyDecisionRow(setting Setting, event ReplayEvent, decision ReplayDecision) []string {
	row := legacyEventFields(event)
	row = append(row, uintText(decision.BaselineBefore), uintText(decision.BaselineAfter))
	saving := decision.DirectCost - decision.ChosenCost
	if setting == SettingB0 || setting == SettingB1 {
		return append(row, costText(decision.DirectCost), "", string(decision.Decision), costText(decision.ChosenCost), costText(saving))
	}
	trustCost, updateCost, totalCost := "", costText(0), ""
	if decision.Decision == ReplayDecisionTrustMap {
		trustCost, updateCost, totalCost = costText(decision.PathCost), costText(decision.TrustMapTotalCost-decision.PathCost), costText(decision.TrustMapTotalCost)
	}
	if setting == SettingB2 {
		return append(row, costText(decision.DirectCost), trustCost, updateCost, totalCost, string(decision.Decision), costText(decision.ChosenCost), costText(saving))
	}
	period, checkpoint := "", ""
	if decision.CheckpointConfigured {
		period, checkpoint = costText(decision.CheckpointPeriod), costText(decision.CheckpointHeight)
	}
	return append(row, uintText(decision.DirectStart), uintText(decision.DirectBlocks), pythonBool(decision.CheckpointApplied), period, checkpoint, costText(decision.DirectCost), trustCost, updateCost, totalCost, string(decision.Decision), costText(decision.ChosenCost), costText(saving))
}

func legacyPathHeader(setting Setting) []string {
	base := []string{"idx", "time", "src_chain", "dst_chain", "src_block_number", "dst_block_number", "bridge_name", "baseline_before", "direct_cost", "trust_cost", "trustmap_update_cost", "trust_cost_total", "saving", "path", "path_len"}
	if setting == SettingB2 {
		return append(base, "path_hops", "path_xchain_hops", "path_height_steps", "path_jump_segments", "path_cost_check", "path_segments_json")
	}
	return base
}

func legacyPathRow(setting Setting, event ReplayEvent, decision ReplayDecision) []string {
	saving := decision.DirectCost - decision.ChosenCost
	row := []string{uintText(event.Sequence), legacyTime(event.SourceTime), event.Source.Chain, event.Destination.Chain, uintText(event.Source.OriginalHeight), uintText(event.Destination.OriginalHeight), event.BridgeName, uintText(decision.BaselineBefore), costText(decision.DirectCost), costText(decision.PathCost), costText(decision.TrustMapTotalCost - decision.PathCost), costText(decision.TrustMapTotalCost), costText(saving), pathText(decision.Path), uintText(decision.Path.NodeCount())}
	if setting == SettingB2 {
		xchain, heightSteps, jumps := pathAudit(decision.Path)
		row = append(row, uintText(decision.Path.HopCount()), uintText(xchain), uintText(heightSteps), uintText(jumps), costText(decision.Path.Cost), pathSegmentsLegacyJSON(decision.Path))
	}
	return row
}

func extendedDecisionHeader() []string {
	return []string{"event_id", "idx", "original_row", "normalized_src_height", "normalized_dst_height", "decision", "direct_cost", "chosen_cost", "path_hops", "path_xchain_hops", "path_height_steps", "path_jump_segments", "path_cost_check", "path_segments_json", "cost_profile_id", "cost_profile_fingerprint"}
}

func extendedDecisionRow(event ReplayEvent, decision ReplayDecision, profile CostProfile) []string {
	xchain, heightSteps, jumps := pathAudit(decision.Path)
	return []string{event.ID, uintText(event.Sequence), uintText(event.OriginalRow), uintText(event.Source.NormalizedHeight), uintText(event.Destination.NormalizedHeight), string(decision.Decision), uintText(decision.DirectCost), uintText(decision.ChosenCost), uintText(decision.Path.HopCount()), uintText(xchain), uintText(heightSteps), uintText(jumps), uintText(decision.Path.Cost), pathSegmentsCompactJSON(decision.Path), profile.ID, profile.Fingerprint()}
}

func extendedPathHeader() []string { return extendedDecisionHeader() }
func extendedPathRow(event ReplayEvent, decision ReplayDecision, profile CostProfile) []string {
	return extendedDecisionRow(event, decision, profile)
}

func legacyEventFields(event ReplayEvent) []string {
	return []string{uintText(event.Sequence), legacyTime(event.SourceTime), event.Source.Chain, event.Destination.Chain, uintText(event.Source.OriginalHeight), uintText(event.Destination.OriginalHeight), event.BridgeName, strconv.FormatInt(event.TxCount, 10), strconv.FormatFloat(event.VolumeUSD, 'g', -1, 64)}
}

func legacyTime(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05.999999999-07:00")
}
func uintText(value uint64) string { return strconv.FormatUint(value, 10) }
func costText(value uint64) string { return strconv.FormatUint(value, 10) + ".0" }
func pythonBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

func pathText(path ReplayPath) string {
	parts := make([]string, len(path.Nodes))
	for index, node := range path.Nodes {
		parts[index] = node.Chain + ":" + uintText(node.Height)
	}
	return strings.Join(parts, " -> ")
}

func pathAudit(path ReplayPath) (xchain, heightSteps, jumps uint64) {
	for _, segment := range path.Segments {
		if segment.Kind == ReplayVerifiedDependencyEdgeKind {
			xchain++
			continue
		}
		delta := segment.From.Height
		if segment.To.Height > delta {
			delta = segment.To.Height - delta
		} else {
			delta -= segment.To.Height
		}
		heightSteps += delta
		if delta > 1 {
			jumps++
		}
	}
	return
}

func pathSegmentsCompactJSON(path ReplayPath) string {
	type segment struct {
		Index  uint64 `json:"i"`
		From   string `json:"from"`
		To     string `json:"to"`
		Kind   string `json:"kind"`
		Delta  uint64 `json:"delta_h"`
		Weight uint64 `json:"w"`
	}
	items := make([]segment, 0, len(path.Segments))
	for index, edge := range path.Segments {
		delta := edge.From.Height
		if edge.To.Height > delta {
			delta = edge.To.Height - delta
		} else {
			delta -= edge.To.Height
		}
		kind := "intra"
		if edge.Kind == ReplayVerifiedDependencyEdgeKind {
			kind, delta = "xchain", 0
		}
		items = append(items, segment{uint64(index), edge.From.Chain + ":" + uintText(edge.From.Height), edge.To.Chain + ":" + uintText(edge.To.Height), kind, delta, edge.Weight})
	}
	data, _ := json.Marshal(items)
	return string(data)
}

func pathSegmentsLegacyJSON(path ReplayPath) string {
	compact := pathSegmentsCompactJSON(path)
	compact = strings.ReplaceAll(compact, ":", ": ")
	compact = strings.ReplaceAll(compact, ",", ", ")
	// Restore the colon in chain:block values changed by the lexical spacing.
	for _, node := range path.Nodes {
		spaced := node.Chain + ": " + uintText(node.Height)
		compact = strings.ReplaceAll(compact, spaced, node.Chain+":"+uintText(node.Height))
	}
	// Legacy pandas/json output represented estimated costs as floats.
	for _, segment := range path.Segments {
		compact = strings.Replace(compact, `"w": `+uintText(segment.Weight), `"w": `+costText(segment.Weight), 1)
	}
	return compact
}

func replaySummary(ctx context.Context, repository *ReplayRepository, setting Setting, config Config, policy CheckpointPolicy) (any, error) {
	var directTotal, chosenTotal, savingTotal float64
	var trustMapRows uint64
	var snapshots uint64
	var trustMapRowsWithJumps, trustMapJumpSegments uint64
	var rows uint64
	var lastGraphNodes, lastGraphEdges, lastCrossEdges uint64
	if err := repository.ForEachCompleted(ctx, func(item ReplayCommittedEvent) error {
		directTotal += float64(item.Decision.DirectCost)
		chosenTotal += float64(item.Decision.ChosenCost)
		savingTotal += float64(item.Decision.DirectCost - item.Decision.ChosenCost)
		if item.Decision.Decision == ReplayDecisionTrustMap {
			trustMapRows++
			_, _, jumps := pathAudit(item.Decision.Path)
			if jumps > 0 {
				trustMapRowsWithJumps++
				trustMapJumpSegments += jumps
			}
		}
		if item.Snapshot != nil {
			snapshots++
		}
		lastGraphNodes, lastGraphEdges, lastCrossEdges = item.Decision.GraphNodes, item.Decision.GraphEdges, item.Decision.CrossEdgesAdded
		rows++
		return nil
	}); err != nil {
		return nil, err
	}
	averageDirect, averageChosen, ratio := 0.0, 0.0, 0.0
	if rows > 0 {
		averageDirect, averageChosen, ratio = directTotal/float64(rows), chosenTotal/float64(rows), float64(trustMapRows)/float64(rows)
	}
	result := map[string]any{"rows": rows, "trustmap_rows": trustMapRows, "trustmap_ratio": ratio, "avg_direct_cost": averageDirect, "avg_chosen_cost": averageChosen, "total_saving": savingTotal}
	if setting == SettingB0 || setting == SettingB1 {
		result["enable_checkpoint"] = setting == SettingB1
		if setting == SettingB1 {
			result["checkpoint_chains_enabled"] = policy.ConfiguredChainCount()
		} else {
			result["checkpoint_chains_enabled"] = 0
		}
		return result, nil
	}
	result["trustmap_update_cost"] = float64(config.CostProfile.TrustRootUpdateCost)
	if rows > 0 {
		result["graph"] = map[string]any{"nodes": lastGraphNodes, "edges_total": lastGraphEdges, "edges_cross_added": lastCrossEdges}
	}
	result["record_every"] = config.RecordEvery
	result["graph_snapshots_rows"] = snapshots
	if setting == SettingB2 {
		result["trustmap_rows_with_height_jumps"] = trustMapRowsWithJumps
		result["trustmap_jump_segments_total"] = trustMapJumpSegments
	}
	if setting == SettingB3 {
		result["checkpoint_chains_enabled"] = policy.ConfiguredChainCount()
	}
	return result, nil
}

func writeCSVAtomic(path string, header []string, rows func(*csv.Writer) error) error {
	return atomicWrite(path, func(output io.Writer) error {
		writer := csv.NewWriter(output)
		if err := writer.Write(header); err != nil {
			return err
		}
		if err := rows(writer); err != nil {
			return err
		}
		writer.Flush()
		return writer.Error()
	})
}

func writeJSONAtomic(path string, value any) error {
	return atomicWrite(path, func(output io.Writer) error {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	})
}

func atomicWrite(path string, write func(io.Writer) error) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".replay-export-*")
	if err != nil {
		return fmt.Errorf("create replay export %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write replay export %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync replay export %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close replay export %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish replay export %s: %w", path, err)
	}
	ok = true
	return nil
}

func sortedKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
