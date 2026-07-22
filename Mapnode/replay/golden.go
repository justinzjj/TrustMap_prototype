package replay

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

type ReplayGoldenGraph struct {
	Nodes           uint64 `json:"nodes"`
	EdgesTotal      uint64 `json:"edges_total"`
	EdgesCrossAdded uint64 `json:"edges_cross_added"`
}

type ReplayGoldenSetting struct {
	TrustMapRows   uint64             `json:"trustmap_rows"`
	AverageDirect  *float64           `json:"avg_direct_cost,omitempty"`
	AverageChosen  *float64           `json:"avg_chosen_cost,omitempty"`
	TotalSaving    *float64           `json:"total_saving,omitempty"`
	DecisionDigest string             `json:"decision_digest,omitempty"`
	PathDigest     string             `json:"path_digest,omitempty"`
	Graph          *ReplayGoldenGraph `json:"graph,omitempty"`
}

type ReplaySemanticDigests struct {
	DecisionRows   uint64 `json:"decision_rows"`
	DecisionDigest string `json:"decision_digest"`
	PathRows       uint64 `json:"path_rows"`
	PathDigest     string `json:"path_digest,omitempty"`
}

type ReplayGolden struct {
	Version       uint64                          `json:"version"`
	TraceDigest   string                          `json:"trace_digest"`
	PreparedRows  uint64                          `json:"prepared_rows"`
	ChainCount    uint64                          `json:"chain_count"`
	CostProfileID string                          `json:"cost_profile_id"`
	Settings      map[Setting]ReplayGoldenSetting `json:"settings"`
}

type replayGoldenManifest struct {
	InputDigest  string `json:"input_digest"`
	PreparedRows uint64 `json:"prepared_rows"`
	ChainCount   uint64 `json:"chain_count"`
	CostProfile  struct {
		ID string `json:"id"`
	} `json:"cost_profile"`
}

type replayGoldenSummary struct {
	Rows          uint64             `json:"rows"`
	TrustMapRows  uint64             `json:"trustmap_rows"`
	AverageDirect float64            `json:"avg_direct_cost"`
	AverageChosen float64            `json:"avg_chosen_cost"`
	TotalSaving   float64            `json:"total_saving"`
	Graph         *ReplayGoldenGraph `json:"graph"`
}

// CheckReplayGolden compares published replay artifacts, rather than internal
// planner state, so it can also validate runs produced by a packaged binary.
func CheckReplayGolden(runRoot, goldenPath string) error {
	var golden ReplayGolden
	if err := readReplayJSON(goldenPath, &golden); err != nil {
		return fmt.Errorf("read replay golden: %w", err)
	}
	if golden.Version != 1 || strings.TrimSpace(golden.TraceDigest) == "" || golden.PreparedRows == 0 || golden.ChainCount == 0 || strings.TrimSpace(golden.CostProfileID) == "" || len(golden.Settings) == 0 {
		return fmt.Errorf("invalid replay golden metadata")
	}
	for setting, expected := range golden.Settings {
		if _, err := ParseSetting(string(setting)); err != nil {
			return fmt.Errorf("invalid replay golden setting: %w", err)
		}
		if expected.DecisionDigest == "" {
			return fmt.Errorf("%s replay golden is missing required decision_digest", setting)
		}
		if setting.UsesTrustMap() && expected.PathDigest == "" {
			return fmt.Errorf("%s replay golden is missing required path_digest", setting)
		}
		settingDir := filepath.Join(runRoot, strings.ToLower(string(setting)))
		var manifest replayGoldenManifest
		if err := readReplayJSON(filepath.Join(settingDir, "run_manifest.json"), &manifest); err != nil {
			return fmt.Errorf("%s manifest: %w", setting, err)
		}
		if manifest.InputDigest != golden.TraceDigest {
			return fmt.Errorf("%s input_digest = %s, want %s", setting, manifest.InputDigest, golden.TraceDigest)
		}
		if manifest.PreparedRows != golden.PreparedRows {
			return fmt.Errorf("%s prepared_rows = %d, want %d", setting, manifest.PreparedRows, golden.PreparedRows)
		}
		if manifest.ChainCount != golden.ChainCount {
			return fmt.Errorf("%s chain_count = %d, want %d", setting, manifest.ChainCount, golden.ChainCount)
		}
		if manifest.CostProfile.ID != golden.CostProfileID {
			return fmt.Errorf("%s cost profile = %s, want %s", setting, manifest.CostProfile.ID, golden.CostProfileID)
		}
		var summary replayGoldenSummary
		if err := readReplayJSON(filepath.Join(settingDir, "summary.json"), &summary); err != nil {
			return fmt.Errorf("%s summary: %w", setting, err)
		}
		if summary.Rows != golden.PreparedRows {
			return fmt.Errorf("%s summary rows = %d, want %d", setting, summary.Rows, golden.PreparedRows)
		}
		if summary.TrustMapRows != expected.TrustMapRows {
			return fmt.Errorf("%s trustmap_rows = %d, want %d", setting, summary.TrustMapRows, expected.TrustMapRows)
		}
		if expected.AverageDirect != nil && summary.AverageDirect != *expected.AverageDirect {
			return fmt.Errorf("%s avg_direct_cost = %v, want %v", setting, summary.AverageDirect, *expected.AverageDirect)
		}
		if expected.AverageChosen != nil && summary.AverageChosen != *expected.AverageChosen {
			return fmt.Errorf("%s avg_chosen_cost = %v, want %v", setting, summary.AverageChosen, *expected.AverageChosen)
		}
		if expected.TotalSaving != nil && summary.TotalSaving != *expected.TotalSaving {
			return fmt.Errorf("%s total_saving = %v, want %v", setting, summary.TotalSaving, *expected.TotalSaving)
		}
		{
			digests, err := ComputeReplaySemanticDigests(settingDir, setting.UsesTrustMap())
			if err != nil {
				return fmt.Errorf("%s semantic digests: %w", setting, err)
			}
			if digests.DecisionRows != golden.PreparedRows || digests.DecisionDigest != expected.DecisionDigest {
				return fmt.Errorf("%s decision digest = %s (%d rows), want %s (%d rows)", setting, digests.DecisionDigest, digests.DecisionRows, expected.DecisionDigest, golden.PreparedRows)
			}
			if setting.UsesTrustMap() && (digests.PathRows != expected.TrustMapRows || digests.PathDigest != expected.PathDigest) {
				return fmt.Errorf("%s path digest = %s (%d rows), want %s (%d rows)", setting, digests.PathDigest, digests.PathRows, expected.PathDigest, expected.TrustMapRows)
			}
		}
		if expected.Graph != nil {
			if summary.Graph == nil {
				return fmt.Errorf("%s summary graph is missing", setting)
			}
			if *summary.Graph != *expected.Graph {
				return fmt.Errorf("%s graph = %+v, want %+v", setting, *summary.Graph, *expected.Graph)
			}
		}
	}
	return nil
}

// ComputeReplaySemanticDigests hashes every decision and, optionally, every
// selected path while normalizing legacy float-rendered integer costs.
func ComputeReplaySemanticDigests(artifactDir string, includePaths bool) (ReplaySemanticDigests, error) {
	decisionHash := sha256.New()
	_, _ = io.WriteString(decisionHash, "trustmap/replay-decisions/v1\n")
	decisionRows, err := hashReplayCSV(filepath.Join(artifactDir, "decisions.csv"), []string{"idx", "direct_cost", "chosen_cost", "decision"}, func(row map[string]string) error {
		direct, err := canonicalInteger(row["direct_cost"])
		if err != nil {
			return fmt.Errorf("direct_cost: %w", err)
		}
		chosen, err := canonicalInteger(row["chosen_cost"])
		if err != nil {
			return fmt.Errorf("chosen_cost: %w", err)
		}
		fmt.Fprintf(decisionHash, "%s\x00%s\x00%s\x00%s\n", row["idx"], direct, chosen, row["decision"])
		return nil
	})
	if err != nil {
		return ReplaySemanticDigests{}, err
	}
	result := ReplaySemanticDigests{DecisionRows: decisionRows, DecisionDigest: hex.EncodeToString(decisionHash.Sum(nil))}
	if !includePaths {
		return result, nil
	}
	pathHash := sha256.New()
	_, _ = io.WriteString(pathHash, "trustmap/replay-paths/v1\n")
	pathRows, err := hashReplayCSV(filepath.Join(artifactDir, "paths.csv"), []string{"idx", "trust_cost", "path"}, func(row map[string]string) error {
		cost, err := canonicalInteger(row["trust_cost"])
		if err != nil {
			return fmt.Errorf("trust_cost: %w", err)
		}
		fmt.Fprintf(pathHash, "%s\x00%s\x00%s\n", row["idx"], cost, row["path"])
		return nil
	})
	if err != nil {
		return ReplaySemanticDigests{}, err
	}
	result.PathRows = pathRows
	result.PathDigest = hex.EncodeToString(pathHash.Sum(nil))
	return result, nil
}

func hashReplayCSV(path string, required []string, visit func(map[string]string) error) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return 0, err
	}
	indices := make(map[string]int, len(header))
	for index, name := range header {
		indices[name] = index
	}
	for _, name := range required {
		if _, ok := indices[name]; !ok {
			return 0, fmt.Errorf("%s is missing column %s", path, name)
		}
	}
	var rows uint64
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		values := make(map[string]string, len(required))
		for _, name := range required {
			index := indices[name]
			if index >= len(record) {
				return 0, fmt.Errorf("%s row %d is short", path, rows)
			}
			values[name] = record[index]
		}
		if err := visit(values); err != nil {
			return 0, fmt.Errorf("%s row %d: %w", path, rows, err)
		}
		rows++
	}
	return rows, nil
}

func canonicalInteger(value string) (string, error) {
	rational, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || !rational.IsInt() {
		return "", fmt.Errorf("%q is not an integer", value)
	}
	return rational.Num().String(), nil
}

func readReplayJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}
