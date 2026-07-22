package replay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLoadsStrictYAMLAndValidatesMatrixIsolation(t *testing.T) {
	directory := t.TempDir()
	trace := filepath.Join(directory, "trace.csv")
	if err := os.WriteFile(trace, []byte("src_chain,dst_chain,src_block_number,dst_block_number,src_block_time\na,b,1,2,2025-12-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(directory, "runs")
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "replay.yaml")
	configYAML := "version: 1\n" +
		"input_trace: " + trace + "\n" +
		"expected_digest: ''\n" +
		"run_root: " + runRoot + "\n" +
		"settings: [B0, B1, B2, B3]\n" +
		"invalid_row_policy: reject\n" +
		"cost_profile:\n" +
		"  id: legacy-v4.1\n" +
		"  direct_step_cost: 3000000\n" +
		"  path_step_cost: 30000\n" +
		"  trust_root_update_cost: 110000\n" +
		"checkpoint_periods:\n" +
		"  a: 40\n" +
		"chain_allowlist: [A, b]\n" +
		"max_events: 10\n" +
		"record_every: 5\n" +
		"log_every: 2\n" +
		"durability:\n" +
		"  synchronous: full\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(config.Settings) != 4 || config.Settings[3] != SettingB3 {
		t.Fatalf("settings = %#v", config.Settings)
	}
	if got := strings.Join(config.ChainAllowlist, ","); got != "a,b" {
		t.Fatalf("allowlist = %q", got)
	}
	for _, setting := range config.Settings {
		directory := config.SettingRunDir(setting)
		if !strings.HasPrefix(directory, runRoot+string(os.PathSeparator)) || filepath.Base(directory) != strings.ToLower(string(setting)) {
			t.Fatalf("setting run dir %s is not isolated under %s", directory, runRoot)
		}
	}
}

func TestConfigRejectsUnknownFieldsDigestMismatchAndUnsafeValues(t *testing.T) {
	directory := t.TempDir()
	trace := filepath.Join(directory, "trace.csv")
	if err := os.WriteFile(trace, []byte("src_chain,dst_chain,src_block_number,dst_block_number,src_block_time\na,b,1,2,2025-12-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(directory, "runs")
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	base := "version: 1\ninput_trace: " + trace + "\nrun_root: " + runRoot + "\nsettings: [B2]\ninvalid_row_policy: reject\ncost_profile:\n  id: x\n  direct_step_cost: 1\n  path_step_cost: 1\n  trust_root_update_cost: 1\ndurability:\n  synchronous: normal\n"

	unknown := filepath.Join(directory, "unknown.yaml")
	if err := os.WriteFile(unknown, []byte(base+"surprise: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(unknown); err == nil {
		t.Fatal("unknown YAML field accepted")
	}

	badDigest := filepath.Join(directory, "digest.yaml")
	if err := os.WriteFile(badDigest, []byte(strings.Replace(base, "run_root:", "expected_digest: deadbeef\nrun_root:", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(badDigest); err == nil {
		t.Fatal("digest mismatch accepted")
	}

	for name, mutation := range map[string]string{
		"duplicate setting": strings.Replace(base, "settings: [B2]", "settings: [B2, B2]", 1),
		"zero max":          base + "max_events: 0\n",
		"relative run root": strings.Replace(base, runRoot, "runs", 1),
		"unknown sync":      strings.Replace(base, "synchronous: normal", "synchronous: fastest", 1),
	} {
		path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".yaml")
		if err := os.WriteFile(path, []byte(mutation), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestConfigRejectsInputUnderRunRootAndAliasedSettingDirectories(t *testing.T) {
	directory := t.TempDir()
	runRoot := filepath.Join(directory, "runs")
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(name, trace string, settings string) string {
		path := filepath.Join(directory, name+".yaml")
		body := "version: 1\ninput_trace: " + trace + "\nrun_root: " + runRoot + "\nsettings: " + settings + "\ninvalid_row_policy: reject\ncost_profile:\n  id: x\n  direct_step_cost: 1\n  path_step_cost: 1\n  trust_root_update_cost: 1\ndurability:\n  synchronous: full\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	inside := filepath.Join(runRoot, "trace.csv")
	if err := os.WriteFile(inside, []byte("src_chain,dst_chain,src_block_number,dst_block_number,src_block_time\na,b,1,2,2025-12-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(writeConfig("inside", inside, "[B0]")); err == nil {
		t.Fatal("input trace beneath run_root accepted")
	}

	outside := filepath.Join(directory, "outside.csv")
	if err := os.WriteFile(outside, []byte("src_chain,dst_chain,src_block_number,dst_block_number,src_block_time\na,b,1,2,2025-12-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(directory, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(runRoot, "b0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(runRoot, "b1")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(writeConfig("alias", outside, "[B0, B1]")); err == nil {
		t.Fatal("aliased setting output directories accepted")
	}
}
