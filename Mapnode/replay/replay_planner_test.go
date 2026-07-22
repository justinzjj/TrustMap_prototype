package replay

import (
	"math"
	"testing"
)

func TestBaselineDirectEstimateUsesUpDownAndOriginalHeightCheckpoint(t *testing.T) {
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, err := NewCheckpointPolicy(map[string]uint64{"a": 40})
	if err != nil {
		t.Fatal(err)
	}
	baselines := NewReplayBaselines(map[string]uint64{"a": 5})

	plain, err := baselines.EstimateDirect("b", "a", 105, profile, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if plain.BaselineBefore != 5 || plain.DirectStart != 5 || plain.Cost != 10_000 || plain.CheckpointApplied {
		t.Fatalf("plain direct estimate = %#v", plain)
	}
	checkpointed, err := baselines.EstimateDirect("b", "a", 105, profile, policy, true)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointed.DirectStart != 80 || checkpointed.Cost != 2_500 || !checkpointed.CheckpointApplied {
		t.Fatalf("checkpoint direct estimate = %#v", checkpointed)
	}
	if !checkpointed.CheckpointConfigured || checkpointed.CheckpointPeriod != 40 || checkpointed.CheckpointHeight != 80 || checkpointed.DirectBlocks != 25 {
		t.Fatalf("checkpoint compatibility detail = %#v", checkpointed)
	}
	baselines.Advance("b", "a", 120)
	configuredButUnused, err := baselines.EstimateDirect("b", "a", 125, profile, policy, true)
	if err != nil {
		t.Fatal(err)
	}
	if !configuredButUnused.CheckpointConfigured || configuredButUnused.CheckpointApplied || configuredButUnused.CheckpointPeriod != 40 || configuredButUnused.CheckpointHeight != 120 || configuredButUnused.DirectBlocks != 5 {
		t.Fatalf("configured-but-unused checkpoint detail = %#v", configuredButUnused)
	}
	down, err := baselines.EstimateDirect("b", "a", 105, profile, policy, true)
	if err != nil {
		t.Fatal(err)
	}
	if down.DirectStart != 120 || down.DirectBlocks != 15 || down.Cost != 150 || down.CheckpointConfigured || down.CheckpointApplied {
		t.Fatalf("downward direct estimate = %#v", down)
	}
	if after := baselines.Advance("b", "a", 100); after != 120 {
		t.Fatalf("baseline regressed to %d", after)
	}
}

func TestReplayPlannerUsesStrictDecisionAndCheckedCutoff(t *testing.T) {
	planner := ReplayPlanner{Profile: CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}}
	if choice, err := planner.choose(1_000, ReplayPath{Cost: 995}, true); err != nil || choice.UseTrustMap {
		t.Fatalf("equal total selected TrustMap: %#v, %v", choice, err)
	}
	if choice, err := planner.choose(1_000, ReplayPath{Cost: 994}, true); err != nil || !choice.UseTrustMap || choice.TotalCost != 999 {
		t.Fatalf("strictly cheaper path not selected: %#v, %v", choice, err)
	}
	if _, err := planner.choose(math.MaxUint64, ReplayPath{Cost: math.MaxUint64}, true); err == nil {
		t.Fatal("overflowing TrustMap total accepted")
	}
}

func TestReplayPlannerPreservesLegacyTargetChainPruning(t *testing.T) {
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	view, err := NewReplayTrustView(profile)
	if err != nil {
		t.Fatal(err)
	}
	start := ReplayBlock{Chain: "b", OriginalHeight: 1}
	lowTarget := ReplayBlock{Chain: "a", OriginalHeight: 1}
	detour := ReplayBlock{Chain: "c", OriginalHeight: 1}
	goal := ReplayBlock{Chain: "a", OriginalHeight: 10}
	for _, block := range []ReplayBlock{start, lowTarget, detour, goal} {
		if _, err := view.Activate(block); err != nil {
			t.Fatal(err)
		}
	}
	view.setEdge(start.Key(), lowTarget.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 1})
	view.setEdge(lowTarget.Key(), detour.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 1})
	view.setEdge(detour.Key(), goal.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 1})

	// Without pruning b:1 -> a:1 -> c:1 -> a:10 costs only 3. cutoff 500
	// gives min target height 5, so the a:1 detour entry is nevertheless
	// pruned, exactly preserving the legacy target-chain rule.
	path, found, err := view.ShortestPath(start.Key(), goal.Key(), 500, "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("legacy-pruned path unexpectedly found: %#v", path)
	}
}
