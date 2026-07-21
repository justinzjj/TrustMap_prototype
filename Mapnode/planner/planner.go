// Package planner selects a deterministic bounded path over one immutable
// TrustViewSnapshot, or records an explicit DirectPlan fallback.
package planner

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrPlanNotFound             = errors.New("plan not found")
	ErrSnapshotUnsealed         = errors.New("TrustViewSnapshot is not sealed")
	ErrSnapshotEndpoint         = errors.New("TrustViewSnapshot endpoint is invalid")
	ErrPathCostOverflow         = errors.New("TrustView path cost overflows uint64")
	ErrStaleSnapshot            = errors.New("TrustViewSnapshot is stale")
	ErrInconsistentPathStepCost = errors.New("TrustViewSnapshot path step cost is not a single trusted non-zero value")
	ErrPlanIDMismatch           = errors.New("PlanID does not match persisted plan content")
)

type PlanID [32]byte
type PlanType string
type FallbackReason string

const (
	PathPlan   PlanType = "path"
	DirectPlan PlanType = "direct"

	UncalibratedProfile   FallbackReason = "uncalibrated_profile"
	NoPath                FallbackReason = "no_path"
	PathCostExceedsDirect FallbackReason = "path_cost_exceeds_direct"
	ProofMaterialMissing  FallbackReason = "proof_material_missing"
)

type Request struct {
	ID          domain.RequestID
	Attempt     uint64
	SnapshotID  trustview.SnapshotID
	HomeChainID domain.ChainID
}

type Plan struct {
	ID                 PlanID
	RequestID          domain.RequestID
	Attempt            uint64
	SnapshotID         trustview.SnapshotID
	ProfileID          string
	ProfileFingerprint common.Hash
	Type               PlanType
	HomeNodeID         trustview.NodeID
	TargetNodeID       trustview.NodeID
	Hops               []trustview.EdgeID
	PathStepCost       uint64
	PathCost           *uint64
	DirectCost         *uint64
	FallbackReason     FallbackReason
	CreatedAt          time.Time
}

func (plan Plan) Clone() Plan {
	copy := plan
	copy.Hops = append([]trustview.EdgeID(nil), plan.Hops...)
	copy.PathCost = cloneUint64(plan.PathCost)
	copy.DirectCost = cloneUint64(plan.DirectCost)
	return copy
}

type SnapshotRepository interface {
	LoadTrustViewSnapshot(context.Context, trustview.SnapshotID) (trustview.TrustViewSnapshot, error)
}

// PlanRepository is owned by the planner consumer. Mapnode/store implements
// it without introducing a store dependency into this package.
type PlanRepository interface {
	SavePlan(context.Context, Plan) error
	LoadPlan(context.Context, PlanID) (Plan, error)
}

type DirectProfileSource interface {
	DirectProfile(domain.ChainID) (registry.ValidatedDirectProfile, bool)
}

type Planner struct {
	snapshots SnapshotRepository
	plans     PlanRepository
	profiles  DirectProfileSource
}

func New(snapshots SnapshotRepository, plans PlanRepository, profiles DirectProfileSource) *Planner {
	return &Planner{snapshots: snapshots, plans: plans, profiles: profiles}
}

func (planner *Planner) Plan(ctx context.Context, request Request) (Plan, error) {
	if planner == nil || planner.snapshots == nil || planner.plans == nil || planner.profiles == nil {
		return Plan{}, errors.New("planner dependencies are required")
	}
	snapshot, err := planner.snapshots.LoadTrustViewSnapshot(ctx, request.SnapshotID)
	if err != nil {
		return Plan{}, err
	}
	if !snapshot.Sealed {
		return Plan{}, ErrSnapshotUnsealed
	}
	if snapshot.ID != request.SnapshotID || snapshot.HomeChainID != request.HomeChainID {
		return Plan{}, ErrSnapshotEndpoint
	}
	expectedSnapshotID := trustview.ComputeSnapshotID(request.ID, request.Attempt, snapshot.Revision, snapshot.StartNodeID, snapshot.TargetNodeID, snapshot.HomeTrustRoot)
	if expectedSnapshotID != snapshot.ID {
		return Plan{}, ErrSnapshotEndpoint
	}
	if snapshot.StartNodeID == snapshot.TargetNodeID {
		return Plan{}, ErrSnapshotEndpoint
	}
	if !snapshotHasNode(snapshot, snapshot.StartNodeID) || !snapshotHasNode(snapshot, snapshot.TargetNodeID) {
		return Plan{}, ErrSnapshotEndpoint
	}
	validatedView, err := trustview.NewTrustView(snapshot.Revision, snapshot.Nodes, snapshot.Edges)
	if err != nil || len(validatedView.Nodes) != len(snapshot.Nodes) || len(validatedView.Edges) != len(snapshot.Edges) {
		return Plan{}, fmtErrorSnapshot(err)
	}
	startNode, _ := findSnapshotNode(snapshot, snapshot.StartNodeID)
	if startNode.Key.ChainID != request.HomeChainID {
		return Plan{}, ErrSnapshotEndpoint
	}
	if startNode.Root != snapshot.HomeTrustRoot {
		return Plan{}, ErrStaleSnapshot
	}

	profile, found := planner.profiles.DirectProfile(request.HomeChainID)
	if !found {
		profile.ID = "unavailable"
	}
	plan := Plan{
		RequestID: request.ID, Attempt: request.Attempt, SnapshotID: snapshot.ID,
		ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
		HomeNodeID: snapshot.StartNodeID, TargetNodeID: snapshot.TargetNodeID,
		CreatedAt: time.Now().UTC(),
	}
	if !profile.Calibrated {
		plan.Type, plan.FallbackReason = DirectPlan, UncalibratedProfile
		return planner.persist(ctx, plan)
	}
	plan.DirectCost = uint64Pointer(profile.DirectCost)

	pathStepCost, err := trustedPathStepCost(snapshot.Edges)
	if err != nil {
		return Plan{}, err
	}
	edges, reachable := directedReachability(snapshot)
	if !reachable {
		plan.Type, plan.FallbackReason = DirectPlan, NoPath
		return planner.persist(ctx, plan)
	}
	path, _, err := shortestPath(snapshot, edges)
	if err != nil {
		return Plan{}, err
	}
	if uint64(len(path)) > math.MaxUint64/pathStepCost {
		return Plan{}, ErrPathCostOverflow
	}
	cost := uint64(len(path)) * pathStepCost
	plan.Hops = make([]trustview.EdgeID, len(path))
	for index, edge := range path {
		plan.Hops[index] = edge.ID
	}
	plan.PathStepCost = pathStepCost
	plan.PathCost = uint64Pointer(cost)
	if cost > profile.DirectCost {
		plan.Type, plan.FallbackReason = DirectPlan, PathCostExceedsDirect
		return planner.persist(ctx, plan)
	}
	for _, edge := range path {
		if edge.WitnessID == nil {
			plan.Type, plan.FallbackReason = DirectPlan, ProofMaterialMissing
			return planner.persist(ctx, plan)
		}
	}
	plan.Type = PathPlan
	return planner.persist(ctx, plan)
}

func (planner *Planner) persist(ctx context.Context, plan Plan) (Plan, error) {
	plan.ID = ComputePlanID(plan)
	if err := planner.plans.SavePlan(ctx, plan); err != nil {
		return Plan{}, err
	}
	return planner.plans.LoadPlan(ctx, plan.ID)
}

// ComputePlanID is a domain-separated, unambiguous content address for every
// persisted planning decision. CreatedAt is deliberately excluded.
func ComputePlanID(plan Plan) PlanID {
	encoded := make([]byte, 0, 320+len(plan.ProfileID)+len(plan.FallbackReason)+32*len(plan.Hops))
	encoded = appendLengthPrefixed(encoded, []byte("TrustMap/Planner/PlanID/v1"))
	encoded = append(encoded, plan.RequestID[:]...)
	encoded = appendUint64Word(encoded, plan.Attempt)
	encoded = append(encoded, plan.SnapshotID[:]...)
	encoded = appendLengthPrefixed(encoded, []byte(plan.ProfileID))
	encoded = append(encoded, plan.ProfileFingerprint[:]...)
	encoded = appendLengthPrefixed(encoded, []byte(plan.Type))
	encoded = append(encoded, plan.HomeNodeID[:]...)
	encoded = append(encoded, plan.TargetNodeID[:]...)
	encoded = appendUint64Word(encoded, uint64(len(plan.Hops)))
	for _, edgeID := range plan.Hops {
		encoded = append(encoded, edgeID[:]...)
	}
	encoded = appendUint64Word(encoded, plan.PathStepCost)
	for _, value := range []*uint64{plan.PathCost, plan.DirectCost} {
		word := [32]byte{}
		if value != nil {
			word[0] = 1
			binary.BigEndian.PutUint64(word[24:], *value)
		}
		encoded = append(encoded, word[:]...)
	}
	encoded = appendLengthPrefixed(encoded, []byte(plan.FallbackReason))
	return PlanID(crypto.Keccak256Hash(encoded))
}

func appendLengthPrefixed(destination, value []byte) []byte {
	length := [4]byte{}
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	destination = append(destination, length[:]...)
	return append(destination, value...)
}
func appendUint64Word(destination []byte, value uint64) []byte {
	word := [32]byte{}
	binary.BigEndian.PutUint64(word[24:], value)
	return append(destination, word[:]...)
}

func snapshotHasNode(snapshot trustview.TrustViewSnapshot, id trustview.NodeID) bool {
	_, ok := findSnapshotNode(snapshot, id)
	return ok
}

func findSnapshotNode(snapshot trustview.TrustViewSnapshot, id trustview.NodeID) (trustview.TrustNode, bool) {
	for _, node := range snapshot.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return trustview.TrustNode{}, false
}

func directedReachability(snapshot trustview.TrustViewSnapshot) (map[trustview.NodeID][]trustview.TrustEdge, bool) {
	edges := make(map[trustview.NodeID][]trustview.TrustEdge)
	for _, edge := range snapshot.Edges {
		edges[edge.From] = append(edges[edge.From], edge)
	}
	for from := range edges {
		sort.Slice(edges[from], func(i, j int) bool { return bytes.Compare(edges[from][i].ID[:], edges[from][j].ID[:]) < 0 })
	}
	seen := map[trustview.NodeID]bool{snapshot.StartNodeID: true}
	queue := []trustview.NodeID{snapshot.StartNodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == snapshot.TargetNodeID {
			return edges, true
		}
		for _, edge := range edges[current] {
			if !seen[edge.To] {
				seen[edge.To] = true
				queue = append(queue, edge.To)
			}
		}
	}
	return edges, false
}

type candidate struct {
	node  trustview.NodeID
	cost  uint64
	path  []trustview.TrustEdge
	index int
}
type candidates []*candidate

func (items candidates) Len() int           { return len(items) }
func (items candidates) Less(i, j int) bool { return compareCandidate(items[i], items[j]) < 0 }
func (items candidates) Swap(i, j int) {
	items[i], items[j] = items[j], items[i]
	items[i].index = i
	items[j].index = j
}
func (items *candidates) Push(value any) {
	item := value.(*candidate)
	item.index = len(*items)
	*items = append(*items, item)
}
func (items *candidates) Pop() any {
	old := *items
	item := old[len(old)-1]
	*items = old[:len(old)-1]
	return item
}

func shortestPath(snapshot trustview.TrustViewSnapshot, adjacency map[trustview.NodeID][]trustview.TrustEdge) ([]trustview.TrustEdge, uint64, error) {
	start := &candidate{node: snapshot.StartNodeID}
	queue := candidates{start}
	heap.Init(&queue)
	best := map[trustview.NodeID]*candidate{start.node: start}
	overflowed := false
	for queue.Len() > 0 {
		current := heap.Pop(&queue).(*candidate)
		if known := best[current.node]; compareCandidate(current, known) != 0 {
			continue
		}
		if current.node == snapshot.TargetNodeID {
			return append([]trustview.TrustEdge(nil), current.path...), current.cost, nil
		}
		for _, edge := range adjacency[current.node] {
			if current.cost > math.MaxUint64-edge.PathStepCost {
				overflowed = true
				continue
			}
			next := &candidate{node: edge.To, cost: current.cost + edge.PathStepCost, path: append(append([]trustview.TrustEdge(nil), current.path...), edge)}
			known, exists := best[next.node]
			if !exists || compareCandidate(next, known) < 0 {
				best[next.node] = next
				heap.Push(&queue, next)
			}
		}
	}
	if overflowed {
		return nil, 0, ErrPathCostOverflow
	}
	return nil, 0, errors.New("directed path disappeared after reachability succeeded")
}

func compareCandidate(left, right *candidate) int {
	if left.cost < right.cost {
		return -1
	}
	if left.cost > right.cost {
		return 1
	}
	if len(left.path) < len(right.path) {
		return -1
	}
	if len(left.path) > len(right.path) {
		return 1
	}
	for index := range left.path {
		if compared := bytes.Compare(left.path[index].ID[:], right.path[index].ID[:]); compared != 0 {
			return compared
		}
	}
	return bytes.Compare(left.node[:], right.node[:])
}

func trustedPathStepCost(edges []trustview.TrustEdge) (uint64, error) {
	if len(edges) == 0 {
		return 0, nil
	}
	cost := edges[0].PathStepCost
	if cost == 0 {
		return 0, ErrInconsistentPathStepCost
	}
	for _, edge := range edges[1:] {
		if edge.PathStepCost != cost {
			return 0, ErrInconsistentPathStepCost
		}
	}
	return cost, nil
}

func fmtErrorSnapshot(cause error) error {
	if cause == nil {
		return errors.New("TrustViewSnapshot contains duplicate nodes or edges")
	}
	return cause
}

func uint64Pointer(value uint64) *uint64 { return &value }
func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	return uint64Pointer(*value)
}
