package replay

import "testing"

func TestCoordinatorHandCalculatedFixtureAndNoFutureCrossEdge(t *testing.T) {
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, err := NewCheckpointPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewReplayCoordinator(SettingB2, map[string]uint64{"a": 0, "b": 0, "c": 0}, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	events := []ReplayEvent{
		{ID: "e1", Sequence: 0, Source: ReplayBlock{Chain: "a", OriginalHeight: 10}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}},
		{ID: "e2", Sequence: 1, Source: ReplayBlock{Chain: "b", OriginalHeight: 1}, Destination: ReplayBlock{Chain: "c", OriginalHeight: 1}},
		{ID: "e3", Sequence: 2, Source: ReplayBlock{Chain: "a", OriginalHeight: 20}, Destination: ReplayBlock{Chain: "c", OriginalHeight: 1}},
	}
	first, err := coordinator.Process(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != ReplayDecisionDirect || first.DirectCost != 1_000 || first.PathFound {
		t.Fatalf("first event used its future cross edge: %#v", first)
	}
	if first.BaselineBefore != 0 || first.BaselineAfter != 10 || first.CrossEdgesAdded != 1 {
		t.Fatalf("first transition state = %#v", first)
	}
	if first.GraphNodes != 2 || first.GraphEdges != 1 {
		t.Fatalf("first graph counters = %#v", first)
	}
	second, err := coordinator.Process(events[1])
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision != ReplayDecisionDirect {
		t.Fatalf("second decision = %#v", second)
	}
	third, err := coordinator.Process(events[2])
	if err != nil {
		t.Fatal(err)
	}
	if third.Decision != ReplayDecisionTrustMap || !third.PathFound || third.PathCost != 1_020 || third.ChosenCost != 1_025 || third.DirectCost != 2_000 {
		t.Fatalf("third hand-calculated transition = %#v", third)
	}
	want := []ReplayBlockKey{{Chain: "c", Height: 1}, {Chain: "b", Height: 1}, {Chain: "a", Height: 10}, {Chain: "a", Height: 20}}
	if len(third.Path.Nodes) != len(want) {
		t.Fatalf("path = %#v", third.Path)
	}
	for i := range want {
		if third.Path.Nodes[i] != want[i] {
			t.Fatalf("path[%d] = %v, want %v", i, third.Path.Nodes[i], want[i])
		}
	}
}

func TestCoordinatorSettingsAreIsolatedAndGateTrustMapAndCheckpoint(t *testing.T) {
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	policy, err := NewCheckpointPolicy(map[string]uint64{"a": 10})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := NewReplayCoordinators([]Setting{SettingB0, SettingB1, SettingB2, SettingB3}, map[string]uint64{"a": 0, "b": 0}, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	event := ReplayEvent{ID: "e", Source: ReplayBlock{Chain: "a", OriginalHeight: 19}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 1}}
	results := make(map[Setting]ReplayDecision)
	for _, setting := range []Setting{SettingB0, SettingB1, SettingB2, SettingB3} {
		result, err := runs[setting].Process(event)
		if err != nil {
			t.Fatal(err)
		}
		results[setting] = result
	}
	if results[SettingB0].DirectCost != 1_900 || results[SettingB2].DirectCost != 1_900 {
		t.Fatalf("non-checkpoint direct costs = B0:%d B2:%d", results[SettingB0].DirectCost, results[SettingB2].DirectCost)
	}
	if results[SettingB1].DirectCost != 900 || results[SettingB3].DirectCost != 900 {
		t.Fatalf("checkpoint direct costs = B1:%d B3:%d", results[SettingB1].DirectCost, results[SettingB3].DirectCost)
	}
	if results[SettingB0].PlanningEnabled || results[SettingB1].PlanningEnabled || !results[SettingB2].PlanningEnabled || !results[SettingB3].PlanningEnabled {
		t.Fatalf("planning gates = B0:%t B1:%t B2:%t B3:%t", results[SettingB0].PlanningEnabled, results[SettingB1].PlanningEnabled, results[SettingB2].PlanningEnabled, results[SettingB3].PlanningEnabled)
	}
	if got := runs[SettingB0].Baselines().Get("b", "a"); got != 19 {
		t.Fatalf("B0 baseline = %d", got)
	}
	if got := runs[SettingB1].Baselines().Get("b", "a"); got != 19 {
		t.Fatalf("B1 baseline = %d", got)
	}
	// A second event only in B0 must not change any other graph or baseline.
	if _, err := runs[SettingB0].Process(ReplayEvent{ID: "e2", Source: ReplayBlock{Chain: "a", OriginalHeight: 25}, Destination: ReplayBlock{Chain: "b", OriginalHeight: 2}}); err != nil {
		t.Fatal(err)
	}
	if got := runs[SettingB1].Baselines().Get("b", "a"); got != 19 {
		t.Fatalf("B1 baseline changed through B0: %d", got)
	}
	if runs[SettingB0].TrustView().CrossEdgesAdded() != 2 || runs[SettingB1].TrustView().CrossEdgesAdded() != 1 {
		t.Fatal("setting graph state is shared")
	}
}
