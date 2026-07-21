package store

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/indexer"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

func TestIndexerRepositoryAppliesEmptyBlockAndCursorAtomicallyAndIdempotently(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	chainID, _ := domain.NewChainID(10002)
	gateway := common.HexToAddress("0x2000000000000000000000000000000000000002")
	codeHash := common.HexToHash("0x22")
	deploymentHeight, _ := domain.NewBlockHeight(5)
	live := NewLiveChainRepository(db)
	if err := live.Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: chainID, Address: gateway, CodeHash: codeHash}, deploymentHeight); err != nil {
		t.Fatal(err)
	}
	repository := NewIndexerRepository(db)
	if err := repository.Configure(ctx, IndexerConfig{ChainID: chainID, Gateway: gateway, GatewayCodeHash: codeHash, DeploymentBlock: deploymentHeight, MerkleDepth: 8, PathStepCostGas: 30713}); err != nil {
		t.Fatal(err)
	}
	cursor := reorg.NewCanonicalCursor(chainID, deploymentHeight, common.HexToHash("0x55"))
	if _, _, err := NewCanonicalCursorRepository(db).Initialize(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	source, _ := domain.NewChainID(10001)
	sourceHeight, _ := domain.NewBlockHeight(40)
	requester := common.HexToAddress("0x3000000000000000000000000000000000000003")
	nonce := coordinator.Uint256{}
	requestID, _ := evidence.ComputeGatewayRequestID(chainID, gateway, requester, big.NewInt(0), source, sourceHeight, common.HexToHash("0x4040"))
	request := coordinator.Request{ID: requestID, HomeChainID: chainID, Gateway: gateway, Requester: requester, Nonce: nonce, SourceChainID: source, SourceHeight: sourceHeight, SourceBlockHash: common.HexToHash("0x4040"), State: coordinator.Observed}
	receipt := indexer.VerificationReceiptRecord{TxHash: common.HexToHash("0x7777"), BlockNumber: 6, BlockHash: common.HexToHash("0x66"), TxIndex: 1, RequestID: requestID}
	resolutionKey, resolutionRoot := common.HexToHash("0xabc"), common.HexToHash("0xdef")
	resolvedTopics := []common.Hash{chainabi.RequestResolvedTopic, common.Hash(requestID), common.BytesToHash(requester[:]), resolutionKey}
	resolvedData := abiWords(uint32ABIWord(0), resolutionRoot[:])
	blockHash := common.HexToHash("0x66")
	block := indexer.ConfirmedBlock{ChainID: chainID, Gateway: gateway, Number: 6, Hash: blockHash, ParentHash: cursor.Hash, ExpectedCursor: cursor, MerkleDepth: 8, PathStepCostGas: 30713, Requests: []coordinator.Request{request}, Receipts: []indexer.VerificationReceiptRecord{receipt},
		Logs:        []indexer.IndexedGatewayLog{{Address: gateway, BlockNumber: 6, BlockHash: blockHash, TxHash: receipt.TxHash, TxIndex: 1, LogIndex: 2, EventTopic: chainabi.RequestResolvedTopic, ContentDigest: crypto.Keccak256Hash(appendTopicsData(resolvedTopics, resolvedData)), Topics: resolvedTopics, Data: resolvedData}},
		Resolutions: []indexer.RequestResolutionRecord{{RequestID: requestID, TxHash: receipt.TxHash, DependencyKey: resolutionKey, NewDependency: false, HomeTrustRoot: resolutionRoot, LogIndex: 2}}}
	injected := errors.New("before commit")
	repository.beforeCommit = func() error { return injected }
	if _, err := repository.ApplyConfirmedBlock(ctx, block); !errors.Is(err, injected) {
		t.Fatalf("injected apply error=%v", err)
	}
	persisted, err := NewCanonicalCursorRepository(db).Load(ctx, chainID)
	if err != nil || persisted != cursor {
		t.Fatalf("cursor after rollback=%+v err=%v", persisted, err)
	}
	repository.beforeCommit = nil
	changed, err := repository.ApplyConfirmedBlock(ctx, block)
	if err != nil || !changed {
		t.Fatalf("apply changed=%v err=%v", changed, err)
	}
	changed, err = repository.ApplyConfirmedBlock(ctx, block)
	if err != nil || changed {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	replayAppend := block
	replayAppend.Logs = append([]indexer.IndexedGatewayLog(nil), block.Logs...)
	replayAppend.Requests = append([]coordinator.Request(nil), block.Requests...)
	requester2 := common.HexToAddress("0x4000000000000000000000000000000000000004")
	nonce2 := coordinator.Uint256{}
	nonce2[31] = 1
	requestID2, _ := evidence.ComputeGatewayRequestID(chainID, gateway, requester2, big.NewInt(1), source, sourceHeight, common.HexToHash("0x4040"))
	request2 := coordinator.Request{ID: requestID2, HomeChainID: chainID, Gateway: gateway, Requester: requester2, Nonce: nonce2, SourceChainID: source, SourceHeight: sourceHeight, SourceBlockHash: common.HexToHash("0x4040"), State: coordinator.Observed}
	requestedTopics := []common.Hash{chainabi.VerificationRequestedTopic, common.Hash(requestID2), common.BytesToHash(requester2[:]), common.Hash(source)}
	requestedData := abiWords(sourceHeight[:], common.HexToHash("0x4040").Bytes(), nonce2[:])
	replayAppend.Logs = append(replayAppend.Logs, indexer.IndexedGatewayLog{Address: gateway, BlockNumber: 6, BlockHash: blockHash, TxHash: common.HexToHash("0x8888"), TxIndex: 2, LogIndex: 3, EventTopic: chainabi.VerificationRequestedTopic, ContentDigest: crypto.Keccak256Hash(appendTopicsData(requestedTopics, requestedData)), Topics: requestedTopics, Data: requestedData})
	replayAppend.Requests = append(replayAppend.Requests, request2)
	if _, err := repository.ApplyConfirmedBlock(ctx, replayAppend); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("replay appended durable facts error=%v", err)
	}
	var appended int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM requests WHERE id=?`, requestID2[:]).Scan(&appended); err != nil || appended != 0 {
		t.Fatalf("replay appended request count=%d err=%v", appended, err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM indexed_gateway_logs WHERE chain_id=? AND tx_hash=?`, chainID[:], common.HexToHash("0x8888").Bytes()).Scan(&appended); err != nil || appended != 0 {
		t.Fatalf("replay appended log count=%d err=%v", appended, err)
	}
	conflict := block
	conflict.Receipts = append([]indexer.VerificationReceiptRecord(nil), block.Receipts...)
	conflict.Receipts[0].TxIndex++
	if _, err := repository.ApplyConfirmedBlock(ctx, conflict); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("conflicting receipt replay error=%v", err)
	}
	persisted, _ = NewCanonicalCursorRepository(db).Load(ctx, chainID)
	if persisted.Hash != block.Hash || persisted.Height.BigInt().Uint64() != 6 {
		t.Fatalf("cursor=%+v", persisted)
	}
	var revision int
	if err := db.sql.QueryRow("SELECT revision FROM graph_state WHERE singleton=1").Scan(&revision); err != nil || revision != 0 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
}

func TestIndexerRepositoryRejectsDependencyLocatorBlockNumberMismatchAtomically(t *testing.T) {
	fixture := newDependencyApplyFixture(t)
	wrongHeight, _ := domain.NewBlockHeight(fixture.block.Number + 1)
	locator := fixture.block.Dependencies[0].Evidence.Locator
	locator.BlockNumber = wrongHeight
	rebindDependencyMaterialization(t, &fixture.block, locator)

	if _, err := fixture.repository.ApplyConfirmedBlock(context.Background(), fixture.block); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("locator block number mismatch error=%v", err)
	}
	assertDependencyApplyRolledBack(t, fixture)
}

func TestIndexerRepositoryRejectsDependencyLocatorReceiptTransactionMismatchAtomically(t *testing.T) {
	fixture := newDependencyApplyFixture(t)
	locator := fixture.block.Dependencies[0].Evidence.Locator
	locator.TxHash = common.HexToHash("0x8888")
	rebindDependencyMaterialization(t, &fixture.block, locator)
	for index := range fixture.block.Logs {
		if fixture.block.Logs[index].EventTopic == chainabi.DependencyRecordedTopic {
			fixture.block.Logs[index].TxHash = locator.TxHash
		}
	}

	if _, err := fixture.repository.ApplyConfirmedBlock(context.Background(), fixture.block); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("locator receipt transaction mismatch error=%v", err)
	}
	assertDependencyApplyRolledBack(t, fixture)
}

func TestIndexerRepositoryRejectsDependencyEdgeLeafMismatchAtomically(t *testing.T) {
	fixture := newDependencyApplyFixture(t)
	material := &fixture.block.Dependencies[0]
	edge, err := trustview.NewTrustEdge(material.From.ID, material.To.ID, material.Evidence.ID, material.Dependency.LeafIndex+1, &material.Witness.ID, fixture.block.PathStepCostGas)
	if err != nil {
		t.Fatal(err)
	}
	material.Edge = edge

	if _, err := fixture.repository.ApplyConfirmedBlock(context.Background(), fixture.block); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("dependency edge leaf mismatch error=%v", err)
	}
	assertDependencyApplyRolledBack(t, fixture)
}

func TestIndexerRepositoryRejectsConflictingPersistedEvidenceLocatorAtomically(t *testing.T) {
	fixture := newDependencyApplyFixture(t)
	material := fixture.block.Dependencies[0]
	conflicting := material.Evidence.Locator
	conflicting.TxIndex++
	now := time.Now().UTC().UnixNano()
	if _, err := fixture.db.sql.Exec(`INSERT INTO evidence(id,chain_id,contract_address,block_number,block_hash,tx_hash,tx_index,log_index,payload_digest,state,invalid_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'active','',?,?)`, material.Evidence.ID[:], conflicting.ChainID[:], conflicting.ContractAddress[:], conflicting.BlockNumber[:], conflicting.BlockHash[:], conflicting.TxHash[:], int64(conflicting.TxIndex), int64(conflicting.LogIndex), conflicting.PayloadDigest[:], now, now); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.repository.ApplyConfirmedBlock(context.Background(), fixture.block); !errors.Is(err, ErrEvidenceBinding) {
		t.Fatalf("conflicting persisted evidence locator error=%v", err)
	}
	assertDependencyApplyRolledBack(t, fixture)
}

func TestIndexerRepositoryAtomicallyMaterializesDependencyAndReplayDoesNotBumpRevision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	home, _ := domain.NewChainID(10002)
	source, _ := domain.NewChainID(10001)
	gateway := common.HexToAddress("0x2000000000000000000000000000000000000002")
	sourceGateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	codeHash, sourceCodeHash := common.HexToHash("0x22"), common.HexToHash("0x11")
	deployment, _ := domain.NewBlockHeight(5)
	live := NewLiveChainRepository(db)
	if err := live.Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: home, Address: gateway, CodeHash: codeHash}, deployment); err != nil {
		t.Fatal(err)
	}
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: source, Address: sourceGateway, CodeHash: sourceCodeHash}, deployment); err != nil {
		t.Fatal(err)
	}
	repository := NewIndexerRepository(db)
	if err := repository.Configure(ctx, IndexerConfig{ChainID: home, Gateway: gateway, GatewayCodeHash: codeHash, DeploymentBlock: deployment, MerkleDepth: 2, PathStepCostGas: 30713}); err != nil {
		t.Fatal(err)
	}
	cursor := reorg.NewCanonicalCursor(home, deployment, common.HexToHash("0x55"))
	if _, _, err := NewCanonicalCursorRepository(db).Initialize(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	blockHeight, _ := domain.NewBlockHeight(6)
	sourceHeight, _ := domain.NewBlockHeight(40)
	blockHash, sourceHash := common.HexToHash("0x66"), common.HexToHash("0xaa")
	sourceRoot := common.HexToHash("0xbb")
	anchorLeaf := domain.LeafHash(common.Hash{}, cursor.Hash)
	dependencyLeaf := domain.LeafHash(sourceRoot, sourceHash)
	zeros, _ := internalproof.ZeroHashes(2)
	siblings := []common.Hash{anchorLeaf, zeros[1]}
	finalRoot, _ := internalproof.RootFromWitness(dependencyLeaf, 1, siblings, 2)
	observationRepository := NewTrustRootObservationRepository(db)
	homeObservation := mustObservation(t, trustview.TrustRootObservationContent{ChainID: home, Height: blockHeight, BlockHash: blockHash, Gateway: gateway, TrustRoot: trustview.TrustRoot{Hash: finalRoot}, GatewayCodeHash: codeHash, RequiredConfirmations: 2, ConfirmedHeadHeight: mustHeight(8), ConfirmedHeadHash: common.HexToHash("0x88")})
	sourceObservation := mustObservation(t, trustview.TrustRootObservationContent{ChainID: source, Height: sourceHeight, BlockHash: sourceHash, Gateway: sourceGateway, TrustRoot: trustview.TrustRoot{Hash: sourceRoot}, GatewayCodeHash: sourceCodeHash, RequiredConfirmations: 2, ConfirmedHeadHeight: mustHeight(42), ConfirmedHeadHash: common.HexToHash("0x99")})
	_, from, _, err := observationRepository.Save(ctx, homeObservation)
	if err != nil {
		t.Fatal(err)
	}
	_, to, _, err := observationRepository.Save(ctx, sourceObservation)
	if err != nil {
		t.Fatal(err)
	}
	requester := common.HexToAddress("0x3000000000000000000000000000000000000003")
	requestID, _ := evidence.ComputeGatewayRequestID(home, gateway, requester, big.NewInt(0), source, sourceHeight, sourceHash)
	request := coordinator.Request{ID: requestID, HomeChainID: home, Gateway: gateway, Requester: requester, SourceChainID: source, SourceHeight: sourceHeight, SourceBlockHash: sourceHash, State: coordinator.Observed}
	dependency := trustview.NewVerifiedDependency(requestID, source, sourceHeight, sourceHash, trustview.TrustRoot{Hash: sourceRoot}, 1, evidence.ID{})
	txHash := common.HexToHash("0x7777")
	payload := trustview.ComputeDependencyRecordedPayloadDigest(dependency)
	locator := evidence.Locator{ChainID: home, ContractAddress: gateway, BlockNumber: blockHeight, BlockHash: blockHash, TxHash: txHash, TxIndex: 1, LogIndex: 3, PayloadDigest: payload}
	evidenceID, _ := evidence.ComputeID(locator)
	dependency.EvidenceID = evidenceID
	witness := trustview.NewMembershipWitness(evidenceID, 1, siblings)
	edge, _ := trustview.NewTrustEdge(from.ID, to.ID, evidenceID, 1, &witness.ID, 30713)
	dependencyKey := dependency.DependencyKey
	depTopics := []common.Hash{chainabi.DependencyRecordedTopic, dependencyKey, common.Hash(requestID), common.Hash(source)}
	depData := abiWords(sourceHeight[:], sourceHash[:], sourceRoot[:], uint32ABIWord(1))
	content := appendTopicsData(depTopics, depData)
	resolvedTopics := []common.Hash{chainabi.RequestResolvedTopic, common.Hash(requestID), common.BytesToHash(requester[:]), dependencyKey}
	resolvedData := abiWords(uint32ABIWord(1), finalRoot[:])
	block := indexer.ConfirmedBlock{ChainID: home, Gateway: gateway, Number: 6, Hash: blockHash, ParentHash: cursor.Hash, ExpectedCursor: cursor, MerkleDepth: 2, PathStepCostGas: 30713,
		Logs:     []indexer.IndexedGatewayLog{{Address: gateway, BlockNumber: 6, BlockHash: blockHash, TxHash: txHash, TxIndex: 1, LogIndex: 3, EventTopic: chainabi.DependencyRecordedTopic, ContentDigest: crypto.Keccak256Hash(content), Topics: depTopics, Data: depData}, {Address: gateway, BlockNumber: 6, BlockHash: blockHash, TxHash: txHash, TxIndex: 1, LogIndex: 4, EventTopic: chainabi.RequestResolvedTopic, ContentDigest: crypto.Keccak256Hash(appendTopicsData(resolvedTopics, resolvedData)), Topics: resolvedTopics, Data: resolvedData}},
		Requests: []coordinator.Request{request}, Receipts: []indexer.VerificationReceiptRecord{{TxHash: txHash, BlockNumber: 6, BlockHash: blockHash, TxIndex: 1, RequestID: requestID}},
		Resolutions:  []indexer.RequestResolutionRecord{{RequestID: requestID, TxHash: txHash, DependencyKey: dependencyKey, NewDependency: true, HomeTrustRoot: finalRoot, LogIndex: 4}},
		Dependencies: []indexer.DependencyMaterialization{{Dependency: dependency, Evidence: evidence.Record{ID: evidenceID, Locator: locator, State: evidence.Candidate}, Witness: witness, From: from, To: to, Edge: edge}}}
	if changed, err := repository.ApplyConfirmedBlock(ctx, block); err != nil || !changed {
		t.Fatalf("apply changed=%v err=%v", changed, err)
	}
	var state evidence.State
	if err := db.sql.QueryRow(`SELECT state FROM evidence WHERE id=?`, evidenceID[:]).Scan(&state); err != nil || state != evidence.Active {
		t.Fatalf("state=%s err=%v", state, err)
	}
	var revision int
	if err := db.sql.QueryRow(`SELECT revision FROM graph_state WHERE singleton=1`).Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
	if changed, err := repository.ApplyConfirmedBlock(ctx, block); err != nil || changed {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	var replayRevision int
	_ = db.sql.QueryRow(`SELECT revision FROM graph_state WHERE singleton=1`).Scan(&replayRevision)
	if replayRevision != revision {
		t.Fatalf("replay revision=%d want=%d", replayRevision, revision)
	}
}

type dependencyApplyFixture struct {
	db              *DB
	repository      *IndexerRepository
	block           indexer.ConfirmedBlock
	cursor          reorg.CanonicalCursor
	initialRevision int
	initialEdges    int
}

func newDependencyApplyFixture(t *testing.T) dependencyApplyFixture {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	home, _ := domain.NewChainID(10002)
	source, _ := domain.NewChainID(10001)
	gateway := common.HexToAddress("0x2000000000000000000000000000000000000002")
	sourceGateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	codeHash, sourceCodeHash := common.HexToHash("0x22"), common.HexToHash("0x11")
	deployment, _ := domain.NewBlockHeight(5)
	live := NewLiveChainRepository(db)
	if err := live.Sync(ctx, liveRegistry(t)); err != nil {
		t.Fatal(err)
	}
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: home, Address: gateway, CodeHash: codeHash}, deployment); err != nil {
		t.Fatal(err)
	}
	if err := live.BindDeployment(ctx, chain.GatewayDeployment{ChainID: source, Address: sourceGateway, CodeHash: sourceCodeHash}, deployment); err != nil {
		t.Fatal(err)
	}
	repository := NewIndexerRepository(db)
	if err := repository.Configure(ctx, IndexerConfig{ChainID: home, Gateway: gateway, GatewayCodeHash: codeHash, DeploymentBlock: deployment, MerkleDepth: 2, PathStepCostGas: 30713}); err != nil {
		t.Fatal(err)
	}
	cursor := reorg.NewCanonicalCursor(home, deployment, common.HexToHash("0x55"))
	if _, _, err := NewCanonicalCursorRepository(db).Initialize(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	blockHeight, _ := domain.NewBlockHeight(6)
	sourceHeight, _ := domain.NewBlockHeight(40)
	blockHash, sourceHash := common.HexToHash("0x66"), common.HexToHash("0xaa")
	sourceRoot := common.HexToHash("0xbb")
	anchorLeaf := domain.LeafHash(common.Hash{}, cursor.Hash)
	dependencyLeaf := domain.LeafHash(sourceRoot, sourceHash)
	zeros, _ := internalproof.ZeroHashes(2)
	siblings := []common.Hash{anchorLeaf, zeros[1]}
	finalRoot, _ := internalproof.RootFromWitness(dependencyLeaf, 1, siblings, 2)
	observations := NewTrustRootObservationRepository(db)
	homeObservation := mustObservation(t, trustview.TrustRootObservationContent{ChainID: home, Height: blockHeight, BlockHash: blockHash, Gateway: gateway, TrustRoot: trustview.TrustRoot{Hash: finalRoot}, GatewayCodeHash: codeHash, RequiredConfirmations: 2, ConfirmedHeadHeight: mustHeight(8), ConfirmedHeadHash: common.HexToHash("0x88")})
	sourceObservation := mustObservation(t, trustview.TrustRootObservationContent{ChainID: source, Height: sourceHeight, BlockHash: sourceHash, Gateway: sourceGateway, TrustRoot: trustview.TrustRoot{Hash: sourceRoot}, GatewayCodeHash: sourceCodeHash, RequiredConfirmations: 2, ConfirmedHeadHeight: mustHeight(42), ConfirmedHeadHash: common.HexToHash("0x99")})
	_, from, _, err := observations.Save(ctx, homeObservation)
	if err != nil {
		t.Fatal(err)
	}
	_, to, _, err := observations.Save(ctx, sourceObservation)
	if err != nil {
		t.Fatal(err)
	}
	requester := common.HexToAddress("0x3000000000000000000000000000000000000003")
	requestID, _ := evidence.ComputeGatewayRequestID(home, gateway, requester, big.NewInt(0), source, sourceHeight, sourceHash)
	request := coordinator.Request{ID: requestID, HomeChainID: home, Gateway: gateway, Requester: requester, SourceChainID: source, SourceHeight: sourceHeight, SourceBlockHash: sourceHash, State: coordinator.Observed}
	dependency := trustview.NewVerifiedDependency(requestID, source, sourceHeight, sourceHash, trustview.TrustRoot{Hash: sourceRoot}, 1, evidence.ID{})
	txHash := common.HexToHash("0x7777")
	payload := trustview.ComputeDependencyRecordedPayloadDigest(dependency)
	locator := evidence.Locator{ChainID: home, ContractAddress: gateway, BlockNumber: blockHeight, BlockHash: blockHash, TxHash: txHash, TxIndex: 1, LogIndex: 3, PayloadDigest: payload}
	evidenceID, _ := evidence.ComputeID(locator)
	dependency.EvidenceID = evidenceID
	witness := trustview.NewMembershipWitness(evidenceID, 1, siblings)
	edge, _ := trustview.NewTrustEdge(from.ID, to.ID, evidenceID, 1, &witness.ID, 30713)
	dependencyKey := dependency.DependencyKey
	depTopics := []common.Hash{chainabi.DependencyRecordedTopic, dependencyKey, common.Hash(requestID), common.Hash(source)}
	depData := abiWords(sourceHeight[:], sourceHash[:], sourceRoot[:], uint32ABIWord(1))
	resolvedTopics := []common.Hash{chainabi.RequestResolvedTopic, common.Hash(requestID), common.BytesToHash(requester[:]), dependencyKey}
	resolvedData := abiWords(uint32ABIWord(1), finalRoot[:])
	block := indexer.ConfirmedBlock{ChainID: home, Gateway: gateway, Number: 6, Hash: blockHash, ParentHash: cursor.Hash, ExpectedCursor: cursor, MerkleDepth: 2, PathStepCostGas: 30713,
		Logs: []indexer.IndexedGatewayLog{
			{Address: gateway, BlockNumber: 6, BlockHash: blockHash, TxHash: txHash, TxIndex: 1, LogIndex: 3, EventTopic: chainabi.DependencyRecordedTopic, ContentDigest: crypto.Keccak256Hash(appendTopicsData(depTopics, depData)), Topics: depTopics, Data: depData},
			{Address: gateway, BlockNumber: 6, BlockHash: blockHash, TxHash: txHash, TxIndex: 1, LogIndex: 4, EventTopic: chainabi.RequestResolvedTopic, ContentDigest: crypto.Keccak256Hash(appendTopicsData(resolvedTopics, resolvedData)), Topics: resolvedTopics, Data: resolvedData},
		},
		Requests:     []coordinator.Request{request},
		Receipts:     []indexer.VerificationReceiptRecord{{TxHash: txHash, BlockNumber: 6, BlockHash: blockHash, TxIndex: 1, RequestID: requestID}},
		Resolutions:  []indexer.RequestResolutionRecord{{RequestID: requestID, TxHash: txHash, DependencyKey: dependencyKey, NewDependency: true, HomeTrustRoot: finalRoot, LogIndex: 4}},
		Dependencies: []indexer.DependencyMaterialization{{Dependency: dependency, Evidence: evidence.Record{ID: evidenceID, Locator: locator, State: evidence.Candidate}, Witness: witness, From: from, To: to, Edge: edge}},
	}
	var revision, edges int
	if err := db.sql.QueryRow(`SELECT revision FROM graph_state WHERE singleton=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM trust_edges`).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	return dependencyApplyFixture{db: db, repository: repository, block: block, cursor: cursor, initialRevision: revision, initialEdges: edges}
}

func rebindDependencyMaterialization(t *testing.T, block *indexer.ConfirmedBlock, locator evidence.Locator) {
	t.Helper()
	material := &block.Dependencies[0]
	id, err := evidence.ComputeID(locator)
	if err != nil {
		t.Fatal(err)
	}
	material.Evidence = evidence.Record{ID: id, Locator: locator, State: evidence.Candidate}
	material.Dependency.EvidenceID = id
	material.Witness = trustview.NewMembershipWitness(id, material.Dependency.LeafIndex, material.Witness.Siblings)
	edge, err := trustview.NewTrustEdge(material.From.ID, material.To.ID, id, material.Dependency.LeafIndex, &material.Witness.ID, block.PathStepCostGas)
	if err != nil {
		t.Fatal(err)
	}
	material.Edge = edge
}

func assertDependencyApplyRolledBack(t *testing.T, fixture dependencyApplyFixture) {
	t.Helper()
	persisted, err := NewCanonicalCursorRepository(fixture.db).Load(context.Background(), fixture.block.ChainID)
	if err != nil || persisted != fixture.cursor {
		t.Fatalf("cursor after rejected dependency=%+v err=%v", persisted, err)
	}
	var revision, edges int
	if err := fixture.db.sql.QueryRow(`SELECT revision FROM graph_state WHERE singleton=1`).Scan(&revision); err != nil || revision != fixture.initialRevision {
		t.Fatalf("revision after rejected dependency=%d want=%d err=%v", revision, fixture.initialRevision, err)
	}
	if err := fixture.db.sql.QueryRow(`SELECT COUNT(*) FROM trust_edges`).Scan(&edges); err != nil || edges != fixture.initialEdges {
		t.Fatalf("edges after rejected dependency=%d want=%d err=%v", edges, fixture.initialEdges, err)
	}
}

func mustHeight(value uint64) domain.BlockHeight {
	result, _ := domain.NewBlockHeight(value)
	return result
}
func mustObservation(t *testing.T, content trustview.TrustRootObservationContent) trustview.TrustRootObservation {
	t.Helper()
	result, err := trustview.NewTrustRootObservation(content)
	if err != nil {
		t.Fatal(err)
	}
	result.ObservedAt = time.Now().UTC()
	return result
}
func uint32ABIWord(value uint32) []byte {
	result := make([]byte, 32)
	result[28] = byte(value >> 24)
	result[29] = byte(value >> 16)
	result[30] = byte(value >> 8)
	result[31] = byte(value)
	return result
}
func abiWords(values ...[]byte) []byte {
	result := make([]byte, 0, len(values)*32)
	for _, value := range values {
		word := make([]byte, 32)
		copy(word[32-len(value):], value)
		result = append(result, word...)
	}
	return result
}
func appendTopicsData(topics []common.Hash, data []byte) []byte {
	result := make([]byte, 0, len(topics)*32+len(data))
	for _, topic := range topics {
		result = append(result, topic[:]...)
	}
	return append(result, data...)
}
