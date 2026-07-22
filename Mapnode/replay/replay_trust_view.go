package replay

import (
	"container/heap"
	"fmt"
	"math"
	"sync"
)

// ReplayTrustView is the active, event-time graph used only for cost replay.
// A deterministic treap indexes observed heights without a network dependency.
type ReplayTrustView struct {
	mu              sync.RWMutex
	profile         CostProfile
	heights         map[string]*orderedHeightIndex
	adjacency       map[ReplayBlockKey]map[ReplayBlockKey]ReplayEdge
	nodes           map[ReplayBlockKey]struct{}
	crossEdgesAdded uint64
}

func NewReplayTrustView(profile CostProfile) (*ReplayTrustView, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return &ReplayTrustView{
		profile:   profile,
		heights:   make(map[string]*orderedHeightIndex),
		adjacency: make(map[ReplayBlockKey]map[ReplayBlockKey]ReplayEdge),
		nodes:     make(map[ReplayBlockKey]struct{}),
	}, nil
}

func (view *ReplayTrustView) Activate(block ReplayBlock) (ReplayBlockKey, error) {
	view.mu.Lock()
	defer view.mu.Unlock()
	return view.activateLocked(block)
}

func (view *ReplayTrustView) activateLocked(block ReplayBlock) (ReplayBlockKey, error) {
	key := block.Key()
	if key.Chain == "" {
		return ReplayBlockKey{}, fmt.Errorf("replay block chain is empty")
	}
	index := view.heights[key.Chain]
	if index == nil {
		index = &orderedHeightIndex{}
		view.heights[key.Chain] = index
	}
	if index.Contains(key.Height) {
		return key, nil
	}
	predecessor, hasPredecessor, successor, hasSuccessor := index.Neighbours(key.Height)
	type pendingEdge struct {
		from, to ReplayBlockKey
		edge     ReplayEdge
	}
	pending := make([]pendingEdge, 0, 4)
	appendPair := func(lower, higher uint64) error {
		delta := higher - lower
		up, err := CheckedCost(delta, view.profile.DirectStepCost)
		if err != nil {
			return err
		}
		down, err := CheckedCost(delta, view.profile.PathStepCost)
		if err != nil {
			return err
		}
		lowKey := ReplayBlockKey{Chain: key.Chain, Height: lower}
		highKey := ReplayBlockKey{Chain: key.Chain, Height: higher}
		pending = append(pending,
			pendingEdge{from: lowKey, to: highKey, edge: ReplayEdge{Kind: ReplayIntraChainUpEdge, Weight: up}},
			pendingEdge{from: highKey, to: lowKey, edge: ReplayEdge{Kind: ReplayIntraChainDownEdge, Weight: down}},
		)
		return nil
	}
	if hasPredecessor {
		if err := appendPair(predecessor, key.Height); err != nil {
			return ReplayBlockKey{}, fmt.Errorf("create lower replay edge: %w", err)
		}
	}
	if hasSuccessor {
		if err := appendPair(key.Height, successor); err != nil {
			return ReplayBlockKey{}, fmt.Errorf("create upper replay edge: %w", err)
		}
	}

	index.Insert(key.Height)
	view.nodes[key] = struct{}{}
	if hasPredecessor && hasSuccessor {
		view.deleteEdgeLocked(ReplayBlockKey{Chain: key.Chain, Height: predecessor}, ReplayBlockKey{Chain: key.Chain, Height: successor})
		view.deleteEdgeLocked(ReplayBlockKey{Chain: key.Chain, Height: successor}, ReplayBlockKey{Chain: key.Chain, Height: predecessor})
	}
	for _, item := range pending {
		view.setEdgeLocked(item.from, item.to, item.edge)
	}
	return key, nil
}

func (view *ReplayTrustView) AddVerifiedDependency(fromBlock, toBlock ReplayBlock) (ReplayVerifiedDependencyEdge, error) {
	view.mu.Lock()
	defer view.mu.Unlock()
	from, err := view.activateLocked(fromBlock)
	if err != nil {
		return ReplayVerifiedDependencyEdge{}, err
	}
	to, err := view.activateLocked(toBlock)
	if err != nil {
		return ReplayVerifiedDependencyEdge{}, err
	}
	if view.crossEdgesAdded == math.MaxUint64 {
		return ReplayVerifiedDependencyEdge{}, fmt.Errorf("cross edge addition counter overflow")
	}
	edge := ReplayVerifiedDependencyEdge{From: from, To: to, Weight: view.profile.PathStepCost}
	view.setEdgeLocked(from, to, ReplayEdge{Kind: ReplayVerifiedDependencyEdgeKind, Weight: edge.Weight})
	view.crossEdgesAdded++
	return edge, nil
}

func (view *ReplayTrustView) Edge(from, to ReplayBlockKey) (ReplayEdge, bool) {
	view.mu.RLock()
	defer view.mu.RUnlock()
	edge, ok := view.adjacency[canonicalKey(from)][canonicalKey(to)]
	return edge, ok
}

func (view *ReplayTrustView) NodeCount() uint64 {
	view.mu.RLock()
	defer view.mu.RUnlock()
	return uint64(len(view.nodes))
}

func (view *ReplayTrustView) EdgeCount() uint64 {
	view.mu.RLock()
	defer view.mu.RUnlock()
	var count uint64
	for _, neighbours := range view.adjacency {
		count += uint64(len(neighbours))
	}
	return count
}

func (view *ReplayTrustView) CrossEdgesAdded() uint64 {
	view.mu.RLock()
	defer view.mu.RUnlock()
	return view.crossEdgesAdded
}

func (view *ReplayTrustView) setEdge(from, to ReplayBlockKey, edge ReplayEdge) {
	view.mu.Lock()
	defer view.mu.Unlock()
	view.setEdgeLocked(canonicalKey(from), canonicalKey(to), edge)
}

func (view *ReplayTrustView) setEdgeLocked(from, to ReplayBlockKey, edge ReplayEdge) {
	neighbours := view.adjacency[from]
	if neighbours == nil {
		neighbours = make(map[ReplayBlockKey]ReplayEdge)
		view.adjacency[from] = neighbours
	}
	neighbours[to] = edge
}

func (view *ReplayTrustView) deleteEdgeLocked(from, to ReplayBlockKey) {
	if neighbours := view.adjacency[from]; neighbours != nil {
		delete(neighbours, to)
	}
}

func canonicalKey(key ReplayBlockKey) ReplayBlockKey {
	key.Chain = canonicalChain(key.Chain)
	return key
}

// orderedHeightIndex is a deterministic treap. Priorities are a stable mix of
// the height, and key ordering breaks the vanishingly rare priority collision.
type orderedHeightIndex struct{ root *heightTreapNode }

type heightTreapNode struct {
	key      uint64
	priority uint64
	left     *heightTreapNode
	right    *heightTreapNode
}

func (index *orderedHeightIndex) Contains(key uint64) bool {
	for node := index.root; node != nil; {
		switch {
		case key < node.key:
			node = node.left
		case key > node.key:
			node = node.right
		default:
			return true
		}
	}
	return false
}

func (index *orderedHeightIndex) Neighbours(key uint64) (predecessor uint64, hasPredecessor bool, successor uint64, hasSuccessor bool) {
	for node := index.root; node != nil; {
		if key < node.key {
			successor, hasSuccessor = node.key, true
			node = node.left
		} else if key > node.key {
			predecessor, hasPredecessor = node.key, true
			node = node.right
		} else {
			return predecessor, hasPredecessor, successor, hasSuccessor
		}
	}
	return predecessor, hasPredecessor, successor, hasSuccessor
}

func (index *orderedHeightIndex) Insert(key uint64) {
	index.root = insertHeightNode(index.root, &heightTreapNode{key: key, priority: mixHeight(key)})
}

func insertHeightNode(root, inserted *heightTreapNode) *heightTreapNode {
	if root == nil {
		return inserted
	}
	if inserted.key < root.key {
		root.left = insertHeightNode(root.left, inserted)
		if heightPriorityLess(root.left, root) {
			root = rotateHeightRight(root)
		}
	} else if inserted.key > root.key {
		root.right = insertHeightNode(root.right, inserted)
		if heightPriorityLess(root.right, root) {
			root = rotateHeightLeft(root)
		}
	}
	return root
}

func heightPriorityLess(left, right *heightTreapNode) bool {
	return left.priority < right.priority || (left.priority == right.priority && left.key < right.key)
}

func rotateHeightRight(root *heightTreapNode) *heightTreapNode {
	next := root.left
	root.left = next.right
	next.right = root
	return next
}

func rotateHeightLeft(root *heightTreapNode) *heightTreapNode {
	next := root.right
	root.right = next.left
	next.left = root
	return next
}

func mixHeight(value uint64) uint64 {
	value += 0x9e3779b97f4a7c15
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

type replayQueueItem struct {
	cost uint64
	key  ReplayBlockKey
}

type replayPriorityQueue []replayQueueItem

func (queue replayPriorityQueue) Len() int { return len(queue) }
func (queue replayPriorityQueue) Less(i, j int) bool {
	if queue[i].cost != queue[j].cost {
		return queue[i].cost < queue[j].cost
	}
	if queue[i].key.Chain != queue[j].key.Chain {
		return queue[i].key.Chain < queue[j].key.Chain
	}
	return queue[i].key.Height < queue[j].key.Height
}
func (queue replayPriorityQueue) Swap(i, j int) { queue[i], queue[j] = queue[j], queue[i] }
func (queue *replayPriorityQueue) Push(value any) {
	*queue = append(*queue, value.(replayQueueItem))
}
func (queue *replayPriorityQueue) Pop() any {
	old := *queue
	last := old[len(old)-1]
	*queue = old[:len(old)-1]
	return last
}

func (view *ReplayTrustView) ShortestPath(start, goal ReplayBlockKey, cutoff uint64, targetChain string, targetHeight uint64) (ReplayPath, bool, error) {
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
	queue := replayPriorityQueue{{key: start}}
	heap.Init(&queue)
	for queue.Len() > 0 {
		item := heap.Pop(&queue).(replayQueueItem)
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
			return view.reconstructPathLocked(start, goal, item.cost, previous)
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
			heap.Push(&queue, replayQueueItem{cost: candidate, key: neighbour})
		}
	}
	return ReplayPath{}, false, nil
}

func (view *ReplayTrustView) reconstructPathLocked(start, goal ReplayBlockKey, cost uint64, previous map[ReplayBlockKey]ReplayBlockKey) (ReplayPath, bool, error) {
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
		segments = append(segments, ReplayPathSegment{From: nodes[index], To: nodes[index+1], Kind: edge.Kind, Weight: edge.Weight})
	}
	return ReplayPath{Cost: cost, Nodes: nodes, Segments: segments}, true, nil
}
