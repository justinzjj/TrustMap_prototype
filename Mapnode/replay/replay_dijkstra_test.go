package replay

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

type referenceReplayQueueItem struct {
	cost uint64
	key  ReplayBlockKey
}

type referenceReplayPriorityQueue []referenceReplayQueueItem

func (queue referenceReplayPriorityQueue) Len() int { return len(queue) }

func (queue referenceReplayPriorityQueue) Less(i, j int) bool {
	if queue[i].cost != queue[j].cost {
		return queue[i].cost < queue[j].cost
	}
	if queue[i].key.Chain != queue[j].key.Chain {
		return queue[i].key.Chain < queue[j].key.Chain
	}
	return queue[i].key.Height < queue[j].key.Height
}

func (queue referenceReplayPriorityQueue) Swap(i, j int) {
	queue[i], queue[j] = queue[j], queue[i]
}

func (queue *referenceReplayPriorityQueue) Push(value any) {
	*queue = append(*queue, value.(referenceReplayQueueItem))
}

func (queue *referenceReplayPriorityQueue) Pop() any {
	old := *queue
	last := old[len(old)-1]
	*queue = old[:len(old)-1]
	return last
}

// referenceReplayShortestPath freezes the map-based Dijkstra semantics used by
// ReplayTrustView. It intentionally does not call ReplayTrustView.ShortestPath.
func referenceReplayShortestPath(view *ReplayTrustView, start, goal ReplayBlockKey, cutoff uint64, targetChain string, targetHeight uint64) (ReplayPath, bool, error) {
	view.mu.RLock()
	defer view.mu.RUnlock()

	start, goal = canonicalKey(start), canonicalKey(goal)
	if start == goal {
		return ReplayPath{Cost: 0, Nodes: []ReplayBlockKey{start}}, true, nil
	}

	minTargetHeight := uint64(0)
	steps := cutoff / view.profile.DirectStepCost
	if targetHeight > steps {
		minTargetHeight = targetHeight - steps
	}
	targetChain = canonicalChain(targetChain)
	nodeOK := func(key ReplayBlockKey) bool {
		return key.Chain != targetChain || key.Height >= minTargetHeight
	}

	distances := map[ReplayBlockKey]uint64{start: 0}
	previous := make(map[ReplayBlockKey]ReplayBlockKey)
	queue := referenceReplayPriorityQueue{{key: start}}
	heap.Init(&queue)
	for queue.Len() > 0 {
		item := heap.Pop(&queue).(referenceReplayQueueItem)
		known, ok := distances[item.key]
		if !ok || known != item.cost {
			continue
		}
		if item.cost > cutoff {
			return ReplayPath{}, false, nil
		}
		if !nodeOK(item.key) {
			continue
		}
		if item.key == goal {
			return referenceReplayReconstructPath(view, start, goal, item.cost, previous)
		}
		for neighbour, edge := range view.adjacency[item.key] {
			candidate, err := CheckedAdd(item.cost, edge.Weight)
			if err != nil {
				return ReplayPath{}, false, fmt.Errorf("dijkstra edge %v -> %v: %w", item.key, neighbour, err)
			}
			if candidate > cutoff || !nodeOK(neighbour) {
				continue
			}
			known, exists := distances[neighbour]
			if exists && candidate >= known {
				continue
			}
			distances[neighbour] = candidate
			previous[neighbour] = item.key
			heap.Push(&queue, referenceReplayQueueItem{cost: candidate, key: neighbour})
		}
	}
	return ReplayPath{}, false, nil
}

func referenceReplayReconstructPath(view *ReplayTrustView, start, goal ReplayBlockKey, cost uint64, previous map[ReplayBlockKey]ReplayBlockKey) (ReplayPath, bool, error) {
	nodes := []ReplayBlockKey{goal}
	for current := goal; current != start; {
		prior, ok := previous[current]
		if !ok {
			return ReplayPath{}, false, fmt.Errorf("replay path predecessor missing for %v", current)
		}
		nodes = append(nodes, prior)
		current = prior
	}
	for left, right := 0, len(nodes)-1; left < right; left, right = left+1, right-1 {
		nodes[left], nodes[right] = nodes[right], nodes[left]
	}

	segments := make([]ReplayPathSegment, 0, len(nodes)-1)
	for index := 0; index+1 < len(nodes); index++ {
		edge, ok := view.adjacency[nodes[index]][nodes[index+1]]
		if !ok {
			return ReplayPath{}, false, fmt.Errorf("replay path edge disappeared")
		}
		segments = append(segments, ReplayPathSegment{
			From:   nodes[index],
			To:     nodes[index+1],
			Kind:   edge.Kind,
			Weight: edge.Weight,
		})
	}
	return ReplayPath{Cost: cost, Nodes: nodes, Segments: segments}, true, nil
}

func TestReplayDijkstraReferenceCurrentBehavior(t *testing.T) {
	view := newReplayDijkstraReferenceView(t)
	start := ReplayBlockKey{Chain: "z", Height: 1}
	left := ReplayBlockKey{Chain: "a", Height: 1}
	right := ReplayBlockKey{Chain: "b", Height: 1}
	goal := ReplayBlockKey{Chain: "g", Height: 1}
	for _, key := range []ReplayBlockKey{start, right, left, goal} {
		activateReplayDijkstraKey(t, view, key)
	}
	view.setEdge(start, right, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(start, left, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(right, goal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(left, goal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})

	path, found, err := assertReplayDijkstraReferenceMatches(t, view, start, goal, 20, goal.Chain, goal.Height)
	if err != nil || !found {
		t.Fatalf("tie fixture error = %v, found = %t", err, found)
	}
	if len(path.Nodes) != 3 || path.Nodes[1] != left {
		t.Fatalf("tie fixture path nodes = %#v, want path through %v", path.Nodes, left)
	}
}

func TestReplayDijkstraReferenceHeightTieBreak(t *testing.T) {
	view := newReplayDijkstraReferenceView(t)
	start := ReplayBlockKey{Chain: "s", Height: 1}
	low := ReplayBlockKey{Chain: "x", Height: 1}
	high := ReplayBlockKey{Chain: "x", Height: 2}
	goal := ReplayBlockKey{Chain: "g", Height: 1}

	// Build adjacency directly so no intra-chain x:1 <-> x:2 edge can create a
	// shorter route than the two deliberately equal-cost alternatives.
	view.setEdge(start, low, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(start, high, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(low, goal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})
	view.setEdge(high, goal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 10})

	path, found, err := assertReplayDijkstraReferenceMatches(t, view, start, goal, 20, goal.Chain, goal.Height)
	if err != nil || !found {
		t.Fatalf("height tie fixture error = %v, found = %t", err, found)
	}
	if len(path.Nodes) != 3 || path.Nodes[1] != low {
		t.Fatalf("height tie path nodes = %#v, want path through %v", path.Nodes, low)
	}
}

func TestReplayDijkstraReferenceMixedFixtures(t *testing.T) {
	view := newReplayDijkstraReferenceView(t)
	a1 := ReplayBlockKey{Chain: "a", Height: 1}
	a3 := ReplayBlockKey{Chain: "a", Height: 3}
	b1 := ReplayBlockKey{Chain: "b", Height: 1}
	b3 := ReplayBlockKey{Chain: "b", Height: 3}
	tieStart := ReplayBlockKey{Chain: "z", Height: 1}
	tieLeft := ReplayBlockKey{Chain: "c", Height: 1}
	tieRight := ReplayBlockKey{Chain: "d", Height: 1}
	tieGoal := ReplayBlockKey{Chain: "g", Height: 1}
	pruneStart := ReplayBlockKey{Chain: "p", Height: 1}
	pruneLow := ReplayBlockKey{Chain: "target", Height: 5}
	pruneGoal := ReplayBlockKey{Chain: "target", Height: 10}
	overflowStart := ReplayBlockKey{Chain: "overflow-start", Height: 1}
	overflowMiddle := ReplayBlockKey{Chain: "overflow-middle", Height: 1}
	overflowGoal := ReplayBlockKey{Chain: "overflow-goal", Height: 1}

	for _, key := range []ReplayBlockKey{
		a1, a3, b1, b3,
		tieStart, tieRight, tieLeft, tieGoal,
		pruneStart, pruneLow, pruneGoal,
		overflowStart, overflowMiddle, overflowGoal,
	} {
		activateReplayDijkstraKey(t, view, key)
	}

	view.setEdge(a3, b1, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 3})
	view.setEdge(tieStart, tieRight, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 4})
	view.setEdge(tieStart, tieLeft, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 4})
	view.setEdge(tieRight, tieGoal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 5})
	view.setEdge(tieLeft, tieGoal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 5})
	view.setEdge(pruneStart, pruneLow, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 1})
	view.setEdge(pruneLow, pruneGoal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 1})
	view.setEdge(overflowStart, overflowMiddle, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: math.MaxUint64})
	view.setEdge(overflowMiddle, overflowGoal, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 1})

	tests := []struct {
		name         string
		start        ReplayBlockKey
		goal         ReplayBlockKey
		cutoff       uint64
		targetChain  string
		targetHeight uint64
		wantPath     ReplayPath
		wantFound    bool
		wantError    bool
	}{
		{
			name:         "intra-chain up at exact cutoff",
			start:        a1,
			goal:         a3,
			cutoff:       20,
			targetChain:  " A ",
			targetHeight: 3,
			wantFound:    true,
			wantPath: ReplayPath{
				Cost:  20,
				Nodes: []ReplayBlockKey{a1, a3},
				Segments: []ReplayPathSegment{{
					From: a1, To: a3, Kind: ReplayIntraChainUpEdge, Weight: 20,
				}},
			},
		},
		{
			name:         "intra-chain down",
			start:        a3,
			goal:         a1,
			cutoff:       6,
			targetChain:  "a",
			targetHeight: 1,
			wantFound:    true,
			wantPath: ReplayPath{
				Cost:  6,
				Nodes: []ReplayBlockKey{a3, a1},
				Segments: []ReplayPathSegment{{
					From: a3, To: a1, Kind: ReplayIntraChainDownEdge, Weight: 6,
				}},
			},
		},
		{
			name:         "cross dependency",
			start:        a3,
			goal:         b1,
			cutoff:       3,
			targetChain:  "b",
			targetHeight: 1,
			wantFound:    true,
			wantPath: ReplayPath{
				Cost:  3,
				Nodes: []ReplayBlockKey{a3, b1},
				Segments: []ReplayPathSegment{{
					From: a3, To: b1, Kind: ReplayVerifiedDependencyEdgeKind, Weight: 3,
				}},
			},
		},
		{
			name:         "equal path tie uses queue key ordering and strict relaxation",
			start:        tieStart,
			goal:         tieGoal,
			cutoff:       9,
			targetChain:  tieGoal.Chain,
			targetHeight: tieGoal.Height,
			wantFound:    true,
			wantPath: ReplayPath{
				Cost:  9,
				Nodes: []ReplayBlockKey{tieStart, tieLeft, tieGoal},
				Segments: []ReplayPathSegment{
					{From: tieStart, To: tieLeft, Kind: ReplayVerifiedDependencyEdgeKind, Weight: 4},
					{From: tieLeft, To: tieGoal, Kind: ReplayVerifiedDependencyEdgeKind, Weight: 5},
				},
			},
		},
		{
			name:         "unreachable",
			start:        b3,
			goal:         tieStart,
			cutoff:       100,
			targetChain:  tieStart.Chain,
			targetHeight: tieStart.Height,
		},
		{
			name:         "path cost over cutoff",
			start:        a1,
			goal:         a3,
			cutoff:       19,
			targetChain:  "unrelated",
			targetHeight: math.MaxUint64,
		},
		{
			name:         "target minimum height prunes low target node",
			start:        pruneStart,
			goal:         pruneGoal,
			cutoff:       20,
			targetChain:  " TARGET ",
			targetHeight: 10,
		},
		{
			name:         "canonical start equals goal before pruning",
			start:        ReplayBlockKey{Chain: " A ", Height: 1},
			goal:         a1,
			cutoff:       0,
			targetChain:  a1.Chain,
			targetHeight: math.MaxUint64,
			wantFound:    true,
			wantPath:     ReplayPath{Cost: 0, Nodes: []ReplayBlockKey{a1}},
		},
		{
			name:         "checked add overflow",
			start:        overflowStart,
			goal:         overflowGoal,
			cutoff:       math.MaxUint64,
			targetChain:  overflowGoal.Chain,
			targetHeight: overflowGoal.Height,
			wantError:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, found, err := assertReplayDijkstraReferenceMatches(
				t,
				view,
				test.start,
				test.goal,
				test.cutoff,
				test.targetChain,
				test.targetHeight,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("error presence = %v, want %v; error = %v", err != nil, test.wantError, err)
			}
			if found != test.wantFound {
				t.Fatalf("found = %t, want %t", found, test.wantFound)
			}
			assertReplayDijkstraPathEqual(t, path, test.wantPath)
		})
	}
}

func TestReplayDijkstraReferenceGeneratedMixedGraphs(t *testing.T) {
	random := rand.New(rand.NewSource(0x5eed_d1a5))
	for graphIndex := range 24 {
		view := newReplayDijkstraReferenceView(t)
		chains := []string{"generated-a", "generated-b", "generated-c"}
		nodesByChain := make([][]ReplayBlockKey, len(chains))
		allNodes := make([]ReplayBlockKey, 0, len(chains)*4)
		for chainIndex, chain := range chains {
			height := uint64(1 + random.Intn(3))
			for range 4 {
				key := ReplayBlockKey{Chain: chain, Height: height}
				nodesByChain[chainIndex] = append(nodesByChain[chainIndex], key)
				allNodes = append(allNodes, key)
				height += uint64(1 + random.Intn(4))
			}
		}
		for _, index := range random.Perm(len(allNodes)) {
			activateReplayDijkstraKey(t, view, allNodes[index])
		}

		firstCrossFrom := nodesByChain[0][random.Intn(len(nodesByChain[0]))]
		firstCrossTo := nodesByChain[1][random.Intn(len(nodesByChain[1]))]
		secondCrossFrom := nodesByChain[1][random.Intn(len(nodesByChain[1]))]
		secondCrossTo := nodesByChain[2][random.Intn(len(nodesByChain[2]))]
		view.setEdge(firstCrossFrom, firstCrossTo, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 3})
		view.setEdge(secondCrossFrom, secondCrossTo, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: 3})

		queries := []struct {
			start, goal ReplayBlockKey
			cutoff      uint64
		}{
			{start: nodesByChain[0][0], goal: nodesByChain[0][3], cutoff: (nodesByChain[0][3].Height - nodesByChain[0][0].Height) * 10},
			{start: nodesByChain[0][3], goal: nodesByChain[0][0], cutoff: (nodesByChain[0][3].Height - nodesByChain[0][0].Height) * 3},
			{start: firstCrossFrom, goal: firstCrossTo, cutoff: 3},
			{start: secondCrossFrom, goal: secondCrossTo, cutoff: 3},
			{start: nodesByChain[0][0], goal: nodesByChain[2][3], cutoff: 500},
			{start: nodesByChain[2][3], goal: nodesByChain[0][0], cutoff: 500},
			{start: nodesByChain[1][1], goal: nodesByChain[1][1], cutoff: 0},
		}
		for range 9 {
			queries = append(queries, struct {
				start, goal ReplayBlockKey
				cutoff      uint64
			}{
				start:  allNodes[random.Intn(len(allNodes))],
				goal:   allNodes[random.Intn(len(allNodes))],
				cutoff: uint64(random.Intn(151)),
			})
		}

		for queryIndex, query := range queries {
			t.Run(fmt.Sprintf("graph_%02d/query_%02d", graphIndex, queryIndex), func(t *testing.T) {
				targetChain := query.goal.Chain
				if queryIndex%3 == 0 {
					targetChain = " " + targetChain + " "
				}
				assertReplayDijkstraReferenceMatches(t, view, query.start, query.goal, query.cutoff, targetChain, query.goal.Height)
			})
		}
	}
}

func newReplayDijkstraReferenceView(t *testing.T) *ReplayTrustView {
	t.Helper()
	view, err := NewReplayTrustView(CostProfile{
		ID:                  "dijkstra-reference",
		DirectStepCost:      10,
		PathStepCost:        3,
		TrustRootUpdateCost: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func activateReplayDijkstraKey(t *testing.T, view *ReplayTrustView, key ReplayBlockKey) {
	t.Helper()
	if _, err := view.Activate(ReplayBlock{Chain: key.Chain, OriginalHeight: key.Height}); err != nil {
		t.Fatal(err)
	}
}

func assertReplayDijkstraReferenceMatches(t *testing.T, view *ReplayTrustView, start, goal ReplayBlockKey, cutoff uint64, targetChain string, targetHeight uint64) (ReplayPath, bool, error) {
	t.Helper()
	wantPath, wantFound, wantErr := referenceReplayShortestPath(view, start, goal, cutoff, targetChain, targetHeight)
	gotPath, gotFound, gotErr := view.ShortestPath(start, goal, cutoff, targetChain, targetHeight)

	if (gotErr != nil) != (wantErr != nil) {
		t.Fatalf("error presence = %v, want %v; got error %v, reference error %v", gotErr != nil, wantErr != nil, gotErr, wantErr)
	}
	if gotFound != wantFound {
		t.Fatalf("found = %t, want %t", gotFound, wantFound)
	}
	if gotPath.Cost != wantPath.Cost {
		t.Errorf("path cost = %d, want %d", gotPath.Cost, wantPath.Cost)
	}
	if len(gotPath.Nodes) != len(wantPath.Nodes) {
		t.Fatalf("node count = %d, want %d; got %#v, reference %#v", len(gotPath.Nodes), len(wantPath.Nodes), gotPath.Nodes, wantPath.Nodes)
	}
	for index := range wantPath.Nodes {
		if gotPath.Nodes[index] != wantPath.Nodes[index] {
			t.Errorf("node[%d] = %v, want %v", index, gotPath.Nodes[index], wantPath.Nodes[index])
		}
	}
	if len(gotPath.Segments) != len(wantPath.Segments) {
		t.Fatalf("segment count = %d, want %d; got %#v, reference %#v", len(gotPath.Segments), len(wantPath.Segments), gotPath.Segments, wantPath.Segments)
	}
	for index := range wantPath.Segments {
		got, want := gotPath.Segments[index], wantPath.Segments[index]
		if got.From != want.From {
			t.Errorf("segment[%d].From = %v, want %v", index, got.From, want.From)
		}
		if got.To != want.To {
			t.Errorf("segment[%d].To = %v, want %v", index, got.To, want.To)
		}
		if got.Kind != want.Kind {
			t.Errorf("segment[%d].Kind = %q, want %q", index, got.Kind, want.Kind)
		}
		if got.Weight != want.Weight {
			t.Errorf("segment[%d].Weight = %d, want %d", index, got.Weight, want.Weight)
		}
	}
	return gotPath, gotFound, gotErr
}

func assertReplayDijkstraPathEqual(t *testing.T, gotPath, wantPath ReplayPath) {
	t.Helper()
	if gotPath.Cost != wantPath.Cost {
		t.Errorf("path cost = %d, want %d", gotPath.Cost, wantPath.Cost)
	}
	if len(gotPath.Nodes) != len(wantPath.Nodes) {
		t.Fatalf("node count = %d, want %d; got %#v, want %#v", len(gotPath.Nodes), len(wantPath.Nodes), gotPath.Nodes, wantPath.Nodes)
	}
	for index := range wantPath.Nodes {
		if gotPath.Nodes[index] != wantPath.Nodes[index] {
			t.Errorf("node[%d] = %v, want %v", index, gotPath.Nodes[index], wantPath.Nodes[index])
		}
	}
	if len(gotPath.Segments) != len(wantPath.Segments) {
		t.Fatalf("segment count = %d, want %d; got %#v, want %#v", len(gotPath.Segments), len(wantPath.Segments), gotPath.Segments, wantPath.Segments)
	}
	for index := range wantPath.Segments {
		got, want := gotPath.Segments[index], wantPath.Segments[index]
		if got.From != want.From || got.To != want.To || got.Kind != want.Kind || got.Weight != want.Weight {
			t.Errorf("segment[%d] = %#v, want %#v", index, got, want)
		}
	}
}
