package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	libp2p "github.com/libp2p/go-libp2p"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
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

func TestEvidenceIngressRejectionPersistsProvenanceWithoutCreatingCandidateEvidence(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inbox := NewEvidenceInboxRepository(db)
	envelope := testStoreEnvelope(t)
	encoded, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	_, public, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := peer.IDFromPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	rejection, err := tmp2p.NewIngressRejection(actual, envelope, encoded, "signed sender does not match claimed origin", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := inbox.RejectProvenance(ctx, rejection); err != nil || !inserted {
		t.Fatalf("RejectProvenance()=%v,%v", inserted, err)
	}
	replayed := rejection
	replayed.ReceivedAt = rejection.ReceivedAt.Add(time.Minute)
	if inserted, err := inbox.RejectProvenance(ctx, replayed); err != nil || inserted {
		t.Fatalf("duplicate RejectProvenance()=%v,%v", inserted, err)
	}
	loaded, err := inbox.LoadIngressRejection(ctx, rejection.RejectionID)
	if err != nil || loaded != rejection {
		t.Fatalf("LoadIngressRejection()=%+v,%v", loaded, err)
	}
	if countTable(t, db, "evidence") != 0 || countTable(t, db, "evidence_inbox") != 0 {
		t.Fatal("provenance rejection polluted canonical candidate evidence")
	}
	if _, err := db.sql.Exec("DELETE FROM evidence_ingress_rejections WHERE rejection_id=?", rejection.RejectionID[:]); err == nil {
		t.Fatal("append-only ingress rejection was deleted")
	}
}

func TestEvidenceOutboxRejectsStandaloneEnqueueEvenForRemoteActiveEvidence(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	envelope := testStoreEnvelope(t)
	locator := envelope.Locator()
	id, _ := evidence.ComputeID(locator)
	record := evidence.Record{ID: id, Locator: locator, State: evidence.Candidate}
	if _, _, err := NewEvidenceRepository(db).Observe(ctx, record); err != nil {
		t.Fatal(err)
	}
	outbox := NewEvidenceOutboxRepository(db)
	activateEvidence(t, NewEvidenceRepository(db), id)
	if inserted, err := outbox.Enqueue(ctx, envelope); err == nil || inserted {
		t.Fatalf("standalone Enqueue() = %v, %v; want sealed write path", inserted, err)
	}
	if countTable(t, db, "evidence_outbox") != 0 {
		t.Fatal("remote active evidence entered local outbox")
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
	wantMessageID := pending[0].MessageID
	var sequence int
	var databaseName, databasePath string
	if err := fixture.db.sql.QueryRow("PRAGMA database_list").Scan(&sequence, &databaseName, &databasePath); err != nil || databasePath == "" {
		t.Fatalf("database path=%q err=%v", databasePath, err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedOutbox := NewEvidenceOutboxRepository(restarted)
	restartedIndexer := NewIndexerRepository(restarted)
	restartedIndexer.ConfigureEvidenceOutbox(restartedOutbox, origin)
	if changed, err := restartedIndexer.ApplyConfirmedBlock(ctx, fixture.block); err != nil || changed {
		t.Fatalf("restart replay changed=%v err=%v", changed, err)
	}
	pending, err = restartedOutbox.PendingEnvelopes(ctx, 10, time.Now().Add(time.Hour))
	if err != nil || len(pending) != 1 || pending[0].MessageID != wantMessageID {
		t.Fatalf("restart pending=%+v err=%v", pending, err)
	}
}

func TestIndexerNeverPublishesEvidenceActivatedOutsideItsConfirmedBlockTransaction(t *testing.T) {
	ctx := context.Background()
	fixture := newDependencyApplyFixture(t)
	material := fixture.block.Dependencies[0]
	if _, _, err := NewEvidenceRepository(fixture.db).Observe(ctx, material.Evidence); err != nil {
		t.Fatal(err)
	}
	activateEvidence(t, NewEvidenceRepository(fixture.db), material.Evidence.ID)
	origin := tmp2pTestEnvelope(t).OriginPeer
	fixture.repository.ConfigureEvidenceOutbox(NewEvidenceOutboxRepository(fixture.db), origin)
	if _, err := fixture.repository.ApplyConfirmedBlock(ctx, fixture.block); err != nil {
		t.Fatal(err)
	}
	if countTable(t, fixture.db, "evidence_outbox") != 0 {
		t.Fatal("pre-activated remote evidence entered the local confirmed-indexer outbox")
	}
}

func TestRestartedPendingOutboxWaitsForLateStaticPeerAndTopicRouterBeforePublished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fixture := newDependencyApplyFixture(t)
	publisherKey, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publisherID, _ := peer.IDFromPrivateKey(publisherKey)
	fixture.repository.ConfigureEvidenceOutbox(NewEvidenceOutboxRepository(fixture.db), publisherID)
	if _, err := fixture.repository.ApplyConfirmedBlock(ctx, fixture.block); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var databaseName, databasePath string
	if err := fixture.db.sql.QueryRow("PRAGMA database_list").Scan(&sequence, &databaseName, &databasePath); err != nil || databasePath == "" {
		t.Fatalf("database path=%q err=%v", databasePath, err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	outbox := NewEvidenceOutboxRepository(restarted)
	publisherInbox := NewEvidenceInboxRepository(restarted)
	publisherHost, err := libp2p.New(libp2p.Identity(publisherKey), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer publisherHost.Close()
	receiverKey, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiverID, _ := peer.IDFromPrivateKey(receiverKey)
	receiverHost, err := libp2p.New(libp2p.Identity(receiverKey), libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer receiverHost.Close()
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	listenAddress, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port))
	if err != nil {
		t.Fatal(err)
	}
	receiverDatabase, err := Open(filepath.Join(t.TempDir(), "receiver.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer receiverDatabase.Close()
	receiverInbox := NewEvidenceInboxRepository(receiverDatabase)
	var bootstrapReady atomic.Bool
	publisher, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisherHost, publisherInbox, outbox, tmp2p.GossipConfig{PublishInterval: 25 * time.Millisecond, PublishTimeout: 250 * time.Millisecond, PublishReady: bootstrapReady.Load})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := tmp2p.NewDependencyEvidenceGossip(ctx, receiverHost, receiverInbox, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = publisher.Close()
		_ = receiver.Close()
	}()
	bootstrapper, err := tmp2p.NewStaticBootstrapper(publisherHost, []peer.AddrInfo{{ID: receiverID, Addrs: []ma.Multiaddr{listenAddress}}}, tmp2p.StaticBootstrapConfig{RetryInterval: 25 * time.Millisecond, DialTimeout: 2 * time.Second}, bootstrapReady.Store)
	if err != nil {
		t.Fatal(err)
	}
	runErrors := make(chan error, 3)
	go func() { runErrors <- publisher.Run(ctx) }()
	go func() { runErrors <- receiver.Run(ctx) }()
	go func() { runErrors <- bootstrapper.Run(ctx) }()
	time.Sleep(500 * time.Millisecond)
	pending, err := outbox.Pending(ctx, 10, time.Now().Add(time.Hour))
	if err != nil || len(pending) != 1 {
		t.Fatalf("offline pending=%+v err=%v; publisher marked without a ready peer/router", pending, err)
	}
	if err := receiverHost.Network().Listen(listenAddress); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		received, receiveErr := receiverInbox.Pending(ctx, 10, time.Now().Add(time.Hour))
		pending, pendingErr := outbox.Pending(ctx, 10, time.Now().Add(time.Hour))
		if receiveErr == nil && pendingErr == nil && len(received) == 1 && len(pending) == 0 {
			var state string
			if err := restarted.sql.QueryRow("SELECT state FROM evidence_outbox").Scan(&state); err != nil || state != "published" || !bootstrapReady.Load() {
				t.Fatalf("published state=%q ready=%t err=%v", state, bootstrapReady.Load(), err)
			}
			cancel()
			for range 3 {
				if err := <-runErrors; !errors.Is(err, context.Canceled) {
					t.Fatalf("worker shutdown=%v", err)
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	var state string
	_ = restarted.sql.QueryRow("SELECT state FROM evidence_outbox").Scan(&state)
	t.Fatalf("late static peer did not receive restarted pending outbox message: bootstrap_ready=%t connectedness=%s outbox_state=%s", bootstrapReady.Load(), publisherHost.Network().Connectedness(receiverID), state)
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
