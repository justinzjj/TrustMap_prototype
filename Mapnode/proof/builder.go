package proof

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

type BuildRequest struct {
	PlanID                planner.PlanID
	SourceBlockHash       common.Hash
	ExpectedHomeTrustRoot trustview.TrustRoot
}

type Builder struct {
	repository    Repository
	pathTreeDepth uint8
}

// NewBuilder takes pathTreeDepth from deployment-validated process
// composition. It is deliberately absent from BuildRequest so an observed
// request or submitter cannot lower the verification shape.
func NewBuilder(repository Repository, pathTreeDepth uint8) *Builder {
	return &Builder{repository: repository, pathTreeDepth: pathTreeDepth}
}

func (builder *Builder) Build(ctx context.Context, request BuildRequest) (PathProof, error) {
	if builder == nil || builder.repository == nil {
		return PathProof{}, errors.New("PathProof repository is required")
	}
	plan, err := builder.repository.LoadPathPlan(ctx, request.PlanID)
	if err != nil {
		return PathProof{}, err
	}
	if plan.ID != request.PlanID || plan.ID != planner.ComputePlanID(plan) || plan.Type != planner.PathPlan || len(plan.Hops) == 0 {
		return PathProof{}, ErrProofMaterialMissing
	}
	snapshot, err := builder.repository.LoadTrustViewSnapshot(ctx, plan.SnapshotID)
	if err != nil {
		return PathProof{}, err
	}
	if snapshot.ID != plan.SnapshotID || !snapshot.Sealed || snapshot.StartNodeID != plan.HomeNodeID || snapshot.TargetNodeID != plan.TargetNodeID {
		return PathProof{}, ErrProofMaterialMissing
	}
	if snapshot.HomeTrustRoot != request.ExpectedHomeTrustRoot {
		return PathProof{}, ErrStaleTrustViewSnapshot
	}
	target, ok := findNode(snapshot, snapshot.TargetNodeID)
	if !ok {
		return PathProof{}, ErrProofMaterialMissing
	}
	if target.Key.BlockHash != request.SourceBlockHash {
		return PathProof{}, ErrSourceBlockHashMismatch
	}

	materials := make([]PathProofMaterial, len(plan.Hops))
	expectedFrom := snapshot.StartNodeID
	for index, edgeID := range plan.Hops {
		material, loadErr := builder.repository.LoadPathProofMaterial(ctx, plan.ID, snapshot.ID, index, edgeID)
		if loadErr != nil {
			if errors.Is(loadErr, ErrProofMaterialMissing) {
				return PathProof{}, fmt.Errorf("plan hop %d: %w", index, ErrProofMaterialMissing)
			}
			return PathProof{}, fmt.Errorf("load plan hop %d PathProof material: %w", index, loadErr)
		}
		if !validMaterial(plan, snapshot, index, edgeID, expectedFrom, material) {
			return PathProof{}, fmt.Errorf("plan hop %d: %w", index, ErrProofMaterialMissing)
		}
		materials[index] = material.Clone()
		expectedFrom = material.ToNode.ID
	}
	if expectedFrom != snapshot.TargetNodeID {
		return PathProof{}, ErrProofMaterialMissing
	}

	result := PathProof{
		RequestID: plan.RequestID, PlanID: plan.ID, SnapshotID: snapshot.ID,
		BaseTrustRoot: target.Root, CreatedAt: time.Now().UTC(),
	}
	for planIndex := len(materials) - 1; planIndex >= 0; planIndex-- {
		material := materials[planIndex]
		witness, witnessErr := domain.NewMembershipWitness(material.Witness.LeafIndex, material.Witness.Siblings)
		if witnessErr != nil {
			return PathProof{}, fmt.Errorf("plan hop %d: %w", planIndex, ErrProofMaterialMissing)
		}
		result.BlockHashes = append(result.BlockHashes, material.ToNode.Key.BlockHash)
		result.Witnesses = append(result.Witnesses, witness)
		result.Hops = append(result.Hops, PathProofHop{
			PlanHopIndex: planIndex, EdgeID: material.Edge.ID, ToNodeID: material.ToNode.ID,
			BlockHash: material.ToNode.Key.BlockHash, WitnessID: material.Witness.ID,
		})
	}
	if result.BlockHashes[0] != request.SourceBlockHash {
		return PathProof{}, ErrSourceBlockHashMismatch
	}
	if err := internalproof.VerifyPath(result.BaseTrustRoot.Hash, result.BlockHashes, result.Witnesses, request.ExpectedHomeTrustRoot.Hash, builder.pathTreeDepth); err != nil {
		return PathProof{}, fmt.Errorf("%w: local PathProof verification: %v", ErrProofMaterialMissing, err)
	}
	result.ID = ComputePathProofID(result)
	if err := builder.repository.SavePathProof(ctx, result); err != nil {
		return PathProof{}, err
	}
	persisted, err := builder.repository.LoadPathProof(ctx, result.ID)
	if err != nil {
		return PathProof{}, err
	}
	if persisted.ID != ComputePathProofID(persisted) || !samePathProof(result, persisted) {
		return PathProof{}, ErrPathProofIDMismatch
	}
	return persisted, nil
}

func validMaterial(plan planner.Plan, snapshot trustview.TrustViewSnapshot, index int, edgeID trustview.EdgeID, expectedFrom trustview.NodeID, material PathProofMaterial) bool {
	if material.PlanID != plan.ID || material.SnapshotID != snapshot.ID || material.PlanHopIndex != index || material.Edge.ID != edgeID ||
		material.Edge.From != expectedFrom || material.FromNode.ID != material.Edge.From || material.ToNode.ID != material.Edge.To ||
		material.Edge.EvidenceID != material.Witness.EvidenceID || material.Edge.LeafIndex != material.Witness.LeafIndex || material.Edge.WitnessID == nil ||
		*material.Edge.WitnessID != material.Witness.ID || material.Witness.ID != trustview.NewMembershipWitness(material.Witness.EvidenceID, material.Witness.LeafIndex, material.Witness.Siblings).ID {
		return false
	}
	from, fromOK := findNode(snapshot, material.FromNode.ID)
	to, toOK := findNode(snapshot, material.ToNode.ID)
	if !fromOK || !toOK || from != material.FromNode || to != material.ToNode {
		return false
	}
	for _, snapshotEdge := range snapshot.Edges {
		if snapshotEdge.ID == edgeID {
			return sameEdge(snapshotEdge, material.Edge)
		}
	}
	return false
}

func findNode(snapshot trustview.TrustViewSnapshot, id trustview.NodeID) (trustview.TrustNode, bool) {
	for _, node := range snapshot.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return trustview.TrustNode{}, false
}

func sameEdge(left, right trustview.TrustEdge) bool {
	if left.ID != right.ID || left.From != right.From || left.To != right.To || left.EvidenceID != right.EvidenceID || left.LeafIndex != right.LeafIndex || left.PathStepCost != right.PathStepCost {
		return false
	}
	return (left.WitnessID == nil && right.WitnessID == nil) || (left.WitnessID != nil && right.WitnessID != nil && *left.WitnessID == *right.WitnessID)
}

func samePathProof(left, right PathProof) bool {
	if left.ID != right.ID || left.RequestID != right.RequestID || left.PlanID != right.PlanID || left.SnapshotID != right.SnapshotID || left.BaseTrustRoot != right.BaseTrustRoot || len(left.BlockHashes) != len(right.BlockHashes) || len(left.Witnesses) != len(right.Witnesses) || len(left.Hops) != len(right.Hops) {
		return false
	}
	for index := range left.BlockHashes {
		if left.BlockHashes[index] != right.BlockHashes[index] || left.Hops[index] != right.Hops[index] || left.Witnesses[index].LeafIndex() != right.Witnesses[index].LeafIndex() {
			return false
		}
		leftSiblings, rightSiblings := left.Witnesses[index].Siblings(), right.Witnesses[index].Siblings()
		if len(leftSiblings) != len(rightSiblings) {
			return false
		}
		for siblingIndex := range leftSiblings {
			if leftSiblings[siblingIndex] != rightSiblings[siblingIndex] {
				return false
			}
		}
	}
	return true
}
