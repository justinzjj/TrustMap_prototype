package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Setting string

const (
	SettingB0 Setting = "B0"
	SettingB1 Setting = "B1"
	SettingB2 Setting = "B2"
	SettingB3 Setting = "B3"
)

func ParseSetting(value string) (Setting, error) {
	setting := Setting(strings.ToUpper(strings.TrimSpace(value)))
	switch setting {
	case SettingB0, SettingB1, SettingB2, SettingB3:
		return setting, nil
	default:
		return "", fmt.Errorf("unknown replay setting %q", value)
	}
}

func (setting Setting) UsesTrustMap() bool   { return setting == SettingB2 || setting == SettingB3 }
func (setting Setting) UsesCheckpoint() bool { return setting == SettingB1 || setting == SettingB3 }

type DurabilityConfig struct {
	Synchronous string `yaml:"synchronous"`
}

type Config struct {
	Version            uint64
	InputTrace         string
	ExpectedDigest     string
	RunRoot            string
	Settings           []Setting
	InvalidRowPolicy   InvalidRowPolicy
	CostProfile        CostProfile
	CheckpointPolicy   CheckpointPolicy
	CheckpointPolicies map[Setting]CheckpointPolicy
	ChainAllowlist     []string
	MaxEvents          uint64
	RecordEvery        uint64
	LogEvery           uint64
	Durability         DurabilityConfig
}

type rawConfig struct {
	Version                    uint64                       `yaml:"version"`
	InputTrace                 string                       `yaml:"input_trace"`
	ExpectedDigest             string                       `yaml:"expected_digest"`
	RunRoot                    string                       `yaml:"run_root"`
	Settings                   []string                     `yaml:"settings"`
	InvalidRowPolicy           InvalidRowPolicy             `yaml:"invalid_row_policy"`
	CostProfile                CostProfile                  `yaml:"cost_profile"`
	CheckpointPeriods          map[string]uint64            `yaml:"checkpoint_periods"`
	CheckpointPeriodsBySetting map[string]map[string]uint64 `yaml:"checkpoint_periods_by_setting"`
	ChainAllowlist             []string                     `yaml:"chain_allowlist"`
	MaxEvents                  *uint64                      `yaml:"max_events"`
	RecordEvery                uint64                       `yaml:"record_every"`
	LogEvery                   uint64                       `yaml:"log_every"`
	Durability                 DurabilityConfig             `yaml:"durability"`
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open replay config: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var raw rawConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode replay config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("replay config contains multiple YAML documents")
		}
		return Config{}, fmt.Errorf("decode replay config: %w", err)
	}
	return validateRawConfig(raw)
}

func validateRawConfig(raw rawConfig) (Config, error) {
	if raw.Version != 1 {
		return Config{}, fmt.Errorf("unsupported replay config version %d", raw.Version)
	}
	if !filepath.IsAbs(raw.InputTrace) {
		return Config{}, fmt.Errorf("input_trace must be absolute")
	}
	if !filepath.IsAbs(raw.RunRoot) {
		return Config{}, fmt.Errorf("run_root must be absolute")
	}
	inputInfo, err := os.Stat(raw.InputTrace)
	if err != nil || !inputInfo.Mode().IsRegular() {
		return Config{}, fmt.Errorf("input_trace must name a readable regular file")
	}
	runInfo, err := os.Stat(raw.RunRoot)
	if err != nil || !runInfo.IsDir() {
		return Config{}, fmt.Errorf("run_root must name an existing directory")
	}
	resolvedInput, err := filepath.EvalSymlinks(raw.InputTrace)
	if err != nil {
		return Config{}, fmt.Errorf("resolve input_trace: %w", err)
	}
	resolvedRunRoot, err := filepath.EvalSymlinks(raw.RunRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve run_root: %w", err)
	}
	if pathWithin(resolvedRunRoot, resolvedInput) {
		return Config{}, fmt.Errorf("input_trace must not be beneath run_root")
	}
	if err := raw.CostProfile.Validate(); err != nil {
		return Config{}, err
	}
	policy := raw.InvalidRowPolicy
	if policy == "" {
		policy = InvalidRowReject
	}
	if policy != InvalidRowReject && policy != InvalidRowDrop {
		return Config{}, fmt.Errorf("unknown invalid_row_policy %q", policy)
	}
	if len(raw.Settings) == 0 {
		return Config{}, fmt.Errorf("at least one replay setting is required")
	}
	settings := make([]Setting, 0, len(raw.Settings))
	seenSettings := make(map[Setting]struct{}, len(raw.Settings))
	for _, value := range raw.Settings {
		setting, err := ParseSetting(value)
		if err != nil {
			return Config{}, err
		}
		if _, exists := seenSettings[setting]; exists {
			return Config{}, fmt.Errorf("duplicate replay setting %s", setting)
		}
		seenSettings[setting] = struct{}{}
		settings = append(settings, setting)
	}
	resolvedSettingDirs := make(map[string]Setting, len(settings))
	for _, setting := range settings {
		settingDir := filepath.Join(raw.RunRoot, strings.ToLower(string(setting)))
		resolvedSettingDir := filepath.Join(resolvedRunRoot, strings.ToLower(string(setting)))
		if _, err := os.Lstat(settingDir); err == nil {
			resolvedSettingDir, err = filepath.EvalSymlinks(settingDir)
			if err != nil {
				return Config{}, fmt.Errorf("resolve output directory for %s: %w", setting, err)
			}
		} else if !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("inspect output directory for %s: %w", setting, err)
		}
		if !pathWithin(resolvedRunRoot, resolvedSettingDir) || filepath.Clean(resolvedSettingDir) == filepath.Clean(resolvedRunRoot) {
			return Config{}, fmt.Errorf("output directory for %s escapes run_root", setting)
		}
		key := filepath.Clean(resolvedSettingDir)
		if previous, exists := resolvedSettingDirs[key]; exists {
			return Config{}, fmt.Errorf("output directories for %s and %s alias", previous, setting)
		}
		resolvedSettingDirs[key] = setting
	}
	checkpointPolicy, err := NewCheckpointPolicy(raw.CheckpointPeriods)
	if err != nil {
		return Config{}, err
	}
	checkpointPolicies := make(map[Setting]CheckpointPolicy, len(raw.CheckpointPeriodsBySetting))
	for rawSetting, periods := range raw.CheckpointPeriodsBySetting {
		setting, parseErr := ParseSetting(rawSetting)
		if parseErr != nil {
			return Config{}, fmt.Errorf("checkpoint_periods_by_setting: %w", parseErr)
		}
		if _, exists := checkpointPolicies[setting]; exists {
			return Config{}, fmt.Errorf("duplicate checkpoint policy for setting %s", setting)
		}
		settingPolicy, policyErr := NewCheckpointPolicy(periods)
		if policyErr != nil {
			return Config{}, fmt.Errorf("checkpoint policy for %s: %w", setting, policyErr)
		}
		checkpointPolicies[setting] = settingPolicy
	}
	allowlist := make([]string, 0, len(raw.ChainAllowlist))
	seenChains := make(map[string]struct{}, len(raw.ChainAllowlist))
	for _, chain := range raw.ChainAllowlist {
		name := canonicalChain(chain)
		if name == "" {
			return Config{}, fmt.Errorf("chain_allowlist contains an empty chain")
		}
		if _, exists := seenChains[name]; exists {
			return Config{}, fmt.Errorf("chain_allowlist contains duplicate chain %q", name)
		}
		seenChains[name] = struct{}{}
		allowlist = append(allowlist, name)
	}
	sort.Strings(allowlist)
	maxEvents := uint64(0)
	if raw.MaxEvents != nil {
		if *raw.MaxEvents == 0 {
			return Config{}, fmt.Errorf("max_events must be positive when specified")
		}
		maxEvents = *raw.MaxEvents
	}
	synchronous := strings.ToLower(strings.TrimSpace(raw.Durability.Synchronous))
	if synchronous == "" {
		synchronous = "full"
	}
	if synchronous != "full" && synchronous != "normal" {
		return Config{}, fmt.Errorf("durability.synchronous must be full or normal")
	}
	expectedDigest := strings.ToLower(strings.TrimSpace(raw.ExpectedDigest))
	if expectedDigest != "" {
		data, err := os.ReadFile(raw.InputTrace)
		if err != nil {
			return Config{}, fmt.Errorf("read input trace for digest: %w", err)
		}
		digest := sha256.Sum256(data)
		actual := hex.EncodeToString(digest[:])
		if expectedDigest != actual {
			return Config{}, fmt.Errorf("input trace digest mismatch: got %s want %s", actual, expectedDigest)
		}
	}
	return Config{
		Version: raw.Version, InputTrace: filepath.Clean(raw.InputTrace), ExpectedDigest: expectedDigest,
		RunRoot: filepath.Clean(raw.RunRoot), Settings: settings, InvalidRowPolicy: policy,
		CostProfile: raw.CostProfile, CheckpointPolicy: checkpointPolicy, CheckpointPolicies: checkpointPolicies, ChainAllowlist: allowlist,
		MaxEvents: maxEvents, RecordEvery: raw.RecordEvery, LogEvery: raw.LogEvery,
		Durability: DurabilityConfig{Synchronous: synchronous},
	}, nil
}

func (config Config) CheckpointPolicyForSetting(setting Setting) CheckpointPolicy {
	if policy, ok := config.CheckpointPolicies[setting]; ok {
		return policy
	}
	return config.CheckpointPolicy
}

func (config Config) SettingRunDir(setting Setting) string {
	return filepath.Join(config.RunRoot, strings.ToLower(string(setting)))
}

func (config Config) ValidatePreparedTrace(trace PreparedTrace) error {
	if err := config.CostProfile.Validate(); err != nil {
		return err
	}
	type heightRange struct{ minimum, maximum uint64 }
	ranges := make(map[string]heightRange, len(trace.Chains))
	observe := func(block ReplayBlock) {
		current, ok := ranges[block.Chain]
		if !ok {
			ranges[block.Chain] = heightRange{minimum: block.OriginalHeight, maximum: block.OriginalHeight}
			return
		}
		if block.OriginalHeight < current.minimum {
			current.minimum = block.OriginalHeight
		}
		if block.OriginalHeight > current.maximum {
			current.maximum = block.OriginalHeight
		}
		ranges[block.Chain] = current
	}
	for _, event := range trace.Events {
		observe(event.Source)
		observe(event.Destination)
	}
	for chain, height := range trace.InitHeights {
		observe(ReplayBlock{Chain: chain, OriginalHeight: height})
	}
	for chain, heights := range ranges {
		span := heights.maximum - heights.minimum
		if _, err := CheckedCost(span, config.CostProfile.DirectStepCost); err != nil {
			return fmt.Errorf("direct cost range for chain %s: %w", chain, err)
		}
		pathSegment, err := CheckedCost(span, config.CostProfile.PathStepCost)
		if err != nil {
			return fmt.Errorf("path cost range for chain %s: %w", chain, err)
		}
		if _, err := CheckedAdd(pathSegment, config.CostProfile.TrustRootUpdateCost); err != nil {
			return fmt.Errorf("path plus update range for chain %s: %w", chain, err)
		}
	}
	if _, err := CheckedAdd(config.CostProfile.PathStepCost, config.CostProfile.TrustRootUpdateCost); err != nil {
		return fmt.Errorf("cross edge plus update cost: %w", err)
	}
	return nil
}

func pathWithin(base, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}
