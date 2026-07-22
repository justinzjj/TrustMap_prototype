package store

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/executor"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestTransactionSubmissionIdentityAndPersistBeforeBroadcastRecovery(t *testing.T) {
	db := openTestDB(t)
	request, planID, snapshotID := insertExecutionPlanFixture(t, db, planner.DirectPlan)
	submission := testSubmission(t, request, planID, snapshotID, nil, 7)
	repository := NewTransactionRepository(db)

	saved, created, err := repository.SavePrepared(context.Background(), submission)
	if err != nil || !created || saved.State != executor.Prepared || string(saved.RawSignedTx) != string(submission.RawSignedTx) {
		t.Fatalf("saved=%+v created=%v err=%v", saved, created, err)
	}
	loaded, err := repository.LoadActive(context.Background(), request.ID)
	if err != nil || loaded.ID != submission.ID || loaded.Nonce != 7 || loaded.TxHash != submission.TxHash {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	recovered, err := repository.ListRecoverable(context.Background())
	if err != nil || len(recovered) != 1 || recovered[0].ID != submission.ID {
		t.Fatalf("recoverable=%+v err=%v", recovered, err)
	}
	if _, created, err := repository.SavePrepared(context.Background(), submission); err != nil || created {
		t.Fatalf("idempotent save created=%v err=%v", created, err)
	}

	otherRequest := testRequestWithNonceLastByte(t, 0xfe)
	if _, _, err := NewRequestRepository(db).Observe(context.Background(), otherRequest); err != nil {
		t.Fatal(err)
	}
	other := submission
	other.ID = executor.SubmissionID{}
	other.RequestID = otherRequest.ID
	other.Attempt = 1
	other.ID = executor.ComputeSubmissionID(other)
	if _, _, err := repository.SavePrepared(context.Background(), other); err == nil {
		t.Fatal("second active submission reused the home sender nonce")
	}
}

func TestConfirmedRequiresSuccessfulCanonicalReceiptAndExactIndexerBundle(t *testing.T) {
	db := openTestDB(t)
	request, planID, snapshotID := insertExecutionPlanFixture(t, db, planner.DirectPlan)
	submission := testSubmission(t, request, planID, snapshotID, nil, 11)
	repository := NewTransactionRepository(db)
	if _, _, err := repository.SavePrepared(context.Background(), submission); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.Transition(context.Background(), submission.ID, executor.Prepared, executor.Submitted, "rpc accepted", time.Now()); err != nil {
		t.Fatal(err)
	}
	receipt := executor.ConfirmedTransactionReceipt{
		SubmissionID: submission.ID, RequestID: request.ID, TxHash: submission.TxHash,
		BlockNumber: 20, BlockHash: common.HexToHash("0xbeef"), Status: 1, Confirmations: 2, ConfirmedAt: time.Now(),
	}
	if err := repository.Confirm(context.Background(), receipt); !errors.Is(err, executor.ErrIndexerBundlePending) {
		t.Fatalf("confirmation without indexer bundle error=%v", err)
	}
	insertIndexerBundleFixture(t, db, submission, receipt)
	if err := repository.Confirm(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load(context.Background(), submission.ID)
	if err != nil || loaded.State != executor.Confirmed {
		t.Fatalf("state=%s err=%v", loaded.State, err)
	}
	if err := repository.Confirm(context.Background(), receipt); err != nil {
		t.Fatalf("idempotent confirm: %v", err)
	}
	reverted := receipt
	reverted.SubmissionID = executor.SubmissionID(common.HexToHash("0x44"))
	reverted.Status = 0
	if err := repository.Confirm(context.Background(), reverted); err == nil {
		t.Fatal("failed receipt accepted")
	}
}

func TestSubmissionContentAddressRejectsChangedRawTransaction(t *testing.T) {
	db := openTestDB(t)
	request, planID, snapshotID := insertExecutionPlanFixture(t, db, planner.DirectPlan)
	submission := testSubmission(t, request, planID, snapshotID, nil, 1)
	submission.RawSignedTx[0] ^= 1
	if _, _, err := NewTransactionRepository(db).SavePrepared(context.Background(), submission); !errors.Is(err, executor.ErrSubmissionIDMismatch) {
		t.Fatalf("changed raw transaction error=%v", err)
	}
}

func insertExecutionPlanFixture(t *testing.T, db *DB, planType planner.PlanType) (coordinator.Request, planner.PlanID, trustview.SnapshotID) {
	t.Helper()
	request := testRequest(t)
	if _, _, err := NewRequestRepository(db).Observe(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	snapshotID := trustview.SnapshotID(common.HexToHash("0x51"))
	homeNode, targetNode := blob32(0x52), blob32(0x53)
	height, _ := domain.NewBlockHeight(1)
	if _, err := db.sql.Exec(`INSERT INTO trustview_snapshots(snapshot_id,graph_revision,home_chain_id,home_trust_root,start_node_id,target_node_id,sealed,created_at) VALUES(?,0,?,zeroblob(32),NULL,NULL,0,1)`, snapshotID[:], request.HomeChainID[:]); err != nil {
		t.Fatal(err)
	}
	for _, node := range [][]byte{homeNode, targetNode} {
		if _, err := db.sql.Exec(`INSERT INTO snapshot_nodes(snapshot_id,node_id,chain_id,block_height,block_hash,trust_root) VALUES(?,?,?,?,?,zeroblob(32))`, snapshotID[:], node, request.HomeChainID[:], height[:], node); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.Exec(`UPDATE trustview_snapshots SET start_node_id=?,target_node_id=?,sealed=1 WHERE snapshot_id=?`, homeNode, targetNode, snapshotID[:]); err != nil {
		t.Fatal(err)
	}
	planID := planner.PlanID(common.HexToHash("0x61"))
	if _, err := db.sql.Exec(`INSERT INTO plans(plan_id,request_id,snapshot_id,profile_id,profile_fingerprint,attempt,plan_type,home_node_id,target_node_id,hop_count,path_step_cost,path_cost,direct_cost,fallback_reason,created_at) VALUES(?,?,?,?,?,0,?,?,?,0,0,NULL,3000096,'',1)`, planID[:], request.ID[:], snapshotID[:], "pow-spv-3m", blob32(0x62), string(planType), homeNode, targetNode); err != nil {
		t.Fatal(err)
	}
	return request, planID, snapshotID
}

func testSubmission(t *testing.T, request coordinator.Request, planID planner.PlanID, snapshotID trustview.SnapshotID, proofID *[32]byte, nonce uint64) executor.TransactionSubmission {
	t.Helper()
	raw := []byte{0x02, 0xaa, 0xbb}
	submission := executor.TransactionSubmission{
		RequestID: request.ID, Attempt: 0, PlanID: planID, PlanType: planner.DirectPlan, SnapshotID: snapshotID,
		HomeChainID: request.HomeChainID, Gateway: request.Gateway, Sender: common.HexToAddress("0x7777777777777777777777777777777777777777"),
		Nonce: nonce, CalldataHash: common.HexToHash("0x71"), RawSignedTx: raw, TxHash: crypto.Keccak256Hash(raw),
		MaxFeePerGas: big.NewInt(3_000_000_000), MaxPriorityFeePerGas: big.NewInt(1_000_000_000), GasLimit: 9000000,
		State: executor.Prepared, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if proofID != nil {
		copyID := pathproof.PathProofID(*proofID)
		submission.ProofID = &copyID
		submission.PlanType = planner.PathPlan
	}
	submission.ID = executor.ComputeSubmissionID(submission)
	return submission
}

func insertIndexerBundleFixture(t *testing.T, db *DB, submission executor.TransactionSubmission, receipt executor.ConfirmedTransactionReceipt) {
	t.Helper()
	deployment, _ := domain.NewBlockHeight(1)
	blockNumber, _ := domain.NewBlockHeight(receipt.BlockNumber)
	now := time.Now().UnixNano()
	if _, err := db.sql.Exec(`INSERT INTO live_chains(chain_id,name,http_rpc,confirmations,deployment_manifest,home,gateway,gateway_code_hash,deployment_block,validated_at) VALUES(?,'home','http://home',2,'manifest',1,?,?,?,?)`, submission.HomeChainID[:], submission.Gateway[:], blob32(0xa1), deployment[:], now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO live_indexer_configs(chain_id,gateway,gateway_code_hash,deployment_block,merkle_depth,path_step_cost_gas,created_at) VALUES(?,?,?,?,8,30713,?)`, submission.HomeChainID[:], submission.Gateway[:], blob32(0xa1), deployment[:], now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO verification_receipts(chain_id,tx_hash,block_number,block_hash,tx_index,request_id,status) VALUES(?,?,?,?,0,?,1)`, submission.HomeChainID[:], submission.TxHash[:], blockNumber[:], receipt.BlockHash[:], submission.RequestID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO request_resolutions(request_id,chain_id,tx_hash,dependency_key,new_dependency,home_trust_root,log_index,created_at) VALUES(?,?,?,zeroblob(32),1,zeroblob(32),1,?)`, submission.RequestID[:], submission.HomeChainID[:], submission.TxHash[:], now); err != nil {
		t.Fatal(err)
	}
}
