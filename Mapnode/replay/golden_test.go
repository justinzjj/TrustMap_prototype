package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckReplayGoldenValidatesManifestSummaryAndGraph(t *testing.T) {
	runRoot := t.TempDir()
	settingDir := filepath.Join(runRoot, "b2")
	if err := os.Mkdir(settingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"input_digest":"abc","prepared_rows":6,"chain_count":3,"cost_profile":{"id":"legacy-v4.1"}}`
	summary := `{"rows":6,"trustmap_rows":1,"avg_direct_cost":100.5,"avg_chosen_cost":80.25,"total_saving":121.5,"graph":{"nodes":11,"edges_total":22,"edges_cross_added":6}}`
	if err := os.WriteFile(filepath.Join(settingDir, "run_manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingDir, "summary.json"), []byte(summary), 0o600); err != nil {
		t.Fatal(err)
	}
	decisions := "idx,direct_cost,chosen_cost,decision\n0,100,80,direct\n1,100,80,direct\n2,100,80,direct\n3,100,80,direct\n4,100,80,direct\n5,100,75,trustmap\n"
	paths := "idx,trust_cost,path\n5,70,b:1 -> a:2\n"
	if err := os.WriteFile(filepath.Join(settingDir, "decisions.csv"), []byte(decisions), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingDir, "paths.csv"), []byte(paths), 0o600); err != nil {
		t.Fatal(err)
	}
	digests, err := ComputeReplaySemanticDigests(settingDir, true)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join(t.TempDir(), "golden.json")
	golden := fmt.Sprintf(`{"version":1,"trace_digest":"abc","prepared_rows":6,"chain_count":3,"cost_profile_id":"legacy-v4.1","settings":{"B2":{"trustmap_rows":1,"avg_direct_cost":100.5,"avg_chosen_cost":80.25,"total_saving":121.5,"decision_digest":%q,"path_digest":%q,"graph":{"nodes":11,"edges_total":22,"edges_cross_added":6}}}}`, digests.DecisionDigest, digests.PathDigest)
	if err := os.WriteFile(goldenPath, []byte(golden), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckReplayGolden(runRoot, goldenPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingDir, "summary.json"), []byte(strings.Replace(summary, `"avg_direct_cost":100.5`, `"avg_direct_cost":101.5`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckReplayGolden(runRoot, goldenPath); err == nil || !strings.Contains(err.Error(), "avg_direct_cost") {
		t.Fatalf("average mismatch error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingDir, "summary.json"), []byte(strings.Replace(summary, `"trustmap_rows":1`, `"trustmap_rows":2`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckReplayGolden(runRoot, goldenPath); err == nil || !strings.Contains(err.Error(), "trustmap_rows") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestReplaySemanticDigestsCoverRowsAndNormalizeIntegerCosts(t *testing.T) {
	writeArtifacts := func(directory, direct, chosen, trust string) {
		t.Helper()
		decisions := "idx,direct_cost,chosen_cost,decision\n0," + direct + "," + chosen + ",trustmap\n"
		paths := "idx,trust_cost,path\n0," + trust + ",b:1 -> a:2\n"
		if err := os.WriteFile(filepath.Join(directory, "decisions.csv"), []byte(decisions), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "paths.csv"), []byte(paths), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := t.TempDir()
	integer := t.TempDir()
	writeArtifacts(legacy, "100.0", "40.0", "30.0")
	writeArtifacts(integer, "100", "40", "30")
	first, err := ComputeReplaySemanticDigests(legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeReplaySemanticDigests(integer, true)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.DecisionRows != 1 || first.PathRows != 1 || first.DecisionDigest == "" || first.PathDigest == "" {
		t.Fatalf("semantic digests = %#v / %#v", first, second)
	}
}
