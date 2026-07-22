package replay

import "testing"

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
