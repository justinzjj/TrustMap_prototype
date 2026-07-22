package store

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestEvidenceInboxPersistsCandidateDeduplicatesAndRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mailbox.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox := NewEvidenceInboxRepository(db)
	envelope := testStoreEnvelope(t)
	record, inserted, err := inbox.Receive(ctx, envelope, envelope.OriginPeer)
	if err != nil || !inserted || record.State != evidence.Candidate {
		t.Fatalf("Receive() = %+v, %v, %v", record, inserted, err)
	}
	if _, inserted, err := inbox.Receive(ctx, envelope, envelope.OriginPeer); err != nil || inserted {
		t.Fatalf("duplicate Receive() inserted=%v err=%v", inserted, err)
	}
	if countTable(t, db, "trust_edges") != 0 || countTable(t, db, "trust_nodes") != 0 {
		t.Fatal("inbox message created TrustView content")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pending, err := NewEvidenceInboxRepository(db).Pending(ctx, 10, time.Now().Add(time.Hour))
	if err != nil || len(pending) != 1 || pending[0].Envelope.MessageID != envelope.MessageID || pending[0].OriginPeer != envelope.OriginPeer {
		t.Fatalf("Pending() = %+v, %v", pending, err)
	}
}

func TestEvidenceInboxRecordsPermanentInvalidAndRetryableFailures(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inbox := NewEvidenceInboxRepository(db)
	envelope := testStoreEnvelope(t)
	if _, _, err := inbox.Receive(ctx, envelope, envelope.OriginPeer); err != nil {
		t.Fatal(err)
	}
	if err := inbox.MarkRetryable(ctx, envelope.MessageID, "rpc unavailable", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err := inbox.Pending(ctx, 10, time.Now())
	if err != nil || len(items) != 0 {
		t.Fatalf("early Pending() = %+v, %v", items, err)
	}
	if err := inbox.MarkInvalid(ctx, envelope.MessageID, "wrong canonical block"); err != nil {
		t.Fatal(err)
	}
	loaded, err := inbox.Load(ctx, envelope.MessageID)
	if err != nil || loaded.State != InboxInvalid || loaded.InvalidReason != "wrong canonical block" || loaded.OriginPeer != envelope.OriginPeer {
		t.Fatalf("Load() = %+v, %v", loaded, err)
	}
	record, err := NewEvidenceRepository(db).Load(ctx, loaded.EvidenceID)
	if err != nil || record.State != evidence.Invalid || record.InvalidReason != loaded.InvalidReason {
		t.Fatalf("evidence = %+v, %v", record, err)
	}
}

func TestEvidenceOutboxRequiresActiveDurableEvidenceAndRecoversAtLeastOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outbox.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	envelope := testStoreEnvelope(t)
	locator := envelope.Locator()
	id, _ := evidence.ComputeID(locator)
	record := evidence.Record{ID: id, Locator: locator, State: evidence.Candidate}
	if _, _, err := NewEvidenceRepository(db).Observe(ctx, record); err != nil {
		t.Fatal(err)
	}
	outbox := NewEvidenceOutboxRepository(db)
	if _, err := outbox.Enqueue(ctx, envelope); !errors.Is(err, ErrInactiveEvidence) {
		t.Fatalf("candidate Enqueue() error = %v", err)
	}
	activateEvidence(t, NewEvidenceRepository(db), id)
	if inserted, err := outbox.Enqueue(ctx, envelope); err != nil || !inserted {
		t.Fatalf("Enqueue() = %v, %v", inserted, err)
	}
	if inserted, err := outbox.Enqueue(ctx, envelope); err != nil || inserted {
		t.Fatalf("duplicate Enqueue() = %v, %v", inserted, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	outbox = NewEvidenceOutboxRepository(db)
	pending, err := outbox.Pending(ctx, 10, time.Now())
	if err != nil || len(pending) != 1 || pending[0].Envelope.MessageID != envelope.MessageID {
		t.Fatalf("Pending() = %+v, %v", pending, err)
	}
	if err := outbox.MarkPublished(ctx, envelope.MessageID); err != nil {
		t.Fatal(err)
	}
	pending, err = outbox.Pending(ctx, 10, time.Now().Add(time.Hour))
	if err != nil || len(pending) != 0 {
		t.Fatalf("published Pending() = %+v, %v", pending, err)
	}
}

func TestRemoteDependencyRepositoryActivatesExactValidatedEdgeAtomically(t *testing.T) {
	ctx := context.Background()
	fixture := newDependencyApplyFixture(t)
	material := fixture.block.Dependencies[0]
	envelope, err := tmp2p.NewDependencyEvidenceEnvelope(tmp2pTestEnvelope(t).OriginPeer, time.Now().UTC(), material.Evidence.Locator)
	if err != nil {
		t.Fatal(err)
	}
	inbox := NewEvidenceInboxRepository(fixture.db)
	if _, inserted, err := inbox.Receive(ctx, envelope, envelope.OriginPeer); err != nil || !inserted {
		t.Fatalf("Receive()=%v,%v", inserted, err)
	}
	validation := evidence.RemoteDependencyValidation{Record: material.Evidence, Dependency: chainabi.DependencyRecorded{DependencyKey: material.Dependency.DependencyKey, RequestID: material.Dependency.RequestID, SourceChainID: material.Dependency.SourceChainID, SourceHeight: material.Dependency.SourceHeight, SourceBlockHash: material.Dependency.SourceBlockHash, SourceTrustRoot: material.Dependency.SourceTrustRoot.Hash, LeafIndex: material.Dependency.LeafIndex}, HomeTrustRoot: material.From.Root.Hash, WitnessSiblings: material.Witness.Siblings, SourceObservation: evidence.ValidatedTrustRootObservation{ChainID: material.To.Key.ChainID, Height: material.To.Key.Height, BlockHash: material.To.Key.BlockHash, TrustRoot: material.To.Root.Hash, EvidenceID: material.To.EvidenceID}, HomeObservation: evidence.ValidatedTrustRootObservation{ChainID: material.From.Key.ChainID, Height: material.From.Key.Height, BlockHash: material.From.Key.BlockHash, TrustRoot: material.From.Root.Hash, EvidenceID: material.From.EvidenceID}, PathStepCostGas: material.Edge.PathStepCost}
	repository := NewRemoteDependencyRepository(fixture.db)
	changed, err := repository.Activate(ctx, envelope.MessageID, validation)
	if err != nil || !changed {
		t.Fatalf("Activate()=%v,%v", changed, err)
	}
	loaded, err := NewEvidenceRepository(fixture.db).Load(ctx, material.Evidence.ID)
	if err != nil || loaded.State != evidence.Active {
		t.Fatalf("evidence=%+v,%v", loaded, err)
	}
	if countTable(t, fixture.db, "trust_edges") != 1 {
		t.Fatal("exact remote edge was not created")
	}
	item, err := inbox.Load(ctx, envelope.MessageID)
	if err != nil || item.State != InboxValidated {
		t.Fatalf("inbox=%+v,%v", item, err)
	}
	changed, err = repository.Activate(ctx, envelope.MessageID, validation)
	if err != nil || changed {
		t.Fatalf("replay Activate()=%v,%v", changed, err)
	}
}

func TestIndexerPublishesOnlyActiveDependencyThroughAtomicOutboxHook(t *testing.T) {
	ctx := context.Background()
	fixture := newDependencyApplyFixture(t)
	origin := tmp2pTestEnvelope(t).OriginPeer
	outbox := NewEvidenceOutboxRepository(fixture.db)
	fixture.repository.ConfigureEvidenceOutbox(outbox, origin)
	injected := errors.New("rollback before commit")
	fixture.repository.beforeCommit = func() error { return injected }
	if _, err := fixture.repository.ApplyConfirmedBlock(ctx, fixture.block); !errors.Is(err, injected) {
		t.Fatalf("rollback error=%v", err)
	}
	if countTable(t, fixture.db, "evidence_outbox") != 0 {
		t.Fatal("rolled-back dependency leaked into outbox")
	}
	fixture.repository.beforeCommit = nil
	if _, err := fixture.repository.ApplyConfirmedBlock(ctx, fixture.block); err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.PendingEnvelopes(ctx, 10, time.Now().Add(time.Second))
	if err != nil || len(pending) != 1 || pending[0].OriginPeer != origin || pending[0].Locator() != fixture.block.Dependencies[0].Evidence.Locator {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func testStoreEnvelope(t *testing.T) tmp2p.DependencyEvidenceEnvelope {
	t.Helper()
	return tmp2pTestEnvelope(t)
}

func countTable(t *testing.T, db *DB, table string) int {
	t.Helper()
	var count int
	if err := db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Kept here to avoid exporting test-only helpers from the p2p package.
func tmp2pTestEnvelope(t *testing.T) tmp2p.DependencyEvidenceEnvelope {
	t.Helper()
	_, public, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := peer.IDFromPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	chainID, err := domain.NewChainID(10002)
	if err != nil {
		t.Fatal(err)
	}
	height := mustHeight(44)
	locator := evidence.Locator{ChainID: chainID, ContractAddress: common.HexToAddress("0x2222222222222222222222222222222222222222"), BlockNumber: height,
		BlockHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), TxHash: common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		TxIndex: 2, LogIndex: 3, PayloadDigest: common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")}
	envelope, err := tmp2p.NewDependencyEvidenceEnvelope(origin, time.Unix(1700000000, 0), locator)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
