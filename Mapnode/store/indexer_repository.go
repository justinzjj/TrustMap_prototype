package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/indexer"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

type IndexerConfig struct {
	ChainID         domain.ChainID
	Gateway         common.Address
	GatewayCodeHash common.Hash
	DeploymentBlock domain.BlockHeight
	MerkleDepth     uint8
	PathStepCostGas uint64
}

type IndexerRepository struct {
	db           *DB
	beforeCommit func() error
}

func NewIndexerRepository(db *DB) *IndexerRepository { return &IndexerRepository{db: db} }

func (repository *IndexerRepository) Degrade(ctx context.Context, chainID domain.ChainID, reason string) error {
	if repository == nil || repository.db == nil || reason == "" {
		return errors.New("indexer degradation requires repository and reason")
	}
	_, err := repository.db.sql.ExecContext(ctx, `INSERT INTO indexer_degraded_states(chain_id,reason,degraded_at) VALUES(?,?,?) ON CONFLICT(chain_id) DO NOTHING`, chainID[:], reason, time.Now().UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("persist indexer degradation: %w", err)
	}
	return nil
}

func (repository *IndexerRepository) HasDegraded(ctx context.Context) (bool, error) {
	if repository == nil || repository.db == nil {
		return false, errors.New("nil indexer repository database")
	}
	var degraded int
	if err := repository.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM indexer_degraded_states)`).Scan(&degraded); err != nil {
		return false, err
	}
	return degraded == 1, nil
}

func (repository *IndexerRepository) Configure(ctx context.Context, config IndexerConfig) error {
	if repository == nil || repository.db == nil || config.ChainID.Validate() != nil || config.Gateway == (common.Address{}) || config.GatewayCodeHash == (common.Hash{}) || config.MerkleDepth == 0 || config.MerkleDepth > 32 || config.PathStepCostGas == 0 || config.PathStepCostGas > math.MaxInt64 {
		return errors.New("invalid trusted indexer configuration")
	}
	var gateway, codeHash, deployment []byte
	if err := repository.db.sql.QueryRowContext(ctx, `SELECT gateway,gateway_code_hash,deployment_block FROM live_chains WHERE chain_id=? AND validated_at IS NOT NULL`, config.ChainID[:]).Scan(&gateway, &codeHash, &deployment); err != nil {
		return fmt.Errorf("load validated live chain for indexer: %w", err)
	}
	if !equalBytes(gateway, config.Gateway[:]) || !equalBytes(codeHash, config.GatewayCodeHash[:]) || !equalBytes(deployment, config.DeploymentBlock[:]) {
		return ErrRecordConflict
	}
	result, err := repository.db.sql.ExecContext(ctx, `INSERT INTO live_indexer_configs(chain_id,gateway,gateway_code_hash,deployment_block,merkle_depth,path_step_cost_gas,created_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(chain_id) DO NOTHING`, config.ChainID[:], config.Gateway[:], config.GatewayCodeHash[:], config.DeploymentBlock[:], int64(config.MerkleDepth), int64(config.PathStepCostGas), time.Now().UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("configure confirmed indexer: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 1 {
		return nil
	}
	var persistedGateway, persistedCode, persistedBlock []byte
	var depth, cost int64
	if err := repository.db.sql.QueryRowContext(ctx, `SELECT gateway,gateway_code_hash,deployment_block,merkle_depth,path_step_cost_gas FROM live_indexer_configs WHERE chain_id=?`, config.ChainID[:]).Scan(&persistedGateway, &persistedCode, &persistedBlock, &depth, &cost); err != nil {
		return err
	}
	if !equalBytes(persistedGateway, config.Gateway[:]) || !equalBytes(persistedCode, config.GatewayCodeHash[:]) || !equalBytes(persistedBlock, config.DeploymentBlock[:]) || depth != int64(config.MerkleDepth) || cost != int64(config.PathStepCostGas) {
		return ErrRecordConflict
	}
	return nil
}

func (repository *IndexerRepository) ApplyConfirmedBlock(ctx context.Context, block indexer.ConfirmedBlock) (bool, error) {
	if repository == nil || repository.db == nil {
		return false, errors.New("nil indexer repository database")
	}
	if block.Number == math.MaxUint64 || block.Hash == (common.Hash{}) || block.ParentHash == (common.Hash{}) || block.Gateway == (common.Address{}) || block.MerkleDepth == 0 || block.PathStepCostGas == 0 {
		return false, errors.New("invalid confirmed block")
	}
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin confirmed block apply: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireIndexerConfig(ctx, tx, block); err != nil {
		return false, err
	}
	current, err := scanCanonicalCursor(tx.QueryRowContext(ctx, `SELECT chain_id,block_height,block_hash,state,degraded_reason FROM canonical_cursors WHERE chain_id=?`, block.ChainID[:]))
	if err != nil {
		return false, err
	}
	if current.State != reorg.Healthy {
		return false, reorg.ErrCursorDegraded
	}
	blockHeight, _ := domain.NewBlockHeight(block.Number)
	replay := false
	if current.Height == blockHeight {
		if current.Hash != block.Hash {
			return false, reorg.ErrCanonicalMismatch
		}
		replay = true
	} else if current != block.ExpectedCursor {
		return false, fmt.Errorf("%w: indexer expected cursor changed", ErrConcurrentUpdate)
	}
	advanced := current
	if !replay {
		advanced, err = current.Advance(current.Hash, reorg.CanonicalBlock{Height: blockHeight, Hash: block.Hash, ParentHash: block.ParentHash})
		if err != nil {
			return false, err
		}
	}
	for _, item := range block.Logs {
		if err := insertIndexedGatewayLog(ctx, tx, block, item); err != nil {
			return false, err
		}
	}
	for _, request := range block.Requests {
		if err := insertObservedRequest(ctx, tx, request); err != nil {
			return false, err
		}
	}
	for _, receipt := range block.Receipts {
		if receipt.BlockNumber != block.Number || receipt.BlockHash != block.Hash || receipt.TxHash == (common.Hash{}) || receipt.RequestID == (domain.RequestID{}) {
			return false, ErrRecordConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO verification_receipts(chain_id,tx_hash,block_number,block_hash,tx_index,request_id,status) VALUES(?,?,?,?,?,?,1) ON CONFLICT(chain_id,tx_hash) DO NOTHING`, block.ChainID[:], receipt.TxHash[:], blockHeight[:], block.Hash[:], int64(receipt.TxIndex), receipt.RequestID[:]); err != nil {
			return false, fmt.Errorf("insert verification receipt: %w", err)
		}
		if err := verifyPersistedReceipt(ctx, tx, block.ChainID, receipt); err != nil {
			return false, err
		}
	}
	for _, resolution := range block.Resolutions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_resolutions(request_id,chain_id,tx_hash,dependency_key,new_dependency,home_trust_root,log_index,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(request_id) DO NOTHING`, resolution.RequestID[:], block.ChainID[:], resolution.TxHash[:], resolution.DependencyKey[:], boolInt(resolution.NewDependency), resolution.HomeTrustRoot[:], int64(resolution.LogIndex), time.Now().UTC().UnixNano()); err != nil {
			return false, fmt.Errorf("insert request resolution: %w", err)
		}
		if err := verifyPersistedResolution(ctx, tx, block.ChainID, resolution); err != nil {
			return false, err
		}
		if err := verifyResolutionIndexedLog(ctx, tx, block, resolution); err != nil {
			return false, err
		}
	}
	if len(block.Receipts) != len(block.Resolutions) {
		return false, ErrEvidenceBinding
	}
	for _, receipt := range block.Receipts {
		matched := false
		for _, resolution := range block.Resolutions {
			matched = matched || (resolution.RequestID == receipt.RequestID && resolution.TxHash == receipt.TxHash)
		}
		if !matched {
			return false, ErrEvidenceBinding
		}
	}
	graphChanged := false
	for _, material := range block.Dependencies {
		changed, err := applyDependencyMaterialization(ctx, tx, block, material)
		if err != nil {
			return false, err
		}
		graphChanged = graphChanged || changed
	}
	if graphChanged {
		if err := bumpGraphRevision(ctx, tx); err != nil {
			return false, err
		}
	}
	if !replay {
		result, err := tx.ExecContext(ctx, `UPDATE canonical_cursors SET block_height=?,block_hash=?,updated_at=? WHERE chain_id=? AND block_height=? AND block_hash=? AND state='healthy'`, advanced.Height[:], advanced.Hash[:], time.Now().UTC().UnixNano(), block.ChainID[:], current.Height[:], current.Hash[:])
		if err != nil {
			return false, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return false, ErrConcurrentUpdate
		}
	}
	if repository.beforeCommit != nil {
		if err := repository.beforeCommit(); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit confirmed block: %w", err)
	}
	return !replay, nil
}

func requireIndexerConfig(ctx context.Context, tx *sql.Tx, block indexer.ConfirmedBlock) error {
	var gateway []byte
	var depth, cost int64
	if err := tx.QueryRowContext(ctx, `SELECT gateway,merkle_depth,path_step_cost_gas FROM live_indexer_configs WHERE chain_id=?`, block.ChainID[:]).Scan(&gateway, &depth, &cost); err != nil {
		return err
	}
	if !equalBytes(gateway, block.Gateway[:]) || depth != int64(block.MerkleDepth) || cost != int64(block.PathStepCostGas) {
		return ErrRecordConflict
	}
	return nil
}

func insertIndexedGatewayLog(ctx context.Context, tx *sql.Tx, block indexer.ConfirmedBlock, item indexer.IndexedGatewayLog) error {
	if item.Address != block.Gateway || item.BlockNumber != block.Number || item.BlockHash != block.Hash || item.TxHash == (common.Hash{}) || item.EventTopic == (common.Hash{}) || item.ContentDigest == (common.Hash{}) || len(item.Topics) == 0 || item.Topics[0] != item.EventTopic || len(item.Topics) > 4 || len(item.Data)%32 != 0 {
		return ErrRecordConflict
	}
	topics := make([]byte, 0, len(item.Topics)*32)
	for _, topic := range item.Topics {
		topics = append(topics, topic[:]...)
	}
	height, _ := domain.NewBlockHeight(item.BlockNumber)
	result, err := tx.ExecContext(ctx, `INSERT INTO indexed_gateway_logs(chain_id,gateway,block_number,block_hash,tx_hash,tx_index,log_index,event_topic,content_digest,topics,data) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(chain_id,block_hash,tx_hash,tx_index,log_index) DO NOTHING`, block.ChainID[:], block.Gateway[:], height[:], block.Hash[:], item.TxHash[:], int64(item.TxIndex), int64(item.LogIndex), item.EventTopic[:], item.ContentDigest[:], topics, item.Data)
	if err != nil {
		return fmt.Errorf("insert indexed Gateway log: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		var digest, persistedTopics, data []byte
		if err := tx.QueryRowContext(ctx, `SELECT content_digest,topics,data FROM indexed_gateway_logs WHERE chain_id=? AND block_hash=? AND tx_hash=? AND tx_index=? AND log_index=?`, block.ChainID[:], block.Hash[:], item.TxHash[:], int64(item.TxIndex), int64(item.LogIndex)).Scan(&digest, &persistedTopics, &data); err != nil {
			return err
		}
		if !equalBytes(digest, item.ContentDigest[:]) || !equalBytes(persistedTopics, topics) || !equalBytes(data, item.Data) {
			return ErrRecordConflict
		}
	}
	return nil
}

func insertObservedRequest(ctx context.Context, tx *sql.Tx, request coordinator.Request) error {
	if request.State != coordinator.Observed {
		return ErrRecordConflict
	}
	want, err := evidence.ComputeGatewayRequestID(request.HomeChainID, request.Gateway, request.Requester, new(big.Int).SetBytes(request.Nonce[:]), request.SourceChainID, request.SourceHeight, request.SourceBlockHash)
	if err != nil || want != request.ID {
		return ErrRecordConflict
	}
	request.CreatedAt, request.UpdatedAt = normalizedTimes(request.CreatedAt, request.UpdatedAt)
	if _, err := tx.ExecContext(ctx, `INSERT INTO requests(id,home_chain_id,gateway,requester,nonce,source_chain_id,source_height,source_block_hash,state,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, request.ID[:], request.HomeChainID[:], request.Gateway[:], request.Requester[:], request.Nonce[:], request.SourceChainID[:], request.SourceHeight[:], request.SourceBlockHash[:], request.State, request.Reason, toUnix(request.CreatedAt), toUnix(request.UpdatedAt)); err != nil {
		return err
	}
	persisted, err := scanRequest(tx.QueryRowContext(ctx, requestSelect+" WHERE id=?", request.ID[:]))
	if err != nil || !sameRequestIdentity(persisted, request) {
		return ErrRecordConflict
	}
	return nil
}

func verifyPersistedResolution(ctx context.Context, tx *sql.Tx, chainID domain.ChainID, want indexer.RequestResolutionRecord) error {
	var txHash, key, root []byte
	var fresh, logIndex int64
	if err := tx.QueryRowContext(ctx, `SELECT tx_hash,dependency_key,new_dependency,home_trust_root,log_index FROM request_resolutions WHERE request_id=? AND chain_id=?`, want.RequestID[:], chainID[:]).Scan(&txHash, &key, &fresh, &root, &logIndex); err != nil {
		return err
	}
	if !equalBytes(txHash, want.TxHash[:]) || !equalBytes(key, want.DependencyKey[:]) || fresh != int64(boolInt(want.NewDependency)) || !equalBytes(root, want.HomeTrustRoot[:]) || logIndex != int64(want.LogIndex) {
		return ErrRecordConflict
	}
	return nil
}

func verifyPersistedReceipt(ctx context.Context, tx *sql.Tx, chainID domain.ChainID, want indexer.VerificationReceiptRecord) error {
	var txHash, blockNumber, blockHash, requestID []byte
	var txIndex, status int64
	if err := tx.QueryRowContext(ctx, `SELECT tx_hash,block_number,block_hash,tx_index,request_id,status FROM verification_receipts WHERE chain_id=? AND tx_hash=?`, chainID[:], want.TxHash[:]).Scan(&txHash, &blockNumber, &blockHash, &txIndex, &requestID, &status); err != nil {
		return err
	}
	height, _ := domain.NewBlockHeight(want.BlockNumber)
	if !equalBytes(txHash, want.TxHash[:]) || !equalBytes(blockNumber, height[:]) || !equalBytes(blockHash, want.BlockHash[:]) || txIndex != int64(want.TxIndex) || !equalBytes(requestID, want.RequestID[:]) || status != 1 {
		return ErrRecordConflict
	}
	return nil
}

func verifyResolutionIndexedLog(ctx context.Context, tx *sql.Tx, block indexer.ConfirmedBlock, want indexer.RequestResolutionRecord) error {
	var topicsRaw, data []byte
	if err := tx.QueryRowContext(ctx, `SELECT topics,data FROM indexed_gateway_logs WHERE chain_id=? AND block_hash=? AND tx_hash=? AND log_index=? AND event_topic=?`, block.ChainID[:], block.Hash[:], want.TxHash[:], int64(want.LogIndex), chainabi.RequestResolvedTopic[:]).Scan(&topicsRaw, &data); err != nil {
		return ErrEvidenceBinding
	}
	if len(topicsRaw)%32 != 0 {
		return ErrEvidenceBinding
	}
	topics := make([]common.Hash, len(topicsRaw)/32)
	for index := range topics {
		copy(topics[index][:], topicsRaw[index*32:(index+1)*32])
	}
	parsed, err := chainabi.ParseRequestResolved(types.Log{Topics: topics, Data: data})
	if err != nil || parsed.RequestID != want.RequestID || parsed.DependencyKey != want.DependencyKey || parsed.NewDependency != want.NewDependency || parsed.HomeTrustRoot != want.HomeTrustRoot {
		return ErrEvidenceBinding
	}
	var requester []byte
	if err := tx.QueryRowContext(ctx, `SELECT requester FROM requests WHERE id=?`, want.RequestID[:]).Scan(&requester); err != nil || !equalBytes(requester, parsed.Requester[:]) {
		return ErrEvidenceBinding
	}
	return nil
}

func applyDependencyMaterialization(ctx context.Context, tx *sql.Tx, block indexer.ConfirmedBlock, material indexer.DependencyMaterialization) (bool, error) {
	if material.Evidence.State != evidence.Candidate || material.Evidence.ID != material.Dependency.EvidenceID || material.Edge.EvidenceID != material.Evidence.ID || material.Witness.EvidenceID != material.Evidence.ID || material.Edge.WitnessID == nil || *material.Edge.WitnessID != material.Witness.ID || material.Edge.PathStepCost != block.PathStepCostGas {
		return false, ErrEvidenceBinding
	}
	wantEvidenceID, err := evidence.ComputeID(material.Evidence.Locator)
	if err != nil || wantEvidenceID != material.Evidence.ID || material.Evidence.Locator.ChainID != block.ChainID || material.Evidence.Locator.ContractAddress != block.Gateway || material.Evidence.Locator.BlockHash != block.Hash {
		return false, ErrEvidenceBinding
	}
	if err := material.Dependency.Validate(material.To, material.Evidence.Locator.PayloadDigest); err != nil {
		return false, ErrEvidenceBinding
	}
	if material.From.ID != material.Edge.From || material.To.ID != material.Edge.To || material.From.Root.Hash == (common.Hash{}) || material.From.Key.ChainID != block.ChainID || material.From.Key.BlockHash != block.Hash {
		return false, ErrEvidenceBinding
	}
	if err := verifyActiveNodeContent(ctx, tx, material.From); err != nil {
		return false, err
	}
	if err := verifyActiveNodeContent(ctx, tx, material.To); err != nil {
		return false, err
	}
	var homeRoot []byte
	if err := tx.QueryRowContext(ctx, `SELECT home_trust_root FROM request_resolutions WHERE request_id=? AND new_dependency=1`, material.Dependency.RequestID[:]).Scan(&homeRoot); err != nil || !equalBytes(homeRoot, material.From.Root.Hash[:]) {
		return false, ErrEvidenceBinding
	}
	leaf := domain.LeafHash(material.Dependency.SourceTrustRoot.Hash, material.Dependency.SourceBlockHash)
	root, err := internalproof.RootFromWitness(leaf, material.Witness.LeafIndex, material.Witness.Siblings, block.MerkleDepth)
	if err != nil || root != material.From.Root.Hash {
		return false, ErrEvidenceBinding
	}
	if err := verifyDependencyIndexedLog(ctx, tx, block, material); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO evidence(id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'candidate','',?,?) ON CONFLICT(id) DO NOTHING`, material.Evidence.ID[:], material.Evidence.Locator.ChainID[:], material.Evidence.Locator.ContractAddress[:], material.Evidence.Locator.BlockNumber[:], material.Evidence.Locator.BlockHash[:], material.Evidence.Locator.TxHash[:], int64(material.Evidence.Locator.TxIndex), int64(material.Evidence.Locator.LogIndex), material.Evidence.Locator.PayloadDigest[:], toUnix(now), toUnix(now))
	if err != nil {
		return false, err
	}
	inserted, _ := result.RowsAffected()
	changed := inserted == 1
	if changed {
		from := evidence.Candidate
		for _, to := range []evidence.State{evidence.Verified, evidence.Confirmed, evidence.Active} {
			result, err := tx.ExecContext(ctx, `UPDATE evidence SET state=?,updated_at=? WHERE id=? AND state=?`, to, toUnix(now), material.Evidence.ID[:], from)
			if err != nil {
				return false, err
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return false, ErrConcurrentUpdate
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_transitions(evidence_id,from_state,to_state,reason,changed_at) VALUES(?,?,?,'confirmed Gateway receipt validation',?)`, material.Evidence.ID[:], from, to, toUnix(now)); err != nil {
				return false, err
			}
			from = to
		}
	} else if _, err := loadActiveEvidence(ctx, tx, material.Evidence.ID); err != nil {
		return false, err
	}
	wantWitness := trustview.NewMembershipWitness(material.Evidence.ID, material.Witness.LeafIndex, material.Witness.Siblings)
	if wantWitness.ID != material.Witness.ID || len(material.Witness.Siblings) != int(block.MerkleDepth) {
		return false, ErrEvidenceBinding
	}
	var existingWitness []byte
	err = tx.QueryRowContext(ctx, `SELECT witness_id FROM membership_witnesses WHERE evidence_id=?`, material.Evidence.ID[:]).Scan(&existingWitness)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO membership_witnesses(witness_id,evidence_id,leaf_index,created_at) VALUES(?,?,?,?)`, material.Witness.ID[:], material.Evidence.ID[:], int64(material.Witness.LeafIndex), toUnix(now)); err != nil {
			return false, err
		}
		for index, sibling := range material.Witness.Siblings {
			if _, err := tx.ExecContext(ctx, `INSERT INTO membership_witness_siblings(witness_id,sibling_index,sibling_hash) VALUES(?,?,?)`, material.Witness.ID[:], index, sibling[:]); err != nil {
				return false, err
			}
		}
		changed = true
	} else if err != nil || !equalBytes(existingWitness, material.Witness.ID[:]) {
		return false, ErrGraphConflict
	}
	edgeInserted, err := mergeTrustEdge(ctx, tx, material.Edge)
	if err != nil {
		return false, err
	}
	return changed || edgeInserted, nil
}

func verifyActiveNodeContent(ctx context.Context, tx *sql.Tx, node trustview.TrustNode) error {
	persisted, err := loadActiveTrustNode(ctx, tx, node.ID)
	if err != nil {
		return err
	}
	if persisted != node {
		return ErrGraphConflict
	}
	return nil
}

func verifyDependencyIndexedLog(ctx context.Context, tx *sql.Tx, block indexer.ConfirmedBlock, material indexer.DependencyMaterialization) error {
	var topicsRaw, data []byte
	if err := tx.QueryRowContext(ctx, `SELECT topics,data FROM indexed_gateway_logs WHERE chain_id=? AND block_hash=? AND tx_hash=? AND tx_index=? AND log_index=? AND event_topic=?`, block.ChainID[:], block.Hash[:], material.Evidence.Locator.TxHash[:], int64(material.Evidence.Locator.TxIndex), int64(material.Evidence.Locator.LogIndex), chainabi.DependencyRecordedTopic[:]).Scan(&topicsRaw, &data); err != nil {
		return ErrEvidenceBinding
	}
	if len(topicsRaw)%32 != 0 {
		return ErrEvidenceBinding
	}
	topics := make([]common.Hash, len(topicsRaw)/32)
	for index := range topics {
		copy(topics[index][:], topicsRaw[index*32:(index+1)*32])
	}
	parsed, err := chainabi.ParseDependencyRecorded(types.Log{Topics: topics, Data: data})
	if err != nil || parsed.RequestID != material.Dependency.RequestID || parsed.DependencyKey != material.Dependency.DependencyKey || parsed.SourceChainID != material.Dependency.SourceChainID || parsed.SourceHeight != material.Dependency.SourceHeight || parsed.SourceBlockHash != material.Dependency.SourceBlockHash || parsed.SourceTrustRoot != material.Dependency.SourceTrustRoot.Hash || parsed.LeafIndex != material.Dependency.LeafIndex {
		return ErrEvidenceBinding
	}
	return nil
}
