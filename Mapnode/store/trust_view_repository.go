package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type TrustViewRepository struct{ db *DB }

func NewTrustViewRepository(db *DB) *TrustViewRepository { return &TrustViewRepository{db: db} }

// MergeActiveTrustEdge atomically validates active evidence, merges both
// TrustRoot-bearing nodes and activates their directed TrustEdge.
func (repository *TrustViewRepository) MergeActiveTrustEdge(
	ctx context.Context,
	from, to trustview.TrustNode,
	edge trustview.TrustEdge,
) (bool, error) {
	if repository == nil || repository.db == nil {
		return false, errors.New("nil TrustView repository database")
	}
	if err := from.Validate(); err != nil {
		return false, err
	}
	if err := to.Validate(); err != nil {
		return false, err
	}
	if err := edge.Validate(); err != nil {
		return false, err
	}
	if edge.From != from.ID || edge.To != to.ID {
		return false, trustview.ErrInvalidTrustEdge
	}
	if from.Key.ChainID == to.Key.ChainID {
		return false, trustview.ErrInvalidTrustEdge
	}
	if edge.PathStepCost > math.MaxInt64 {
		return false, errors.New("path step cost exceeds SQLite INTEGER range")
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin TrustView merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range []evidence.ID{from.EvidenceID, to.EvidenceID, edge.EvidenceID} {
		if err := requireActiveEvidence(ctx, tx, id); err != nil {
			return false, err
		}
	}
	changed := false
	for _, node := range []trustview.TrustNode{from, to} {
		inserted, err := mergeTrustNode(ctx, tx, node)
		if err != nil {
			return false, err
		}
		changed = changed || inserted
	}
	inserted, err := mergeTrustEdge(ctx, tx, edge)
	if err != nil {
		return false, err
	}
	changed = changed || inserted
	if changed {
		if err := bumpGraphRevision(ctx, tx); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit TrustView merge: %w", err)
	}
	return changed, nil
}

func requireActiveEvidence(ctx context.Context, tx *sql.Tx, id evidence.ID) error {
	var state evidence.State
	if err := tx.QueryRowContext(ctx, "SELECT state FROM evidence WHERE id=?", id[:]).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: evidence %x not found", ErrInactiveEvidence, id)
		}
		return fmt.Errorf("load TrustView evidence: %w", err)
	}
	if state != evidence.Active {
		return fmt.Errorf("%w: evidence %x is %s", ErrInactiveEvidence, id, state)
	}
	return nil
}

func mergeTrustNode(ctx context.Context, tx *sql.Tx, node trustview.TrustNode) (bool, error) {
	var id, root, evidenceID []byte
	err := tx.QueryRowContext(ctx, `SELECT node_id,trust_root,evidence_id FROM trust_nodes
		WHERE chain_id=? AND block_height=? AND block_hash=?`,
		node.Key.ChainID[:], node.Key.Height[:], node.Key.BlockHash[:]).Scan(&id, &root, &evidenceID)
	if err == nil {
		if !equalBytes(id, node.ID[:]) {
			return false, ErrGraphConflict
		}
		if !equalBytes(root, node.Root.Hash[:]) {
			return false, trustview.ErrTrustRootConflict
		}
		if !equalBytes(evidenceID, node.EvidenceID[:]) {
			return false, ErrGraphConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("load TrustView node: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO trust_nodes(
		node_id,chain_id,block_height,block_hash,trust_root,evidence_id,evidence_state,first_observed_at
	) VALUES(?,?,?,?,?,?,?,?)`, node.ID[:], node.Key.ChainID[:], node.Key.Height[:], node.Key.BlockHash[:],
		node.Root.Hash[:], node.EvidenceID[:], evidence.Active, time.Now().UTC().UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert TrustView node: %w", err)
	}
	return true, nil
}

func mergeTrustEdge(ctx context.Context, tx *sql.Tx, edge trustview.TrustEdge) (bool, error) {
	var from, to, evidenceID []byte
	var active int
	var cost int64
	err := tx.QueryRowContext(ctx, `SELECT from_node_id,to_node_id,evidence_id,active,path_step_cost
		FROM trust_edges WHERE edge_id=?`, edge.ID[:]).Scan(&from, &to, &evidenceID, &active, &cost)
	if err == nil {
		if !equalBytes(from, edge.From[:]) || !equalBytes(to, edge.To[:]) || !equalBytes(evidenceID, edge.EvidenceID[:]) ||
			cost != int64(edge.PathStepCost) {
			return false, ErrGraphConflict
		}
		if active == 1 {
			return false, nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE trust_edges SET active=1 WHERE edge_id=?", edge.ID[:]); err != nil {
			return false, fmt.Errorf("activate TrustEdge: %w", err)
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("load TrustEdge: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO trust_edges(
		edge_id,from_node_id,to_node_id,evidence_id,active,path_step_cost,created_at
	) VALUES(?,?,?,?,1,?,?)`, edge.ID[:], edge.From[:], edge.To[:], edge.EvidenceID[:], int64(edge.PathStepCost), time.Now().UTC().UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert TrustEdge: %w", err)
	}
	return true, nil
}

func bumpGraphRevision(ctx context.Context, tx *sql.Tx) error {
	result, err := tx.ExecContext(ctx, "UPDATE graph_state SET revision=revision+1 WHERE singleton=1 AND revision<9223372036854775807")
	if err != nil {
		return fmt.Errorf("advance TrustView revision: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.New("TrustView graph revision exhausted")
	}
	return nil
}

func (repository *TrustViewRepository) SaveMembershipWitness(ctx context.Context, witness trustview.MembershipWitness) (bool, error) {
	if repository == nil || repository.db == nil {
		return false, errors.New("nil TrustView repository database")
	}
	want := trustview.NewMembershipWitness(witness.EvidenceID, witness.LeafIndex, witness.Siblings)
	if witness.ID != want.ID || len(witness.Siblings) == 0 {
		return false, errors.New("invalid TrustRoot membership witness")
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin witness insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireActiveEvidence(ctx, tx, witness.EvidenceID); err != nil {
		return false, err
	}
	var existingID []byte
	err = tx.QueryRowContext(ctx, "SELECT witness_id FROM membership_witnesses WHERE evidence_id=?", witness.EvidenceID[:]).Scan(&existingID)
	if err == nil {
		if !equalBytes(existingID, witness.ID[:]) {
			return false, ErrGraphConflict
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("load membership witness: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO membership_witnesses(witness_id,evidence_id,leaf_index,created_at)
		VALUES(?,?,?,?)`, witness.ID[:], witness.EvidenceID[:], int64(witness.LeafIndex), time.Now().UTC().UnixNano()); err != nil {
		return false, fmt.Errorf("insert membership witness: %w", err)
	}
	for index, sibling := range witness.Siblings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO membership_witness_siblings(witness_id,sibling_index,sibling_hash)
			VALUES(?,?,?)`, witness.ID[:], index, sibling[:]); err != nil {
			return false, fmt.Errorf("insert witness sibling: %w", err)
		}
	}
	if err := bumpGraphRevision(ctx, tx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit witness insert: %w", err)
	}
	return true, nil
}

type TrustViewSnapshotRequest struct {
	RequestID             domain.RequestID
	Attempt               uint64
	HomeChainID           domain.ChainID
	ExpectedHomeTrustRoot trustview.TrustRoot
	StartNodeID           trustview.NodeID
	TargetNodeID          trustview.NodeID
}

func (repository *TrustViewRepository) CreateTrustViewSnapshot(ctx context.Context, request TrustViewSnapshotRequest) (trustview.TrustViewSnapshot, error) {
	if repository == nil || repository.db == nil {
		return trustview.TrustViewSnapshot{}, errors.New("nil TrustView repository database")
	}
	if request.Attempt > math.MaxInt64 {
		return trustview.TrustViewSnapshot{}, errors.New("snapshot attempt exceeds SQLite INTEGER range")
	}
	if request.StartNodeID == request.TargetNodeID {
		return trustview.TrustViewSnapshot{}, errors.New("TrustView snapshot start and target must differ")
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return trustview.TrustViewSnapshot{}, fmt.Errorf("begin TrustView snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM graph_state WHERE singleton=1").Scan(&revision); err != nil {
		return trustview.TrustViewSnapshot{}, err
	}
	start, err := loadActiveTrustNode(ctx, tx, request.StartNodeID)
	if err != nil {
		return trustview.TrustViewSnapshot{}, err
	}
	if _, err := loadActiveTrustNode(ctx, tx, request.TargetNodeID); err != nil {
		return trustview.TrustViewSnapshot{}, err
	}
	if start.Key.ChainID != request.HomeChainID || start.Root != request.ExpectedHomeTrustRoot {
		return trustview.TrustViewSnapshot{}, trustview.ErrTrustRootConflict
	}
	id := trustview.ComputeSnapshotID(request.RequestID, request.Attempt, uint64(revision), request.StartNodeID, request.TargetNodeID, request.ExpectedHomeTrustRoot)
	if existing, err := loadSnapshot(ctx, tx, id); err == nil {
		if err := tx.Commit(); err != nil {
			return trustview.TrustViewSnapshot{}, err
		}
		return existing, nil
	} else if !errors.Is(err, ErrRecordNotFound) {
		return trustview.TrustViewSnapshot{}, err
	}
	createdAt := time.Now().UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO trustview_snapshots(
		snapshot_id,graph_revision,home_chain_id,home_trust_root,start_node_id,target_node_id,sealed,created_at
	) VALUES(?,?,?,?,NULL,NULL,0,?)`, id[:], revision, request.HomeChainID[:], request.ExpectedHomeTrustRoot.Hash[:], createdAt); err != nil {
		return trustview.TrustViewSnapshot{}, fmt.Errorf("insert TrustView snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_nodes(snapshot_id,node_id,chain_id,block_height,block_hash,trust_root)
		SELECT ?,n.node_id,n.chain_id,n.block_height,n.block_hash,n.trust_root FROM trust_nodes n
		JOIN evidence e ON e.id=n.evidence_id WHERE n.evidence_state='active' AND e.state='active'`, id[:]); err != nil {
		return trustview.TrustViewSnapshot{}, fmt.Errorf("materialize TrustView nodes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_edges(snapshot_id,edge_id,from_node_id,to_node_id,evidence_id,witness_id,path_step_cost)
		SELECT ?,g.edge_id,g.from_node_id,g.to_node_id,g.evidence_id,w.witness_id,g.path_step_cost
		FROM trust_edges g JOIN evidence e ON e.id=g.evidence_id
		LEFT JOIN membership_witnesses w ON w.evidence_id=g.evidence_id
		WHERE g.active=1 AND e.state='active'`, id[:]); err != nil {
		return trustview.TrustViewSnapshot{}, fmt.Errorf("materialize TrustEdges: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE trustview_snapshots SET start_node_id=?,target_node_id=?,sealed=1
		WHERE snapshot_id=? AND EXISTS(SELECT 1 FROM snapshot_nodes WHERE snapshot_id=? AND node_id=?)
		AND EXISTS(SELECT 1 FROM snapshot_nodes WHERE snapshot_id=? AND node_id=?)`,
		request.StartNodeID[:], request.TargetNodeID[:], id[:], id[:], request.StartNodeID[:], id[:], request.TargetNodeID[:])
	if err != nil {
		return trustview.TrustViewSnapshot{}, fmt.Errorf("seal TrustView snapshot: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return trustview.TrustViewSnapshot{}, trustview.ErrDanglingTrustEdge
	}
	snapshot, err := loadSnapshot(ctx, tx, id)
	if err != nil {
		return trustview.TrustViewSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return trustview.TrustViewSnapshot{}, fmt.Errorf("commit TrustView snapshot: %w", err)
	}
	return snapshot, nil
}

func (repository *TrustViewRepository) LoadTrustViewSnapshot(ctx context.Context, id trustview.SnapshotID) (trustview.TrustViewSnapshot, error) {
	if repository == nil || repository.db == nil {
		return trustview.TrustViewSnapshot{}, errors.New("nil TrustView repository database")
	}
	return loadSnapshot(ctx, repository.db.sql, id)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadActiveTrustNode(ctx context.Context, query queryer, id trustview.NodeID) (trustview.TrustNode, error) {
	var node trustview.TrustNode
	var nodeID, chainID, height, blockHash, root, evidenceID []byte
	err := query.QueryRowContext(ctx, `SELECT n.node_id,n.chain_id,n.block_height,n.block_hash,n.trust_root,n.evidence_id
		FROM trust_nodes n JOIN evidence e ON e.id=n.evidence_id WHERE n.node_id=? AND n.evidence_state='active' AND e.state='active'`, id[:]).Scan(&nodeID, &chainID, &height, &blockHash, &root, &evidenceID)
	if errors.Is(err, sql.ErrNoRows) {
		return node, ErrRecordNotFound
	}
	if err != nil {
		return node, fmt.Errorf("load active TrustView node: %w", err)
	}
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{node.ID[:], nodeID, "node ID"}, {node.Key.ChainID[:], chainID, "chain ID"}, {node.Key.Height[:], height, "height"}, {node.Key.BlockHash[:], blockHash, "block hash"}, {node.Root.Hash[:], root, "TrustRoot"}, {node.EvidenceID[:], evidenceID, "evidence ID"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return trustview.TrustNode{}, err
		}
	}
	return node, nil
}

func loadSnapshot(ctx context.Context, query queryer, id trustview.SnapshotID) (trustview.TrustViewSnapshot, error) {
	var snapshot trustview.TrustViewSnapshot
	var snapshotID, home, root, start, target []byte
	var revision int64
	var sealed int
	err := query.QueryRowContext(ctx, `SELECT snapshot_id,graph_revision,home_chain_id,home_trust_root,start_node_id,target_node_id,sealed
		FROM trustview_snapshots WHERE snapshot_id=?`, id[:]).Scan(&snapshotID, &revision, &home, &root, &start, &target, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, ErrRecordNotFound
	}
	if err != nil {
		return snapshot, fmt.Errorf("load TrustView snapshot: %w", err)
	}
	for _, item := range []struct {
		to, from []byte
		label    string
	}{{snapshot.ID[:], snapshotID, "snapshot ID"}, {snapshot.HomeChainID[:], home, "home chain"}, {snapshot.HomeTrustRoot.Hash[:], root, "home TrustRoot"}, {snapshot.StartNodeID[:], start, "start node"}, {snapshot.TargetNodeID[:], target, "target node"}} {
		if err := copyExact(item.to, item.from, item.label); err != nil {
			return trustview.TrustViewSnapshot{}, err
		}
	}
	snapshot.Revision, snapshot.Sealed = uint64(revision), sealed == 1
	rows, err := query.QueryContext(ctx, `SELECT node_id,chain_id,block_height,block_hash,trust_root FROM snapshot_nodes WHERE snapshot_id=? ORDER BY node_id`, id[:])
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var node trustview.TrustNode
		var nodeID, chain, height, hash, trustRoot []byte
		if err := rows.Scan(&nodeID, &chain, &height, &hash, &trustRoot); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		for _, item := range []struct {
			to, from []byte
			label    string
		}{{node.ID[:], nodeID, "node ID"}, {node.Key.ChainID[:], chain, "chain ID"}, {node.Key.Height[:], height, "height"}, {node.Key.BlockHash[:], hash, "block hash"}, {node.Root.Hash[:], trustRoot, "TrustRoot"}} {
			if err := copyExact(item.to, item.from, item.label); err != nil {
				_ = rows.Close()
				return snapshot, err
			}
		}
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}
	rows, err = query.QueryContext(ctx, `SELECT edge_id,from_node_id,to_node_id,evidence_id,witness_id,path_step_cost FROM snapshot_edges WHERE snapshot_id=? ORDER BY edge_id`, id[:])
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var edge trustview.TrustEdge
		var edgeID, from, to, evidenceID []byte
		var witness []byte
		var cost int64
		if err := rows.Scan(&edgeID, &from, &to, &evidenceID, &witness, &cost); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		for _, item := range []struct {
			to, from []byte
			label    string
		}{{edge.ID[:], edgeID, "edge ID"}, {edge.From[:], from, "from node"}, {edge.To[:], to, "to node"}, {edge.EvidenceID[:], evidenceID, "evidence ID"}} {
			if err := copyExact(item.to, item.from, item.label); err != nil {
				_ = rows.Close()
				return snapshot, err
			}
		}
		if witness != nil {
			var witnessID trustview.WitnessID
			if err := copyExact(witnessID[:], witness, "witness ID"); err != nil {
				_ = rows.Close()
				return snapshot, err
			}
			edge.WitnessID = &witnessID
		}
		edge.PathStepCost = uint64(cost)
		snapshot.Edges = append(snapshot.Edges, edge)
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}
