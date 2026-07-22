package replay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyExportHeadersPreservePerSettingSchemas(t *testing.T) {
	want := map[Setting]string{
		SettingB0: "idx,time,src_chain,dst_chain,src_block_number,dst_block_number,bridge_name,tx_count,volume_usd,baseline_before,baseline_after,direct_cost,trust_cost,decision,chosen_cost,saving",
		SettingB1: "idx,time,src_chain,dst_chain,src_block_number,dst_block_number,bridge_name,tx_count,volume_usd,baseline_before,baseline_after,direct_cost,trust_cost,decision,chosen_cost,saving",
		SettingB2: "idx,time,src_chain,dst_chain,src_block_number,dst_block_number,bridge_name,tx_count,volume_usd,baseline_before,baseline_after,direct_cost,trust_cost,trustmap_update_cost,trust_cost_total,decision,chosen_cost,saving",
		SettingB3: "idx,time,src_chain,dst_chain,src_block_number,dst_block_number,bridge_name,tx_count,volume_usd,baseline_before,baseline_after,direct_start_h,direct_blocks,direct_used_checkpoint,checkpoint_period,checkpoint_h,direct_cost,trust_cost,trustmap_update_cost,trust_cost_total,decision,chosen_cost,saving",
	}
	for setting, header := range want {
		if got := strings.Join(legacyDecisionHeader(setting), ","); got != header {
			t.Errorf("%s decisions header = %s", setting, got)
		}
	}
	if got := len(legacyPathHeader(SettingB2)); got != 21 {
		t.Fatalf("B2 paths columns = %d", got)
	}
	if got := len(legacyPathHeader(SettingB3)); got != 15 {
		t.Fatalf("B3 paths columns = %d", got)
	}
}

func TestLegacyDecisionRowsUseFloatCostAndCheckpointLexemes(t *testing.T) {
	event := ReplayEvent{Sequence: 7, Source: ReplayBlock{Chain: "a", OriginalHeight: 19}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 2}, SourceTime: time.Date(2025, 12, 1, 0, 0, 7, 0, time.UTC), TxCount: 1, VolumeUSD: 2.5}
	decision := ReplayDecision{Decision: ReplayDecisionDirect, BaselineBefore: 10, BaselineAfter: 19, DirectStart: 10, DirectBlocks: 9, CheckpointConfigured: true, CheckpointPeriod: 10, CheckpointHeight: 10, DirectCost: 900, ChosenCost: 900}
	row := legacyDecisionRow(SettingB3, event, decision)
	if row[1] != "2025-12-01 00:00:07+00:00" || row[13] != "False" || row[14] != "10.0" || row[15] != "10.0" || row[16] != "900.0" || row[18] != "0.0" {
		t.Fatalf("B3 lexical row = %#v", row)
	}
}

func TestRunReplayExportsSettingSpecificCheckpointFilesAndCounts(t *testing.T) {
	directory := t.TempDir()
	tracePath := filepath.Join(directory, "trace.csv")
	if err := os.WriteFile(tracePath, []byte("src_chain,dst_chain,src_block_number,dst_block_number,src_block_time\na,b,19,1,2025-12-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(directory, "runs")
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	flat, _ := NewCheckpointPolicy(map[string]uint64{"a": 10})
	b3, _ := NewCheckpointPolicy(map[string]uint64{"a": 20, "placeholder": 30})
	config := Config{Version: 1, InputTrace: tracePath, RunRoot: runRoot, Settings: []Setting{SettingB1, SettingB3}, CostProfile: CostProfile{ID: "x", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}, CheckpointPolicy: flat, CheckpointPolicies: map[Setting]CheckpointPolicy{SettingB3: b3}, Durability: DurabilityConfig{Synchronous: "full"}}
	if _, err := RunReplay(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runRoot, "b1", "checkpoint_config_used.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runRoot, "b1", "checkpoint_periods.csv")); !os.IsNotExist(err) {
		t.Fatalf("B1 unexpectedly exported checkpoint_periods.csv: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runRoot, "b3", "checkpoint_periods.csv")); err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(runRoot, "b3", "run_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["checkpoint_configured_chains"] != float64(2) || manifest["checkpoint_effective_workload_chains"] != float64(1) {
		t.Fatalf("checkpoint counts = %#v", manifest)
	}
}
