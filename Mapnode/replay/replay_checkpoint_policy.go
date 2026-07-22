package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type CheckpointPolicy struct {
	periods map[string]uint64
}

func NewCheckpointPolicy(periods map[string]uint64) (CheckpointPolicy, error) {
	canonical := make(map[string]uint64, len(periods))
	for chain, period := range periods {
		name := canonicalChain(chain)
		if name == "" {
			return CheckpointPolicy{}, fmt.Errorf("checkpoint chain is empty")
		}
		if period == 0 {
			return CheckpointPolicy{}, fmt.Errorf("checkpoint period for %s must be positive", name)
		}
		if _, exists := canonical[name]; exists {
			return CheckpointPolicy{}, fmt.Errorf("duplicate checkpoint chain %q", name)
		}
		canonical[name] = period
	}
	return CheckpointPolicy{periods: canonical}, nil
}

func LoadCheckpointPolicyJSON(path string) (CheckpointPolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckpointPolicy{}, fmt.Errorf("read checkpoint policy: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return CheckpointPolicy{}, fmt.Errorf("decode checkpoint policy: expected JSON object")
	}
	raw := make(map[string]uint64)
	canonicalKeys := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return CheckpointPolicy{}, fmt.Errorf("decode checkpoint policy: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return CheckpointPolicy{}, fmt.Errorf("decode checkpoint policy: non-string key")
		}
		canonical := canonicalChain(key)
		if _, exists := canonicalKeys[canonical]; exists {
			return CheckpointPolicy{}, fmt.Errorf("duplicate checkpoint chain %q", canonical)
		}
		canonicalKeys[canonical] = struct{}{}
		var period uint64
		if err := decoder.Decode(&period); err != nil {
			return CheckpointPolicy{}, fmt.Errorf("decode checkpoint period for %s: %w", key, err)
		}
		raw[key] = period
	}
	if _, err := decoder.Token(); err != nil {
		return CheckpointPolicy{}, fmt.Errorf("decode checkpoint policy: %w", err)
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return CheckpointPolicy{}, fmt.Errorf("decode checkpoint policy: trailing data")
	}
	return NewCheckpointPolicy(raw)
}

func (policy CheckpointPolicy) CheckpointHeight(chain string, originalHeight uint64) (uint64, bool) {
	period, ok := policy.periods[canonicalChain(chain)]
	if !ok {
		return 0, false
	}
	return (originalHeight / period) * period, true
}

func (policy CheckpointPolicy) Period(chain string) (uint64, bool) {
	period, ok := policy.periods[canonicalChain(chain)]
	return period, ok
}

func (policy CheckpointPolicy) ConfiguredChainCount() int { return len(policy.periods) }

func (policy CheckpointPolicy) EffectiveWorkloadChainCount(chains []string) int {
	seen := make(map[string]struct{}, len(chains))
	for _, chain := range chains {
		name := canonicalChain(chain)
		if _, ok := policy.periods[name]; ok {
			seen[name] = struct{}{}
		}
	}
	return len(seen)
}

func (policy CheckpointPolicy) Periods() map[string]uint64 {
	copyOfPeriods := make(map[string]uint64, len(policy.periods))
	for chain, period := range policy.periods {
		copyOfPeriods[chain] = period
	}
	return copyOfPeriods
}

func (policy CheckpointPolicy) Fingerprint() string {
	keys := make([]string, 0, len(policy.periods))
	for chain := range policy.periods {
		keys = append(keys, chain)
	}
	sort.Strings(keys)
	var material strings.Builder
	material.WriteString("trustmap/replay-checkpoints/v1\n")
	for _, chain := range keys {
		fmt.Fprintf(&material, "%s=%d\n", chain, policy.periods[chain])
	}
	digest := sha256.Sum256([]byte(material.String()))
	return hex.EncodeToString(digest[:])
}

func canonicalChain(chain string) string {
	return strings.ToLower(strings.TrimSpace(chain))
}
