package replay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointPolicyUsesOriginalHeightAndReportsEffectiveChains(t *testing.T) {
	fixture := filepath.Join("..", "..", "tests", "fixtures", "replay", "checkpoint-periods-small.json")
	policy, err := LoadCheckpointPolicyJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if policy.ConfiguredChainCount() != 3 || policy.EffectiveWorkloadChainCount([]string{"alpha", "gamma", "beta"}) != 2 {
		t.Fatalf("checkpoint counts = configured %d effective %d", policy.ConfiguredChainCount(), policy.EffectiveWorkloadChainCount([]string{"alpha", "gamma", "beta"}))
	}
	// Original height 105 floors to 80. Normalized height 5 would floor to 0,
	// which is exactly the compatibility bug this API must prevent.
	if height, ok := policy.CheckpointHeight(" ALPHA ", 105); !ok || height != 80 {
		t.Fatalf("checkpoint = %d/%t, want 80/true", height, ok)
	}
	if _, ok := policy.CheckpointHeight("gamma", 105); ok {
		t.Fatal("unsupported chain returned a checkpoint")
	}
}

func TestCheckpointPolicyRejectsZeroPeriodAndDuplicateCanonicalChain(t *testing.T) {
	if _, err := NewCheckpointPolicy(map[string]uint64{"alpha": 0}); err == nil {
		t.Fatal("zero period accepted")
	}
	if _, err := NewCheckpointPolicy(map[string]uint64{"alpha": 40, " ALPHA ": 50}); err == nil {
		t.Fatal("duplicate canonical chain accepted")
	}
	directory := t.TempDir()
	duplicate := filepath.Join(directory, "duplicate.json")
	if err := os.WriteFile(duplicate, []byte(`{"alpha":40,"alpha":50}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpointPolicyJSON(duplicate); err == nil {
		t.Fatal("duplicate JSON checkpoint key accepted")
	}
}
