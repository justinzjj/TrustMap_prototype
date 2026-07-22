package replay

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReplayIsFiniteResumableAndUsesIsolatedSettingDatabases(t *testing.T) {
	directory := t.TempDir()
	tracePath := filepath.Join(directory, "trace.csv")
	traceCSV := "src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time\n" +
		"a,b,bridge,10,1,1,2.5,2025-12-01T00:00:00Z\n" +
		"b,c,bridge,1,1,1,3.5,2025-12-01T00:00:01Z\n" +
		"a,c,bridge,20,1,1,4.5,2025-12-01T00:00:02Z\n"
	if err := os.WriteFile(tracePath, []byte(traceCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(directory, "runs")
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, _ := NewCheckpointPolicy(nil)
	config := Config{
		Version: 1, InputTrace: tracePath, RunRoot: runRoot, Settings: []Setting{SettingB0, SettingB2},
		InvalidRowPolicy: InvalidRowReject,
		CostProfile:      CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5},
		CheckpointPolicy: policy, RecordEvery: 1, Durability: DurabilityConfig{Synchronous: "full"},
	}
	results, err := RunReplay(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].CompletedEvents != 3 || results[1].CompletedEvents != 3 {
		t.Fatalf("results = %#v", results)
	}
	for _, setting := range config.Settings {
		for _, name := range []string{"replay.db", "decisions.csv", "summary.json", "decisions_extended.csv", "run_manifest.json", "progress.json"} {
			if _, err := os.Stat(filepath.Join(config.SettingRunDir(setting), name)); err != nil {
				t.Fatalf("%s %s: %v", setting, name, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(config.SettingRunDir(SettingB0), "paths.csv")); !os.IsNotExist(err) {
		t.Fatalf("B0 paths.csv must not exist: %v", err)
	}
	if _, err := RunReplay(context.Background(), config); err != nil {
		t.Fatalf("resume completed run: %v", err)
	}
	file, err := os.Open(filepath.Join(config.SettingRunDir(SettingB2), "decisions.csv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(file).ReadAll()
	_ = file.Close()
	if err != nil || len(rows) != 4 {
		t.Fatalf("resumed decisions rows=%d err=%v", len(rows), err)
	}
	if got := strings.Join(rows[0], ","); got != strings.Join(legacyDecisionHeader(SettingB2), ",") {
		t.Fatalf("B2 header = %s", got)
	}
}
