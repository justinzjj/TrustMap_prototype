package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

// PathProofRepository implements the PathProof Builder's consumer-owned
// repository against the same immutable plan and TrustViewSnapshot rows.
type PathProofRepository struct{ db *DB }

func NewPathProofRepository(db *DB) *PathProofRepository { return &PathProofRepository{db: db} }

func (repository *PathProofRepository) LoadPathPlan(ctx context.Context, id planner.PlanID) (planner.Plan, error) {
	if repository == nil || repository.db == nil {
		return planner.Plan{}, errors.New("nil PathProof repository database")
	}
	return NewPlanRepository(repository.db).LoadPlan(ctx, id)
}

func (repository *PathProofRepository) LoadTrustViewSnapshot(ctx context.Context, id trustview.SnapshotID) (trustview.TrustViewSnapshot, error) {
	if repository == nil || repository.db == nil {
		return trustview.TrustViewSnapshot{}, errors.New("nil PathProof repository database")
	}
	return NewTrustViewRepository(repository.db).LoadTrustViewSnapshot(ctx, id)
}

func (repository *PathProofRepository) LoadPathProofMaterial(ctx context.Context, planID planner.PlanID, snapshotID trustview.SnapshotID, planHopIndex int, edgeID trustview.EdgeID) (pathproof.PathProofMaterial, error) {
	if repository == nil || repository.db == nil {
		return pathproof.PathProofMaterial{}, errors.New("nil PathProof repository database")
	}
	return loadPathProofMaterial(ctx, repository.db.sql, planID, snapshotID, planHopIndex, edgeID)
}

func loadPathProofMaterial(ctx context.Context, query queryer, planID planner.PlanID, snapshotID trustview.SnapshotID, planHopIndex int, edgeID trustview.EdgeID) (pathproof.PathProofMaterial, error) {
	if planHopIndex < 0 {
		return pathproof.PathProofMaterial{}, pathproof.ErrProofMaterialMissing
	}
	var fromID, toID, evidenceID, witnessID []byte
	var leafIndex, pathStepCost int64
	err := query.QueryRowContext(ctx, `SELECT se.from_node_id,se.to_node_id,se.evidence_id,se.witness_id,se.dependency_leaf_index,se.path_step_cost
		FROM plan_hops ph
		JOIN snapshot_edges se ON se.snapshot_id=ph.snapshot_id AND se.edge_id=ph.edge_id
		WHERE ph.plan_id=? AND ph.snapshot_id=? AND ph.hop_index=? AND ph.edge_id=?`,
		planID[:], snapshotID[:], planHopIndex, edgeID[:]).Scan(&fromID, &toID, &evidenceID, &witnessID, &leafIndex, &pathStepCost)
	if errors.Is(err, sql.ErrNoRows) {
		return pathproof.PathProofMaterial{}, pathproof.ErrProofMaterialMissing
	}
	if err != nil {
		return pathproof.PathProofMaterial{}, fmt.Errorf("load PathProof hop binding: %w", err)
	}
	material := pathproof.PathProofMaterial{PlanID: planID, SnapshotID: snapshotID, PlanHopIndex: planHopIndex}
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{material.Edge.ID[:], edgeID[:], "PathProof edge ID"}, {material.Edge.From[:], fromID, "PathProof from node"}, {material.Edge.To[:], toID, "PathProof to node"}, {material.Edge.EvidenceID[:], evidenceID, "PathProof evidence ID"}, {material.Witness.ID[:], witnessID, "PathProof witness ID"}, {material.Witness.EvidenceID[:], evidenceID, "PathProof witness evidence ID"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return pathproof.PathProofMaterial{}, err
		}
	}
	material.Edge.LeafIndex, material.Edge.PathStepCost = uint32(leafIndex), uint64(pathStepCost)
	witness := trustview.WitnessID(material.Witness.ID)
	material.Edge.WitnessID = &witness
	material.Witness.LeafIndex = uint32(leafIndex)
	material.Witness.Siblings, err = loadWitnessSiblings(ctx, query, material.Witness.ID)
	if err != nil {
		return pathproof.PathProofMaterial{}, fmt.Errorf("load PathProof witness siblings: %w", err)
	}
	if len(material.Witness.Siblings) == 0 {
		return pathproof.PathProofMaterial{}, pathproof.ErrProofMaterialMissing
	}
	material.FromNode, err = loadSnapshotNode(ctx, query, snapshotID, material.Edge.From)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pathproof.PathProofMaterial{}, pathproof.ErrProofMaterialMissing
		}
		return pathproof.PathProofMaterial{}, fmt.Errorf("load PathProof from node: %w", err)
	}
	material.ToNode, err = loadSnapshotNode(ctx, query, snapshotID, material.Edge.To)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pathproof.PathProofMaterial{}, pathproof.ErrProofMaterialMissing
		}
		return pathproof.PathProofMaterial{}, fmt.Errorf("load PathProof to node: %w", err)
	}
	wantWitness := trustview.NewMembershipWitness(material.Witness.EvidenceID, material.Witness.LeafIndex, material.Witness.Siblings)
	if wantWitness.ID != material.Witness.ID {
		return pathproof.PathProofMaterial{}, pathproof.ErrProofMaterialMissing
	}
	return material, nil
}

func loadSnapshotNode(ctx context.Context, query queryer, snapshotID trustview.SnapshotID, nodeID trustview.NodeID) (trustview.TrustNode, error) {
	var rawID, chainID, height, blockHash, trustRoot []byte
	err := query.QueryRowContext(ctx, `SELECT node_id,chain_id,block_height,block_hash,trust_root
		FROM snapshot_nodes WHERE snapshot_id=? AND node_id=?`, snapshotID[:], nodeID[:]).Scan(&rawID, &chainID, &height, &blockHash, &trustRoot)
	if err != nil {
		return trustview.TrustNode{}, err
	}
	var node trustview.TrustNode
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{node.ID[:], rawID, "snapshot node ID"}, {node.Key.ChainID[:], chainID, "snapshot node chain ID"}, {node.Key.Height[:], height, "snapshot node height"}, {node.Key.BlockHash[:], blockHash, "snapshot node block hash"}, {node.Root.Hash[:], trustRoot, "snapshot node TrustRoot"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return trustview.TrustNode{}, err
		}
	}
	return node, nil
}

func (repository *PathProofRepository) SavePathProof(ctx context.Context, value pathproof.PathProof) error {
	if repository == nil || repository.db == nil {
		return errors.New("nil PathProof repository database")
	}
	if value.ID == (pathproof.PathProofID{}) || value.ID != pathproof.ComputePathProofID(value) {
		return pathproof.ErrPathProofIDMismatch
	}
	if len(value.Hops) == 0 || len(value.Hops) != len(value.BlockHashes) || len(value.Hops) != len(value.Witnesses) {
		return pathproof.ErrProofMaterialMissing
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin PathProof save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := loadPlan(ctx, tx, value.PlanID)
	if err != nil {
		if errors.Is(err, planner.ErrPlanNotFound) {
			return pathproof.ErrProofMaterialMissing
		}
		return fmt.Errorf("load PathProof plan: %w", err)
	}
	if plan.Type != planner.PathPlan || plan.RequestID != value.RequestID || plan.SnapshotID != value.SnapshotID || len(plan.Hops) != len(value.Hops) {
		return pathproof.ErrProofMaterialMissing
	}
	target, err := loadSnapshotNode(ctx, tx, value.SnapshotID, plan.TargetNodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pathproof.ErrProofMaterialMissing
		}
		return fmt.Errorf("load PathProof target node: %w", err)
	}
	if target.Root != value.BaseTrustRoot {
		return pathproof.ErrProofMaterialMissing
	}
	for index, hop := range value.Hops {
		wantPlanIndex := len(value.Hops) - 1 - index
		if hop.PlanHopIndex != wantPlanIndex || hop.EdgeID != plan.Hops[wantPlanIndex] || hop.BlockHash != value.BlockHashes[index] || hop.ToNodeID == (trustview.NodeID{}) {
			return pathproof.ErrProofMaterialMissing
		}
		material, loadErr := loadPathProofMaterial(ctx, tx, value.PlanID, value.SnapshotID, wantPlanIndex, hop.EdgeID)
		if loadErr != nil {
			if errors.Is(loadErr, pathproof.ErrProofMaterialMissing) {
				return pathproof.ErrProofMaterialMissing
			}
			return fmt.Errorf("load PathProof material %d: %w", index, loadErr)
		}
		if material.ToNode.ID != hop.ToNodeID || material.ToNode.Key.BlockHash != hop.BlockHash || material.Witness.ID != hop.WitnessID || material.Witness.LeafIndex != value.Witnesses[index].LeafIndex() || !sameHashes(material.Witness.Siblings, value.Witnesses[index].Siblings()) {
			return pathproof.ErrProofMaterialMissing
		}
	}
	if existing, loadErr := loadPathProof(ctx, tx, value.ID); loadErr == nil {
		if existing.ID != value.ID || pathproof.ComputePathProofID(existing) != value.ID {
			return pathproof.ErrPathProofIDMismatch
		}
		return tx.Commit()
	} else if !errors.Is(loadErr, pathproof.ErrPathProofNotFound) {
		return loadErr
	}
	createdAt := value.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO proofs(proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at)
		VALUES(?,?,?,?,?,?)`, value.ID[:], value.RequestID[:], value.PlanID[:], value.SnapshotID[:], value.BaseTrustRoot.Hash[:], toUnix(createdAt)); err != nil {
		return fmt.Errorf("insert PathProof: %w", err)
	}
	for index, hop := range value.Hops {
		if _, err := tx.ExecContext(ctx, `INSERT INTO proof_hops(
			proof_id,plan_id,snapshot_id,hop_index,plan_hop_index,edge_id,to_node_id,block_hash,witness_id
		) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID[:], value.PlanID[:], value.SnapshotID[:], index, hop.PlanHopIndex, hop.EdgeID[:], hop.ToNodeID[:], hop.BlockHash[:], hop.WitnessID[:]); err != nil {
			return fmt.Errorf("insert PathProof hop %d: %w", index, err)
		}
	}
	return tx.Commit()
}

func (repository *PathProofRepository) LoadPathProof(ctx context.Context, id pathproof.PathProofID) (pathproof.PathProof, error) {
	if repository == nil || repository.db == nil {
		return pathproof.PathProof{}, errors.New("nil PathProof repository database")
	}
	return loadPathProof(ctx, repository.db.sql, id)
}

func (repository *PathProofRepository) LoadPathProofForPlan(ctx context.Context, planID planner.PlanID) (pathproof.PathProof, error) {
	if repository == nil || repository.db == nil {
		return pathproof.PathProof{}, errors.New("nil PathProof repository database")
	}
	var raw []byte
	if err := repository.db.sql.QueryRowContext(ctx, "SELECT proof_id FROM proofs WHERE plan_id=?", planID[:]).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return pathproof.PathProof{}, pathproof.ErrPathProofNotFound
	} else if err != nil {
		return pathproof.PathProof{}, err
	}
	var id pathproof.PathProofID
	if err := copyExact(id[:], raw, "PathProof ID"); err != nil {
		return pathproof.PathProof{}, err
	}
	return repository.LoadPathProof(ctx, id)
}

func loadPathProof(ctx context.Context, query queryer, id pathproof.PathProofID) (pathproof.PathProof, error) {
	var result pathproof.PathProof
	var proofID, requestID, planID, snapshotID, root []byte
	var createdAt int64
	err := query.QueryRowContext(ctx, `SELECT proof_id,request_id,plan_id,snapshot_id,base_trust_root,created_at
		FROM proofs WHERE proof_id=?`, id[:]).Scan(&proofID, &requestID, &planID, &snapshotID, &root, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return pathproof.PathProof{}, pathproof.ErrPathProofNotFound
	}
	if err != nil {
		return pathproof.PathProof{}, fmt.Errorf("load PathProof: %w", err)
	}
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{result.ID[:], proofID, "PathProof ID"}, {result.RequestID[:], requestID, "PathProof request ID"}, {result.PlanID[:], planID, "PathProof plan ID"}, {result.SnapshotID[:], snapshotID, "PathProof snapshot ID"}, {result.BaseTrustRoot.Hash[:], root, "PathProof base TrustRoot"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return pathproof.PathProof{}, err
		}
	}
	result.CreatedAt = fromUnix(createdAt)
	rows, err := query.QueryContext(ctx, `SELECT plan_hop_index,edge_id,to_node_id,block_hash,witness_id
		FROM proof_hops WHERE proof_id=? ORDER BY hop_index`, id[:])
	if err != nil {
		return pathproof.PathProof{}, err
	}
	for rows.Next() {
		var hop pathproof.PathProofHop
		var edgeID, toNodeID, blockHash, witnessID []byte
		if err := rows.Scan(&hop.PlanHopIndex, &edgeID, &toNodeID, &blockHash, &witnessID); err != nil {
			_ = rows.Close()
			return pathproof.PathProof{}, err
		}
		for _, item := range []struct {
			to, from []byte
			label    string
		}{{hop.EdgeID[:], edgeID, "PathProof edge ID"}, {hop.ToNodeID[:], toNodeID, "PathProof to node"}, {hop.BlockHash[:], blockHash, "PathProof block hash"}, {hop.WitnessID[:], witnessID, "PathProof witness ID"}} {
			if err := copyExact(item.to, item.from, item.label); err != nil {
				_ = rows.Close()
				return pathproof.PathProof{}, err
			}
		}
		result.Hops = append(result.Hops, hop)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return pathproof.PathProof{}, err
	}
	if err := rows.Close(); err != nil {
		return pathproof.PathProof{}, err
	}
	plan, err := loadPlan(ctx, query, result.PlanID)
	if err != nil {
		if errors.Is(err, planner.ErrPlanNotFound) {
			return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
		}
		return pathproof.PathProof{}, fmt.Errorf("load persisted PathProof plan: %w", err)
	}
	if plan.Type != planner.PathPlan || plan.RequestID != result.RequestID || plan.SnapshotID != result.SnapshotID || len(result.Hops) == 0 || len(result.Hops) != len(plan.Hops) {
		return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
	}
	target, err := loadSnapshotNode(ctx, query, result.SnapshotID, plan.TargetNodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
		}
		return pathproof.PathProof{}, fmt.Errorf("load persisted PathProof target: %w", err)
	}
	if target.Root != result.BaseTrustRoot {
		return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
	}
	for index, hop := range result.Hops {
		wantPlanIndex := len(plan.Hops) - 1 - index
		if hop.PlanHopIndex != wantPlanIndex || hop.EdgeID != plan.Hops[wantPlanIndex] {
			return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
		}
		material, err := loadPathProofMaterial(ctx, query, result.PlanID, result.SnapshotID, hop.PlanHopIndex, hop.EdgeID)
		if err != nil {
			if errors.Is(err, pathproof.ErrProofMaterialMissing) {
				return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
			}
			return pathproof.PathProof{}, fmt.Errorf("load persisted PathProof material: %w", err)
		}
		if material.Witness.ID != hop.WitnessID || material.ToNode.ID != hop.ToNodeID || material.ToNode.Key.BlockHash != hop.BlockHash {
			return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
		}
		witness, err := domain.NewMembershipWitness(material.Witness.LeafIndex, material.Witness.Siblings)
		if err != nil {
			return pathproof.PathProof{}, pathproof.ErrProofMaterialMissing
		}
		result.BlockHashes = append(result.BlockHashes, hop.BlockHash)
		result.Witnesses = append(result.Witnesses, witness)
	}
	if result.ID != pathproof.ComputePathProofID(result) {
		return pathproof.PathProof{}, pathproof.ErrPathProofIDMismatch
	}
	return result, nil
}

func sameHashes(left, right []common.Hash) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ pathproof.Repository = (*PathProofRepository)(nil)
