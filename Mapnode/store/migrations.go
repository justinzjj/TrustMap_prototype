package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{{version: 1, name: "phase3_core", sql: phase3Schema}}

const migrationTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK(version > 0),
    name TEXT NOT NULL CHECK(length(name) > 0),
    checksum BLOB NOT NULL CHECK(typeof(checksum) = 'blob' AND length(checksum) = 32),
    applied_at INTEGER NOT NULL CHECK(applied_at > 0)
) STRICT;`

func (db *DB) migrate() error {
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(migrationTable); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	rows, err := tx.Query("SELECT version,name,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	type appliedMigration struct {
		version  int
		name     string
		checksum []byte
	}
	var applied []appliedMigration
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan migration history: %w", err)
		}
		applied = append(applied, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate migration history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration history: %w", err)
	}
	for index, item := range applied {
		wantVersion := index + 1
		if item.version != wantVersion {
			return fmt.Errorf("%w: got version %d after %d", ErrMigrationGap, item.version, index)
		}
		if item.version > len(migrations) {
			return fmt.Errorf("%w: version %d", ErrFutureMigration, item.version)
		}
		want := migrations[index]
		checksum := sha256.Sum256([]byte(want.sql))
		if item.name != want.name || !equalBytes(item.checksum, checksum[:]) {
			return fmt.Errorf("%w: version %d", ErrMigrationChecksum, item.version)
		}
	}

	for index := len(applied); index < len(migrations); index++ {
		item := migrations[index]
		if item.version != index+1 {
			return fmt.Errorf("%w: embedded version %d at index %d", ErrMigrationGap, item.version, index)
		}
		if _, err := tx.Exec(item.sql); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", item.version, item.name, err)
		}
		checksum := sha256.Sum256([]byte(item.sql))
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)",
			item.version, item.name, checksum[:], time.Now().UTC().UnixNano(),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", item.version, err)
		}
	}
	if err := tx.Commit(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

const phase3Schema = `
CREATE TABLE evidence (
    id BLOB PRIMARY KEY CHECK(typeof(id)='blob' AND length(id)=32),
    chain_id BLOB NOT NULL CHECK(typeof(chain_id)='blob' AND length(chain_id)=32),
    contract_address BLOB NOT NULL CHECK(typeof(contract_address)='blob' AND length(contract_address)=20),
    block_number BLOB NOT NULL CHECK(typeof(block_number)='blob' AND length(block_number)=32),
    block_hash BLOB NOT NULL CHECK(typeof(block_hash)='blob' AND length(block_hash)=32),
    tx_hash BLOB NOT NULL CHECK(typeof(tx_hash)='blob' AND length(tx_hash)=32),
    tx_index INTEGER NOT NULL CHECK(tx_index BETWEEN 0 AND 4294967295),
    log_index INTEGER NOT NULL CHECK(log_index BETWEEN 0 AND 4294967295),
    payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
    state TEXT NOT NULL CHECK(state IN ('candidate','verified','confirmed','active','invalid')),
    invalid_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    updated_at INTEGER NOT NULL CHECK(updated_at > 0),
    UNIQUE(chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest),
    CHECK((state='invalid' AND length(invalid_reason)>0) OR (state<>'invalid' AND invalid_reason=''))
) STRICT;

CREATE TABLE evidence_transitions (
    sequence INTEGER PRIMARY KEY,
    evidence_id BLOB NOT NULL CHECK(typeof(evidence_id)='blob' AND length(evidence_id)=32),
    from_state TEXT NOT NULL CHECK(from_state IN ('candidate','verified','confirmed','active','invalid')),
    to_state TEXT NOT NULL CHECK(to_state IN ('candidate','verified','confirmed','active','invalid')),
    reason TEXT NOT NULL DEFAULT '',
    changed_at INTEGER NOT NULL CHECK(changed_at > 0),
    FOREIGN KEY(evidence_id) REFERENCES evidence(id) ON DELETE CASCADE,
    UNIQUE(evidence_id,from_state,to_state)
) STRICT;

CREATE TABLE requests (
    id BLOB PRIMARY KEY CHECK(typeof(id)='blob' AND length(id)=32),
    home_chain_id BLOB NOT NULL CHECK(typeof(home_chain_id)='blob' AND length(home_chain_id)=32),
    gateway BLOB NOT NULL CHECK(typeof(gateway)='blob' AND length(gateway)=20),
    requester BLOB NOT NULL CHECK(typeof(requester)='blob' AND length(requester)=20),
    nonce BLOB NOT NULL CHECK(typeof(nonce)='blob' AND length(nonce)=32),
    source_chain_id BLOB NOT NULL CHECK(typeof(source_chain_id)='blob' AND length(source_chain_id)=32),
    source_height BLOB NOT NULL CHECK(typeof(source_height)='blob' AND length(source_height)=32),
    source_block_hash BLOB NOT NULL CHECK(typeof(source_block_hash)='blob' AND length(source_block_hash)=32),
    state TEXT NOT NULL CHECK(state IN ('observed','evidence_ready','planned','proof_ready','rejected','retryable','replanned','direct_fallback')),
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    updated_at INTEGER NOT NULL CHECK(updated_at > 0),
    UNIQUE(home_chain_id,gateway,requester,nonce,source_chain_id,source_height,source_block_hash)
) STRICT;

CREATE TABLE request_transitions (
    sequence INTEGER PRIMARY KEY,
    request_id BLOB NOT NULL CHECK(typeof(request_id)='blob' AND length(request_id)=32),
    from_state TEXT NOT NULL CHECK(from_state IN ('observed','evidence_ready','planned','proof_ready','rejected','retryable','replanned','direct_fallback')),
    to_state TEXT NOT NULL CHECK(to_state IN ('observed','evidence_ready','planned','proof_ready','rejected','retryable','replanned','direct_fallback')),
    reason TEXT NOT NULL DEFAULT '',
    changed_at INTEGER NOT NULL CHECK(changed_at > 0),
    FOREIGN KEY(request_id) REFERENCES requests(id) ON DELETE CASCADE,
    UNIQUE(request_id,from_state,to_state)
) STRICT;

CREATE TABLE graph_state (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    revision INTEGER NOT NULL CHECK(revision >= 0)
) STRICT;

INSERT INTO graph_state(singleton,revision) VALUES(1,0);

CREATE TABLE trust_nodes (
    node_id BLOB PRIMARY KEY CHECK(typeof(node_id)='blob' AND length(node_id)=32),
    chain_id BLOB NOT NULL CHECK(typeof(chain_id)='blob' AND length(chain_id)=32),
    block_height BLOB NOT NULL CHECK(typeof(block_height)='blob' AND length(block_height)=32),
    block_hash BLOB NOT NULL CHECK(typeof(block_hash)='blob' AND length(block_hash)=32),
    trust_root BLOB NOT NULL CHECK(typeof(trust_root)='blob' AND length(trust_root)=32),
    evidence_id BLOB CHECK(evidence_id IS NULL OR (typeof(evidence_id)='blob' AND length(evidence_id)=32)),
    evidence_state TEXT NOT NULL CHECK(evidence_state IN ('candidate','verified','confirmed','active','invalid')),
    first_observed_at INTEGER NOT NULL CHECK(first_observed_at > 0),
    FOREIGN KEY(evidence_id) REFERENCES evidence(id),
    UNIQUE(chain_id,block_height,block_hash)
) STRICT;

CREATE TABLE trust_edges (
    edge_id BLOB PRIMARY KEY CHECK(typeof(edge_id)='blob' AND length(edge_id)=32),
    from_node_id BLOB NOT NULL CHECK(typeof(from_node_id)='blob' AND length(from_node_id)=32),
    to_node_id BLOB NOT NULL CHECK(typeof(to_node_id)='blob' AND length(to_node_id)=32),
    evidence_id BLOB NOT NULL UNIQUE CHECK(typeof(evidence_id)='blob' AND length(evidence_id)=32),
    active INTEGER NOT NULL CHECK(active IN (0,1)),
    dependency_leaf_index INTEGER NOT NULL CHECK(dependency_leaf_index BETWEEN 0 AND 4294967295),
    path_step_cost INTEGER NOT NULL CHECK(path_step_cost >= 0),
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    FOREIGN KEY(from_node_id) REFERENCES trust_nodes(node_id),
    FOREIGN KEY(to_node_id) REFERENCES trust_nodes(node_id),
    FOREIGN KEY(evidence_id) REFERENCES evidence(id),
    UNIQUE(from_node_id,to_node_id,evidence_id)
) STRICT;

CREATE TRIGGER require_active_evidence_for_trust_node_insert
BEFORE INSERT ON trust_nodes
WHEN NEW.evidence_id IS NULL OR NEW.evidence_state<>'active'
  OR COALESCE((SELECT state FROM evidence WHERE id=NEW.evidence_id),'')<>'active'
BEGIN
    SELECT RAISE(ABORT,'TrustView node requires active evidence');
END;

CREATE TRIGGER require_active_evidence_for_trust_node_update
BEFORE UPDATE ON trust_nodes
WHEN NEW.evidence_id IS NULL OR NEW.evidence_state<>'active'
  OR COALESCE((SELECT state FROM evidence WHERE id=NEW.evidence_id),'')<>'active'
BEGIN
    SELECT RAISE(ABORT,'TrustView node requires active evidence');
END;

CREATE TRIGGER require_active_evidence_for_trust_edge_insert
BEFORE INSERT ON trust_edges
WHEN NEW.active=1 AND (
  COALESCE((SELECT state FROM evidence WHERE id=NEW.evidence_id),'')<>'active'
  OR COALESCE((SELECT evidence_state FROM trust_nodes WHERE node_id=NEW.from_node_id),'')<>'active'
  OR COALESCE((SELECT evidence_state FROM trust_nodes WHERE node_id=NEW.to_node_id),'')<>'active'
)
BEGIN
    SELECT RAISE(ABORT,'active TrustEdge requires active evidence');
END;

CREATE TRIGGER require_active_evidence_for_trust_edge_update
BEFORE UPDATE ON trust_edges
WHEN NEW.active=1 AND (
  COALESCE((SELECT state FROM evidence WHERE id=NEW.evidence_id),'')<>'active'
  OR COALESCE((SELECT evidence_state FROM trust_nodes WHERE node_id=NEW.from_node_id),'')<>'active'
  OR COALESCE((SELECT evidence_state FROM trust_nodes WHERE node_id=NEW.to_node_id),'')<>'active'
)
BEGIN
    SELECT RAISE(ABORT,'active TrustEdge requires active evidence');
END;

CREATE TABLE trustview_snapshots (
    snapshot_id BLOB PRIMARY KEY CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    graph_revision INTEGER NOT NULL CHECK(graph_revision >= 0),
    home_chain_id BLOB NOT NULL CHECK(typeof(home_chain_id)='blob' AND length(home_chain_id)=32),
    home_trust_root BLOB NOT NULL CHECK(typeof(home_trust_root)='blob' AND length(home_trust_root)=32),
    start_node_id BLOB CHECK(start_node_id IS NULL OR (typeof(start_node_id)='blob' AND length(start_node_id)=32)),
    target_node_id BLOB CHECK(target_node_id IS NULL OR (typeof(target_node_id)='blob' AND length(target_node_id)=32)),
    sealed INTEGER NOT NULL DEFAULT 0 CHECK(sealed IN (0,1)),
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    CHECK(sealed=0 OR (start_node_id IS NOT NULL AND target_node_id IS NOT NULL)),
    FOREIGN KEY(snapshot_id,start_node_id) REFERENCES snapshot_nodes(snapshot_id,node_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(snapshot_id,target_node_id) REFERENCES snapshot_nodes(snapshot_id,node_id) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE snapshot_nodes (
    snapshot_id BLOB NOT NULL CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    node_id BLOB NOT NULL CHECK(typeof(node_id)='blob' AND length(node_id)=32),
    chain_id BLOB NOT NULL CHECK(typeof(chain_id)='blob' AND length(chain_id)=32),
    block_height BLOB NOT NULL CHECK(typeof(block_height)='blob' AND length(block_height)=32),
    block_hash BLOB NOT NULL CHECK(typeof(block_hash)='blob' AND length(block_hash)=32),
    trust_root BLOB NOT NULL CHECK(typeof(trust_root)='blob' AND length(trust_root)=32),
    PRIMARY KEY(snapshot_id,node_id),
    FOREIGN KEY(snapshot_id) REFERENCES trustview_snapshots(snapshot_id) ON DELETE CASCADE,
    UNIQUE(snapshot_id,node_id,block_hash)
) STRICT;

CREATE TABLE snapshot_edges (
    snapshot_id BLOB NOT NULL CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    edge_id BLOB NOT NULL CHECK(typeof(edge_id)='blob' AND length(edge_id)=32),
    from_node_id BLOB NOT NULL CHECK(typeof(from_node_id)='blob' AND length(from_node_id)=32),
    to_node_id BLOB NOT NULL CHECK(typeof(to_node_id)='blob' AND length(to_node_id)=32),
    evidence_id BLOB NOT NULL CHECK(typeof(evidence_id)='blob' AND length(evidence_id)=32),
    witness_id BLOB CHECK(witness_id IS NULL OR (typeof(witness_id)='blob' AND length(witness_id)=32)),
    dependency_leaf_index INTEGER NOT NULL CHECK(dependency_leaf_index BETWEEN 0 AND 4294967295),
    path_step_cost INTEGER NOT NULL CHECK(path_step_cost >= 0),
    PRIMARY KEY(snapshot_id,edge_id),
    FOREIGN KEY(snapshot_id) REFERENCES trustview_snapshots(snapshot_id) ON DELETE CASCADE,
    FOREIGN KEY(snapshot_id,from_node_id) REFERENCES snapshot_nodes(snapshot_id,node_id),
    FOREIGN KEY(snapshot_id,to_node_id) REFERENCES snapshot_nodes(snapshot_id,node_id),
    FOREIGN KEY(evidence_id) REFERENCES evidence(id),
    FOREIGN KEY(witness_id,evidence_id,dependency_leaf_index) REFERENCES membership_witnesses(witness_id,evidence_id,leaf_index),
    UNIQUE(snapshot_id,edge_id,witness_id,to_node_id)
) STRICT;

CREATE TRIGGER prevent_sealed_snapshot_update
BEFORE UPDATE ON trustview_snapshots
WHEN OLD.sealed=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot is immutable');
END;

CREATE TRIGGER prevent_sealed_snapshot_node_insert
BEFORE INSERT ON snapshot_nodes
WHEN (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=NEW.snapshot_id)=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot nodes are immutable');
END;

CREATE TRIGGER prevent_sealed_snapshot_node_update
BEFORE UPDATE ON snapshot_nodes
WHEN (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=OLD.snapshot_id)=1
  OR (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=NEW.snapshot_id)=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot nodes are immutable');
END;

CREATE TRIGGER prevent_sealed_snapshot_node_delete
BEFORE DELETE ON snapshot_nodes
WHEN (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=OLD.snapshot_id)=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot nodes are immutable');
END;

CREATE TRIGGER prevent_sealed_snapshot_edge_insert
BEFORE INSERT ON snapshot_edges
WHEN (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=NEW.snapshot_id)=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot edges are immutable');
END;

CREATE TRIGGER prevent_sealed_snapshot_edge_update
BEFORE UPDATE ON snapshot_edges
WHEN (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=OLD.snapshot_id)=1
  OR (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=NEW.snapshot_id)=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot edges are immutable');
END;

CREATE TRIGGER prevent_sealed_snapshot_edge_delete
BEFORE DELETE ON snapshot_edges
WHEN (SELECT sealed FROM trustview_snapshots WHERE snapshot_id=OLD.snapshot_id)=1
BEGIN
    SELECT RAISE(ABORT,'sealed snapshot edges are immutable');
END;

CREATE TABLE membership_witnesses (
    witness_id BLOB PRIMARY KEY CHECK(typeof(witness_id)='blob' AND length(witness_id)=32),
    evidence_id BLOB NOT NULL UNIQUE CHECK(typeof(evidence_id)='blob' AND length(evidence_id)=32),
    leaf_index INTEGER NOT NULL CHECK(leaf_index BETWEEN 0 AND 4294967295),
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    FOREIGN KEY(evidence_id) REFERENCES evidence(id) ON DELETE CASCADE,
    UNIQUE(witness_id,evidence_id),
    UNIQUE(witness_id,evidence_id,leaf_index)
) STRICT;

CREATE TABLE membership_witness_siblings (
    witness_id BLOB NOT NULL CHECK(typeof(witness_id)='blob' AND length(witness_id)=32),
    sibling_index INTEGER NOT NULL CHECK(sibling_index BETWEEN 0 AND 4294967295),
    sibling_hash BLOB NOT NULL CHECK(typeof(sibling_hash)='blob' AND length(sibling_hash)=32),
    PRIMARY KEY(witness_id,sibling_index),
    FOREIGN KEY(witness_id) REFERENCES membership_witnesses(witness_id) ON DELETE CASCADE
) STRICT;

CREATE TRIGGER prevent_global_witness_update
BEFORE UPDATE ON membership_witnesses
BEGIN
    SELECT RAISE(ABORT,'global witness is immutable');
END;

CREATE TRIGGER prevent_global_witness_delete
BEFORE DELETE ON membership_witnesses
BEGIN
    SELECT RAISE(ABORT,'global witness is immutable');
END;

CREATE TRIGGER prevent_global_witness_sibling_update
BEFORE UPDATE ON membership_witness_siblings
BEGIN
    SELECT RAISE(ABORT,'global witness sibling is immutable');
END;

CREATE TRIGGER prevent_global_witness_sibling_delete
BEFORE DELETE ON membership_witness_siblings
BEGIN
    SELECT RAISE(ABORT,'global witness sibling is immutable');
END;

CREATE TRIGGER prevent_referenced_witness_sibling_insert
BEFORE INSERT ON membership_witness_siblings
WHEN EXISTS(SELECT 1 FROM snapshot_edges WHERE witness_id=NEW.witness_id)
BEGIN
    SELECT RAISE(ABORT,'snapshot-referenced witness is sealed');
END;

CREATE TABLE plans (
    plan_id BLOB PRIMARY KEY CHECK(typeof(plan_id)='blob' AND length(plan_id)=32),
    request_id BLOB NOT NULL CHECK(typeof(request_id)='blob' AND length(request_id)=32),
    snapshot_id BLOB NOT NULL CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    profile_id TEXT NOT NULL CHECK(length(profile_id)>0),
    profile_fingerprint BLOB NOT NULL CHECK(typeof(profile_fingerprint)='blob' AND length(profile_fingerprint)=32),
    attempt INTEGER NOT NULL CHECK(attempt >= 0),
    plan_type TEXT NOT NULL CHECK(plan_type IN ('path','direct')),
    home_node_id BLOB NOT NULL CHECK(typeof(home_node_id)='blob' AND length(home_node_id)=32),
    target_node_id BLOB NOT NULL CHECK(typeof(target_node_id)='blob' AND length(target_node_id)=32),
    hop_count INTEGER NOT NULL CHECK(hop_count >= 0),
    path_step_cost INTEGER NOT NULL CHECK(path_step_cost >= 0),
    path_cost INTEGER CHECK(path_cost IS NULL OR path_cost >= 0),
    direct_cost INTEGER CHECK(direct_cost IS NULL OR direct_cost >= 0),
    fallback_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    FOREIGN KEY(request_id) REFERENCES requests(id),
    FOREIGN KEY(snapshot_id) REFERENCES trustview_snapshots(snapshot_id),
    FOREIGN KEY(snapshot_id,home_node_id) REFERENCES snapshot_nodes(snapshot_id,node_id),
    FOREIGN KEY(snapshot_id,target_node_id) REFERENCES snapshot_nodes(snapshot_id,node_id),
    UNIQUE(request_id,attempt),
    UNIQUE(plan_id,snapshot_id),
    UNIQUE(request_id,plan_id),
    UNIQUE(request_id,plan_id,snapshot_id)
) STRICT;

CREATE TRIGGER require_sealed_snapshot_for_plan
BEFORE INSERT ON plans
WHEN COALESCE((SELECT sealed FROM trustview_snapshots WHERE snapshot_id=NEW.snapshot_id),0)<>1
BEGIN
    SELECT RAISE(ABORT,'plan requires a sealed snapshot');
END;

CREATE TRIGGER prevent_plan_update
BEFORE UPDATE ON plans
BEGIN
    SELECT RAISE(ABORT,'plan is append-only');
END;

CREATE TRIGGER prevent_plan_delete
BEFORE DELETE ON plans
BEGIN
    SELECT RAISE(ABORT,'plan is append-only');
END;

CREATE TABLE plan_hops (
    plan_id BLOB NOT NULL CHECK(typeof(plan_id)='blob' AND length(plan_id)=32),
    snapshot_id BLOB NOT NULL CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    hop_index INTEGER NOT NULL CHECK(hop_index >= 0),
    edge_id BLOB NOT NULL CHECK(typeof(edge_id)='blob' AND length(edge_id)=32),
    PRIMARY KEY(plan_id,hop_index),
    FOREIGN KEY(plan_id,snapshot_id) REFERENCES plans(plan_id,snapshot_id) ON DELETE CASCADE,
    FOREIGN KEY(snapshot_id,edge_id) REFERENCES snapshot_edges(snapshot_id,edge_id),
    UNIQUE(plan_id,snapshot_id,hop_index,edge_id)
) STRICT;

CREATE TRIGGER enforce_plan_hop_count
BEFORE INSERT ON plan_hops
WHEN NEW.hop_index >= COALESCE((
    SELECT hop_count FROM plans WHERE plan_id=NEW.plan_id AND snapshot_id=NEW.snapshot_id
),-1)
BEGIN
    SELECT RAISE(ABORT,'plan hop index exceeds hop count');
END;

CREATE TRIGGER prevent_plan_hop_update
BEFORE UPDATE ON plan_hops
BEGIN
    SELECT RAISE(ABORT,'plan hop is append-only');
END;

CREATE TRIGGER prevent_plan_hop_delete
BEFORE DELETE ON plan_hops
BEGIN
    SELECT RAISE(ABORT,'plan hop is append-only');
END;

CREATE TABLE request_current_plan (
    request_id BLOB PRIMARY KEY CHECK(typeof(request_id)='blob' AND length(request_id)=32),
    plan_id BLOB NOT NULL UNIQUE CHECK(typeof(plan_id)='blob' AND length(plan_id)=32),
    FOREIGN KEY(request_id) REFERENCES requests(id) ON DELETE CASCADE,
    FOREIGN KEY(request_id,plan_id) REFERENCES plans(request_id,plan_id)
) STRICT;

CREATE TABLE proofs (
    proof_id BLOB PRIMARY KEY CHECK(typeof(proof_id)='blob' AND length(proof_id)=32),
    request_id BLOB NOT NULL CHECK(typeof(request_id)='blob' AND length(request_id)=32),
    plan_id BLOB NOT NULL CHECK(typeof(plan_id)='blob' AND length(plan_id)=32),
    snapshot_id BLOB NOT NULL CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    base_trust_root BLOB NOT NULL CHECK(typeof(base_trust_root)='blob' AND length(base_trust_root)=32),
    created_at INTEGER NOT NULL CHECK(created_at > 0),
    FOREIGN KEY(request_id) REFERENCES requests(id),
    FOREIGN KEY(request_id,plan_id,snapshot_id) REFERENCES plans(request_id,plan_id,snapshot_id),
    UNIQUE(proof_id,plan_id,snapshot_id)
) STRICT;

CREATE TABLE proof_hops (
    proof_id BLOB NOT NULL CHECK(typeof(proof_id)='blob' AND length(proof_id)=32),
    plan_id BLOB NOT NULL CHECK(typeof(plan_id)='blob' AND length(plan_id)=32),
    snapshot_id BLOB NOT NULL CHECK(typeof(snapshot_id)='blob' AND length(snapshot_id)=32),
    hop_index INTEGER NOT NULL CHECK(hop_index >= 0),
    plan_hop_index INTEGER NOT NULL CHECK(plan_hop_index >= 0),
    edge_id BLOB NOT NULL CHECK(typeof(edge_id)='blob' AND length(edge_id)=32),
    to_node_id BLOB NOT NULL CHECK(typeof(to_node_id)='blob' AND length(to_node_id)=32),
    block_hash BLOB NOT NULL CHECK(typeof(block_hash)='blob' AND length(block_hash)=32),
    witness_id BLOB NOT NULL CHECK(typeof(witness_id)='blob' AND length(witness_id)=32),
    PRIMARY KEY(proof_id,hop_index),
    FOREIGN KEY(proof_id,plan_id,snapshot_id) REFERENCES proofs(proof_id,plan_id,snapshot_id) ON DELETE CASCADE,
    FOREIGN KEY(plan_id,snapshot_id,plan_hop_index,edge_id) REFERENCES plan_hops(plan_id,snapshot_id,hop_index,edge_id),
    FOREIGN KEY(snapshot_id,edge_id,witness_id,to_node_id) REFERENCES snapshot_edges(snapshot_id,edge_id,witness_id,to_node_id),
    FOREIGN KEY(snapshot_id,to_node_id,block_hash) REFERENCES snapshot_nodes(snapshot_id,node_id,block_hash),
    UNIQUE(proof_id,plan_hop_index)
) STRICT;
`
