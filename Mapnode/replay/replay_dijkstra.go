package replay

import (
	"container/heap"
	"fmt"
	"math"
)

type replaySearchScratch struct {
	generation             uint64
	distances              []uint64
	distanceGenerations    []uint64
	predecessors           []replayNodeID
	predecessorGenerations []uint64
	queue                  replayPriorityQueue
}

func (scratch *replaySearchScratch) begin(size int) {
	if len(scratch.distances) < size {
		scratch.distances = append(scratch.distances, make([]uint64, size-len(scratch.distances))...)
	}
	if len(scratch.distanceGenerations) < size {
		scratch.distanceGenerations = append(scratch.distanceGenerations, make([]uint64, size-len(scratch.distanceGenerations))...)
	}
	if len(scratch.predecessors) < size {
		scratch.predecessors = append(scratch.predecessors, make([]replayNodeID, size-len(scratch.predecessors))...)
	}
	if len(scratch.predecessorGenerations) < size {
		scratch.predecessorGenerations = append(scratch.predecessorGenerations, make([]uint64, size-len(scratch.predecessorGenerations))...)
	}

	if scratch.generation == math.MaxUint64 {
		clear(scratch.distanceGenerations)
		clear(scratch.predecessorGenerations)
		scratch.generation = 1
	} else {
		scratch.generation++
		if scratch.generation == 0 {
			scratch.generation = 1
		}
	}
	scratch.queue = scratch.queue[:0]
}

func (scratch *replaySearchScratch) getDistance(id replayNodeID) (uint64, bool) {
	if id < 0 || int(id) >= len(scratch.distanceGenerations) || scratch.distanceGenerations[id] != scratch.generation {
		return 0, false
	}
	return scratch.distances[id], true
}

func (scratch *replaySearchScratch) setDistance(id replayNodeID, distance uint64) {
	scratch.distances[id] = distance
	scratch.distanceGenerations[id] = scratch.generation
}

func (scratch *replaySearchScratch) getPredecessor(id replayNodeID) (replayNodeID, bool) {
	if id < 0 || int(id) >= len(scratch.predecessorGenerations) || scratch.predecessorGenerations[id] != scratch.generation {
		return 0, false
	}
	return scratch.predecessors[id], true
}

func (scratch *replaySearchScratch) setPredecessor(id, predecessor replayNodeID) {
	scratch.predecessors[id] = predecessor
	scratch.predecessorGenerations[id] = scratch.generation
}

type replayQueueItem struct {
	id   replayNodeID
	key  ReplayBlockKey
	cost uint64
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
	lastIndex := len(old) - 1
	last := old[lastIndex]
	old[lastIndex] = replayQueueItem{}
	*queue = old[:lastIndex]
	return last
}

func (view *ReplayTrustView) ShortestPath(start, goal ReplayBlockKey, cutoff uint64, targetChain string, targetHeight uint64) (ReplayPath, bool, error) {
	view.mu.RLock()
	defer view.mu.RUnlock()

	start, goal = canonicalKey(start), canonicalKey(goal)
	if start == goal {
		return ReplayPath{Cost: 0, Nodes: []ReplayBlockKey{start}}, true, nil
	}

	scratch := view.searchPool.Get().(*replaySearchScratch)
	scratch.begin(len(view.nodeKeys))
	defer view.searchPool.Put(scratch)

	startID, err := view.nodeIDLocked(start, "replay start")
	if err != nil {
		return ReplayPath{}, false, err
	}
	goalID, err := view.nodeIDLocked(goal, "replay goal")
	if err != nil {
		return ReplayPath{}, false, err
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

	scratch.setDistance(startID, 0)
	scratch.queue = append(scratch.queue, replayQueueItem{id: startID, key: start})
	heap.Init(&scratch.queue)
	for scratch.queue.Len() > 0 {
		item := heap.Pop(&scratch.queue).(replayQueueItem)
		known, ok := scratch.getDistance(item.id)
		if !ok || known != item.cost {
			continue
		}
		if item.cost > cutoff {
			return ReplayPath{}, false, nil
		}
		if !nodeOK(item.key) {
			continue
		}
		if item.id == goalID {
			return view.reconstructPathLocked(startID, goalID, item.cost, scratch)
		}
		for neighbour, edge := range view.adjacency[item.key] {
			candidate, err := CheckedAdd(item.cost, edge.Weight)
			if err != nil {
				return ReplayPath{}, false, fmt.Errorf("dijkstra edge %v -> %v: %w", item.key, neighbour, err)
			}
			if candidate > cutoff || !nodeOK(neighbour) {
				continue
			}
			neighbourID, err := view.nodeIDLocked(neighbour, "replay adjacency neighbour")
			if err != nil {
				return ReplayPath{}, false, err
			}
			known, exists := scratch.getDistance(neighbourID)
			if exists && candidate >= known {
				continue
			}
			scratch.setDistance(neighbourID, candidate)
			scratch.setPredecessor(neighbourID, item.id)
			heap.Push(&scratch.queue, replayQueueItem{id: neighbourID, key: neighbour, cost: candidate})
		}
	}
	return ReplayPath{}, false, nil
}

func (view *ReplayTrustView) nodeIDLocked(key ReplayBlockKey, role string) (replayNodeID, error) {
	id, ok := view.nodeIDs[key]
	if !ok {
		if role == "replay adjacency neighbour" {
			return 0, fmt.Errorf("replay invariant: neighbour node ID missing for %v", key)
		}
		return 0, fmt.Errorf("replay invariant: node ID missing for %s %v", role, key)
	}
	if id < 0 || int(id) >= len(view.nodeKeys) {
		return 0, fmt.Errorf("replay invariant: node ID %d out of range for %s %v", id, role, key)
	}
	if view.nodeKeys[id] != key {
		return 0, fmt.Errorf("replay invariant: node ID %d maps to %v, want %s %v", id, view.nodeKeys[id], role, key)
	}
	return id, nil
}

func (view *ReplayTrustView) reconstructPathLocked(start, goal replayNodeID, cost uint64, scratch *replaySearchScratch) (ReplayPath, bool, error) {
	if goal < 0 || int(goal) >= len(view.nodeKeys) {
		return ReplayPath{}, false, fmt.Errorf("replay invariant: goal node ID %d out of range during path reconstruction", goal)
	}
	nodes := []ReplayBlockKey{view.nodeKeys[goal]}
	for current := goal; current != start; {
		prior, ok := scratch.getPredecessor(current)
		if !ok {
			return ReplayPath{}, false, fmt.Errorf("replay path predecessor missing for %v", view.nodeKeys[current])
		}
		if prior < 0 || int(prior) >= len(view.nodeKeys) {
			return ReplayPath{}, false, fmt.Errorf("replay invariant: predecessor node ID %d out of range for %v", prior, view.nodeKeys[current])
		}
		nodes = append(nodes, view.nodeKeys[prior])
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
