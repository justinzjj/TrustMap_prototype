package replay

import (
	"fmt"
	"math"
	"sync"
)

type replayNodeID int

// ReplayTrustView is the active, event-time graph used only for cost replay.
// A deterministic treap indexes observed heights without a network dependency.
type ReplayTrustView struct {
	mu              sync.RWMutex
	profile         CostProfile
	heights         map[string]*orderedHeightIndex
	adjacency       map[ReplayBlockKey]map[ReplayBlockKey]ReplayEdge
	nodes           map[ReplayBlockKey]struct{}
	nodeIDs         map[ReplayBlockKey]replayNodeID
	nodeKeys        []ReplayBlockKey
	searchPool      sync.Pool
	edgeCount       uint64
	crossEdgesAdded uint64
}

func NewReplayTrustView(profile CostProfile) (*ReplayTrustView, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	view := &ReplayTrustView{
		profile:   profile,
		heights:   make(map[string]*orderedHeightIndex),
		adjacency: make(map[ReplayBlockKey]map[ReplayBlockKey]ReplayEdge),
		nodes:     make(map[ReplayBlockKey]struct{}),
		nodeIDs:   make(map[ReplayBlockKey]replayNodeID),
	}
	view.searchPool.New = func() any { return &replaySearchScratch{} }
	return view, nil
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
	view.nodeIDs[key] = replayNodeID(len(view.nodeKeys))
	view.nodeKeys = append(view.nodeKeys, key)
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
	return view.edgeCount
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
	if _, exists := neighbours[to]; !exists {
		view.edgeCount++
	}
	neighbours[to] = edge
}

func (view *ReplayTrustView) deleteEdgeLocked(from, to ReplayBlockKey) {
	if neighbours := view.adjacency[from]; neighbours != nil {
		if _, exists := neighbours[to]; exists {
			delete(neighbours, to)
			view.edgeCount--
		}
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
