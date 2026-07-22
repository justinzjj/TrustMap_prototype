package replay

import (
	"math"
	"testing"
)

func TestReplayTrustViewAssignsStableNodeIDsToCanonicalKeys(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}

	key, err := view.Activate(ReplayBlock{Chain: " C ", OriginalHeight: 7})
	if err != nil {
		t.Fatal(err)
	}
	id := view.nodeIDs[key]
	if id != replayNodeID(0) {
		t.Fatalf("first node ID = %d, want 0", id)
	}

	duplicate, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: 7})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate != key {
		t.Fatalf("duplicate key = %v, want %v", duplicate, key)
	}
	if got := view.nodeIDs[duplicate]; got != id {
		t.Fatalf("duplicate node ID = %d, want original ID %d", got, id)
	}
	if len(view.nodeIDs) != 1 || len(view.nodeKeys) != 1 {
		t.Fatalf("duplicate activation grew IDs: map=%d keys=%d", len(view.nodeIDs), len(view.nodeKeys))
	}
}

func TestReplayTrustViewAssignsMonotonicContiguousNodeIDs(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []ReplayBlock{
		{Chain: "c", OriginalHeight: 100},
		{Chain: "a", OriginalHeight: 5},
		{Chain: "b", OriginalHeight: 9},
	}
	for want, block := range blocks {
		key, activateErr := view.Activate(block)
		if activateErr != nil {
			t.Fatal(activateErr)
		}
		if got := view.nodeIDs[key]; got != replayNodeID(want) {
			t.Fatalf("node %v ID = %d, want %d", key, got, want)
		}
		if got := view.nodeKeys[want]; got != key {
			t.Fatalf("node key at ID %d = %v, want %v", want, got, key)
		}
	}
}

func TestReplayTrustViewMiddleSplitPreservesExistingNodeIDs(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	low, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: 100})
	if err != nil {
		t.Fatal(err)
	}
	high, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: 200})
	if err != nil {
		t.Fatal(err)
	}
	lowID, highID := view.nodeIDs[low], view.nodeIDs[high]

	middle, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: 150})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.nodeIDs[low]; got != lowID {
		t.Fatalf("low node ID changed from %d to %d", lowID, got)
	}
	if got := view.nodeIDs[high]; got != highID {
		t.Fatalf("high node ID changed from %d to %d", highID, got)
	}
	if got := view.nodeIDs[middle]; got != replayNodeID(2) {
		t.Fatalf("middle node ID = %d, want 2", got)
	}
}

func TestReplayTrustViewFailedActivationDoesNotLeakNodeIDOrGraphState(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "overflow", DirectStepCost: math.MaxUint64, PathStepCost: 1, TrustRootUpdateCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	firstID := view.nodeIDs[first]

	failed := ReplayBlockKey{Chain: "c", Height: 3}
	if _, err := view.Activate(ReplayBlock{Chain: " C ", OriginalHeight: failed.Height}); err == nil {
		t.Fatal("overflowing second activation succeeded")
	}
	if len(view.nodes) != 1 || len(view.nodeIDs) != 1 || len(view.nodeKeys) != 1 {
		t.Fatalf("failed activation leaked node state: nodes=%d IDs=%d keys=%d", len(view.nodes), len(view.nodeIDs), len(view.nodeKeys))
	}
	if got := view.nodeIDs[first]; got != firstID || view.nodeKeys[firstID] != first {
		t.Fatalf("existing node mapping changed: ID=%d key=%v", got, view.nodeKeys[firstID])
	}
	if _, ok := view.nodes[failed]; ok {
		t.Fatalf("failed node %v leaked into nodes", failed)
	}
	if _, ok := view.nodeIDs[failed]; ok {
		t.Fatalf("failed node %v leaked an ID", failed)
	}
	if view.heights["c"].Contains(failed.Height) {
		t.Fatalf("failed height %d leaked into index", failed.Height)
	}
	if view.edgeCount != 0 || len(view.adjacency) != 0 {
		t.Fatalf("failed activation leaked edges: cached=%d adjacency=%d", view.edgeCount, len(view.adjacency))
	}
}

func TestReplayTrustViewProductionGraphHasCompleteCanonicalNodeIDMappings(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, height := range []uint64{100, 200, 150} {
		if _, err := view.Activate(ReplayBlock{Chain: " C ", OriginalHeight: height}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := view.AddVerifiedDependency(
		ReplayBlock{Chain: " B ", OriginalHeight: 1},
		ReplayBlock{Chain: " A ", OriginalHeight: 10},
	); err != nil {
		t.Fatal(err)
	}

	if len(view.nodes) != len(view.nodeIDs) || len(view.nodeIDs) != len(view.nodeKeys) {
		t.Fatalf("node mapping sizes differ: nodes=%d IDs=%d keys=%d", len(view.nodes), len(view.nodeIDs), len(view.nodeKeys))
	}
	for key := range view.nodes {
		assertReplayNodeIDMapping(t, view, key)
	}
	for from, neighbours := range view.adjacency {
		assertReplayNodeIDMapping(t, view, from)
		for to := range neighbours {
			assertReplayNodeIDMapping(t, view, to)
		}
	}
}

func assertReplayNodeIDMapping(t *testing.T, view *ReplayTrustView, key ReplayBlockKey) {
	t.Helper()
	if key != canonicalKey(key) {
		t.Fatalf("non-canonical graph key %v", key)
	}
	id, ok := view.nodeIDs[key]
	if !ok {
		t.Fatalf("graph key %v has no node ID", key)
	}
	if id < 0 || int(id) >= len(view.nodeKeys) {
		t.Fatalf("graph key %v has out-of-range node ID %d", key, id)
	}
	if got := view.nodeKeys[id]; got != key {
		t.Fatalf("node key at ID %d = %v, want %v", id, got, key)
	}
}

func TestReplayTrustViewSplitsIntraChainNeighboursWithAsymmetricCosts(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, height := range []uint64{100, 200, 150} {
		if _, err := view.Activate(ReplayBlock{Chain: " C ", OriginalHeight: height}); err != nil {
			t.Fatal(err)
		}
	}

	assertReplayEdge(t, view, ReplayBlockKey{Chain: "c", Height: 100}, ReplayBlockKey{Chain: "c", Height: 150}, ReplayIntraChainUpEdge, 5_000)
	assertReplayEdge(t, view, ReplayBlockKey{Chain: "c", Height: 150}, ReplayBlockKey{Chain: "c", Height: 100}, ReplayIntraChainDownEdge, 500)
	assertReplayEdge(t, view, ReplayBlockKey{Chain: "c", Height: 150}, ReplayBlockKey{Chain: "c", Height: 200}, ReplayIntraChainUpEdge, 5_000)
	assertReplayEdge(t, view, ReplayBlockKey{Chain: "c", Height: 200}, ReplayBlockKey{Chain: "c", Height: 150}, ReplayIntraChainDownEdge, 500)
	if _, ok := view.Edge(ReplayBlockKey{Chain: "c", Height: 100}, ReplayBlockKey{Chain: "c", Height: 200}); ok {
		t.Fatal("obsolete predecessor/successor edge survived middle insertion")
	}
	if got := view.NodeCount(); got != 3 {
		t.Fatalf("node count = %d, want 3", got)
	}
}

func TestReplayTrustViewSupportsOrdinarySameChainDownwardRelation(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, height := range []uint64{201, 200} {
		if _, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: height}); err != nil {
			t.Fatal(err)
		}
	}
	path, found, err := view.ShortestPath(
		ReplayBlockKey{Chain: "c", Height: 201},
		ReplayBlockKey{Chain: "c", Height: 200},
		10,
		"c",
		200,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || path.Cost != 10 || len(path.Nodes) != 2 {
		t.Fatalf("downward path = %#v, found=%t", path, found)
	}
}

func TestReplayTrustViewCrossEdgeOverwritesAdjacencyButCountsEveryRow(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	from := ReplayBlock{Chain: "b", OriginalHeight: 1}
	to := ReplayBlock{Chain: "a", OriginalHeight: 10}
	for range 2 {
		if _, err := view.AddVerifiedDependency(from, to); err != nil {
			t.Fatal(err)
		}
	}
	if got := view.CrossEdgesAdded(); got != 2 {
		t.Fatalf("cross additions = %d, want 2", got)
	}
	edge, ok := view.Edge(from.Key(), to.Key())
	if !ok || edge.Kind != ReplayVerifiedDependencyEdgeKind || edge.Weight != 10 {
		t.Fatalf("dependency edge = %#v, found=%t", edge, ok)
	}
}

func TestReplayTrustViewMaintainsCachedEdgeCountAcrossSplitsAndOverwrites(t *testing.T) {
	view, err := NewReplayTrustView(CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, height := range []uint64{100, 200} {
		if _, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: height}); err != nil {
			t.Fatal(err)
		}
	}
	assertCachedReplayEdgeCountInvariant(t, view, 2)

	if _, err := view.Activate(ReplayBlock{Chain: "c", OriginalHeight: 150}); err != nil {
		t.Fatal(err)
	}
	assertCachedReplayEdgeCountInvariant(t, view, 4)

	from := ReplayBlock{Chain: "b", OriginalHeight: 1}
	to := ReplayBlock{Chain: "a", OriginalHeight: 10}
	if _, err := view.AddVerifiedDependency(from, to); err != nil {
		t.Fatal(err)
	}
	assertCachedReplayEdgeCountInvariant(t, view, 5)
	if got := view.CrossEdgesAdded(); got != 1 {
		t.Fatalf("cross additions = %d, want 1", got)
	}

	if _, err := view.AddVerifiedDependency(from, to); err != nil {
		t.Fatal(err)
	}
	assertCachedReplayEdgeCountInvariant(t, view, 5)
	if got := view.CrossEdgesAdded(); got != 2 {
		t.Fatalf("cross additions = %d, want 2", got)
	}
}

func TestReplayTrustViewShortestPathUsesDeterministicTieBreakAndStrictRelaxation(t *testing.T) {
	profile := CostProfile{ID: "fixture", DirectStepCost: 100, PathStepCost: 10, TrustRootUpdateCost: 5}
	view, err := NewReplayTrustView(profile)
	if err != nil {
		t.Fatal(err)
	}
	start := ReplayBlock{Chain: "z", OriginalHeight: 1}
	left := ReplayBlock{Chain: "a", OriginalHeight: 1}
	right := ReplayBlock{Chain: "b", OriginalHeight: 1}
	goal := ReplayBlock{Chain: "g", OriginalHeight: 1}
	for _, block := range []ReplayBlock{start, right, left, goal} {
		if _, err := view.Activate(block); err != nil {
			t.Fatal(err)
		}
	}
	view.setEdge(start.Key(), right.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(start.Key(), left.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(right.Key(), goal.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(left.Key(), goal.Key(), ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})

	path, found, err := view.ShortestPath(start.Key(), goal.Key(), 20, goal.Chain, goal.OriginalHeight)
	if err != nil {
		t.Fatal(err)
	}
	if !found || path.Cost != 20 || len(path.Nodes) != 3 || path.Nodes[1] != left.Key() {
		t.Fatalf("tie-broken path = %#v, found=%t", path, found)
	}
}

func assertReplayEdge(t *testing.T, view *ReplayTrustView, from, to ReplayBlockKey, kind ReplayEdgeKind, weight uint64) {
	t.Helper()
	edge, ok := view.Edge(from, to)
	if !ok || edge.Kind != kind || edge.Weight != weight {
		t.Fatalf("edge %v -> %v = %#v, found=%t; want %s/%d", from, to, edge, ok, kind, weight)
	}
}

func assertCachedReplayEdgeCountInvariant(t *testing.T, view *ReplayTrustView, want uint64) {
	t.Helper()
	var actual uint64
	for _, neighbours := range view.adjacency {
		actual += uint64(len(neighbours))
	}
	if view.edgeCount != actual {
		t.Fatalf("cached edge count = %d, adjacency contains %d edges", view.edgeCount, actual)
	}
	if got := view.EdgeCount(); got != want {
		t.Fatalf("edge count = %d, want %d", got, want)
	}
}
