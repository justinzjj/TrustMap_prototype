package proof

import (
	"context"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
)

// Repository is owned by the PathProof consumer. The concrete SQLite store
// implements it without becoming a dependency of this package.
type Repository interface {
	LoadPathPlan(context.Context, planner.PlanID) (planner.Plan, error)
	LoadTrustViewSnapshot(context.Context, trustview.SnapshotID) (trustview.TrustViewSnapshot, error)
	LoadPathProofMaterial(context.Context, planner.PlanID, trustview.SnapshotID, int, trustview.EdgeID) (PathProofMaterial, error)
	SavePathProof(context.Context, PathProof) error
	LoadPathProof(context.Context, PathProofID) (PathProof, error)
}

// PathProofMaterial is one snapshot-bound TrustEdge plus the exact immutable
// TrustRoot membership witness needed to verify it.
type PathProofMaterial struct {
	PlanID       planner.PlanID
	SnapshotID   trustview.SnapshotID
	PlanHopIndex int
	Edge         trustview.TrustEdge
	FromNode     trustview.TrustNode
	ToNode       trustview.TrustNode
	Witness      trustview.MembershipWitness
}

func (material PathProofMaterial) Clone() PathProofMaterial {
	copy := material
	copy.Edge.WitnessID = cloneWitnessID(material.Edge.WitnessID)
	copy.Witness = material.Witness.Clone()
	return copy
}

func cloneWitnessID(id *trustview.WitnessID) *trustview.WitnessID {
	if id == nil {
		return nil
	}
	copy := *id
	return &copy
}
