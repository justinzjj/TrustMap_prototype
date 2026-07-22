package p2p_test

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	libp2p "github.com/libp2p/go-libp2p"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

type testInbox struct {
	received chan tmp2p.DependencyEvidenceEnvelope
	err      error
}

func (inbox *testInbox) Receive(_ context.Context, envelope tmp2p.DependencyEvidenceEnvelope, sender peer.ID) (evidence.Record, bool, error) {
	if inbox.err != nil {
		return evidence.Record{}, false, inbox.err
	}
	if sender != envelope.OriginPeer {
		return evidence.Record{}, false, errors.New("origin mismatch")
	}
	select {
	case inbox.received <- envelope:
	default:
	}
	id, _ := evidence.ComputeID(envelope.Locator())
	return evidence.Record{ID: id, Locator: envelope.Locator(), State: evidence.Candidate}, true, nil
}

func (inbox *testInbox) RejectProvenance(context.Context, tmp2p.IngressRejection) (bool, error) {
	if inbox.err != nil {
		return false, inbox.err
	}
	return true, nil
}

func TestGossipFailsClosedWhenDurableInboxWriteFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := newLoopbackHost(t)
	subscriber := newLoopbackHost(t)
	defer publisher.Close()
	defer subscriber.Close()
	if err := subscriber.Connect(ctx, peer.AddrInfo{ID: publisher.ID(), Addrs: publisher.Addrs()}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("inbox disk failure")
	pub, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, &testInbox{}, &testOutbox{envelope: gossipEnvelope(t, publisher.ID())}, tmp2p.GossipConfig{PublishInterval: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := tmp2p.NewDependencyEvidenceGossip(ctx, subscriber, &testInbox{received: make(chan tmp2p.DependencyEvidenceEnvelope), err: injected}, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	defer sub.Close()
	go pub.Run(ctx)
	done := make(chan error, 1)
	go func() { done <- sub.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, injected) {
			t.Fatalf("Run() error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inbox failure did not stop gossip")
	}
}

type testOutbox struct {
	mu            sync.Mutex
	envelope      tmp2p.DependencyEvidenceEnvelope
	published     bool
	nextAttemptAt time.Time
	retries       int
}

type failingOutbox struct {
	err error
}

func (outbox *failingOutbox) PendingEnvelopes(context.Context, int, time.Time) ([]tmp2p.DependencyEvidenceEnvelope, error) {
	return nil, outbox.err
}

func (outbox *failingOutbox) MarkPublished(context.Context, common.Hash, time.Time) error {
	return nil
}

func (outbox *failingOutbox) MarkRetryable(context.Context, common.Hash, string, time.Time) error {
	return nil
}

func TestGossipFailsClosedWhenDurableOutboxReadFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := newLoopbackHost(t)
	defer publisher.Close()
	injected := errors.New("outbox disk failure")
	gossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, nil, &failingOutbox{err: injected}, tmp2p.GossipConfig{PublishInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer gossip.Close()
	done := make(chan error, 1)
	go func() { done <- gossip.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, injected) {
			t.Fatalf("Run() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("outbox failure did not stop gossip")
	}
}

func (outbox *testOutbox) PendingEnvelopes(_ context.Context, _ int, now time.Time) ([]tmp2p.DependencyEvidenceEnvelope, error) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if outbox.published && now.Before(outbox.nextAttemptAt) {
		return nil, nil
	}
	return []tmp2p.DependencyEvidenceEnvelope{outbox.envelope}, nil
}
func (outbox *testOutbox) MarkPublished(_ context.Context, id common.Hash, next time.Time) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if id != outbox.envelope.MessageID {
		return errors.New("wrong message")
	}
	outbox.published = true
	outbox.nextAttemptAt = next
	return nil
}
func (outbox *testOutbox) MarkRetryable(context.Context, common.Hash, string, time.Time) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	outbox.retries++
	return nil
}

func (outbox *testOutbox) snapshot() (bool, int) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	return outbox.published, outbox.retries
}

func (outbox *testOutbox) publishedRepair() (bool, time.Time) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	return outbox.published, outbox.nextAttemptAt
}

func (outbox *testOutbox) reset(envelope tmp2p.DependencyEvidenceEnvelope) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	outbox.envelope, outbox.published, outbox.retries = envelope, false, 0
}

func TestGossipSchedulesRepublishFromCompletedLocalPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := newLoopbackHost(t)
	receiver := newLoopbackHost(t)
	defer publisher.Close()
	defer receiver.Close()
	outbox := &testOutbox{envelope: gossipEnvelope(t, publisher.ID())}
	pubGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, &testInbox{}, outbox, tmp2p.GossipConfig{PublishInterval: 10 * time.Millisecond, PublishTimeout: 2 * time.Second, RepublishInterval: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	subGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, receiver, &testInbox{received: make(chan tmp2p.DependencyEvidenceEnvelope, 1)}, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = pubGossip.Close()
		_ = subGossip.Close()
	}()
	done := make(chan error, 2)
	go func() { done <- pubGossip.Run(ctx) }()
	go func() { done <- subGossip.Run(ctx) }()
	time.Sleep(350 * time.Millisecond)
	if err := publisher.Connect(ctx, peer.AddrInfo{ID: receiver.ID(), Addrs: receiver.Addrs()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		published, repairAt := outbox.publishedRepair()
		if published {
			if !repairAt.After(time.Now()) {
				t.Fatalf("republish scheduled from stale poll time: repair_at=%s now=%s", repairAt, time.Now())
			}
			cancel()
			for range 2 {
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("worker shutdown=%v", err)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("publisher never scheduled durable repair")
}

func TestGossipPeriodicallyRedeliversPublishedEnvelopeWithoutBusyLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := newLoopbackHost(t)
	receiver := newLoopbackHost(t)
	defer publisher.Close()
	defer receiver.Close()
	if err := publisher.Connect(ctx, peer.AddrInfo{ID: receiver.ID(), Addrs: receiver.Addrs()}); err != nil {
		t.Fatal(err)
	}
	envelope := gossipEnvelope(t, publisher.ID())
	outbox := &testOutbox{envelope: envelope}
	inbox := &testInbox{received: make(chan tmp2p.DependencyEvidenceEnvelope, 3)}
	config := tmp2p.GossipConfig{PublishInterval: 10 * time.Millisecond, PublishTimeout: time.Second, RepublishInterval: 250 * time.Millisecond}
	pubGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, &testInbox{}, outbox, config)
	if err != nil {
		t.Fatal(err)
	}
	subGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, receiver, inbox, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = pubGossip.Close()
		_ = subGossip.Close()
	}()
	done := make(chan error, 2)
	go func() { done <- pubGossip.Run(ctx) }()
	go func() { done <- subGossip.Run(ctx) }()
	select {
	case got := <-inbox.received:
		if got.MessageID != envelope.MessageID {
			t.Fatalf("first delivery=%x", got.MessageID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first delivery timed out")
	}
	select {
	case <-inbox.received:
		t.Fatal("published envelope redelivered before configured repair interval")
	case <-time.After(125 * time.Millisecond):
	}
	select {
	case got := <-inbox.received:
		if got.MessageID != envelope.MessageID {
			t.Fatalf("repair delivery=%x", got.MessageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("published envelope was not actually redelivered after repair interval")
	}
	cancel()
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("worker shutdown=%v", err)
		}
	}
}

func TestGossipRouterReadinessTimeoutKeepsPendingAndWorkerLive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := newLoopbackHost(t)
	defer publisher.Close()
	outbox := &testOutbox{envelope: gossipEnvelope(t, publisher.ID())}
	gossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, &testInbox{}, outbox, tmp2p.GossipConfig{PublishInterval: 10 * time.Millisecond, PublishTimeout: 50 * time.Millisecond, RetryBackoff: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = gossip.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- gossip.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	published, retries := outbox.snapshot()
	if published || retries == 0 {
		t.Fatalf("router-unready outbox published=%t retries=%d", published, retries)
	}
	select {
	case err := <-done:
		t.Fatalf("router readiness timeout stopped worker: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker shutdown=%v", err)
	}
}

func TestGossipReadinessLossKeepsNewPendingUntilPeerReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := newLoopbackHost(t)
	receiver := newLoopbackHost(t)
	defer publisher.Close()
	defer receiver.Close()
	if err := publisher.Connect(ctx, peer.AddrInfo{ID: receiver.ID(), Addrs: receiver.Addrs()}); err != nil {
		t.Fatal(err)
	}
	first := gossipEnvelope(t, publisher.ID())
	outbox := &testOutbox{envelope: first}
	inbox := &testInbox{received: make(chan tmp2p.DependencyEvidenceEnvelope, 2)}
	var staticReady atomic.Bool
	staticReady.Store(true)
	pubGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, &testInbox{}, outbox, tmp2p.GossipConfig{PublishInterval: 25 * time.Millisecond, PublishTimeout: 250 * time.Millisecond, RetryBackoff: 25 * time.Millisecond, PublishReady: staticReady.Load})
	if err != nil {
		t.Fatal(err)
	}
	subGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, receiver, inbox, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = pubGossip.Close()
		_ = subGossip.Close()
	}()
	runErrors := make(chan error, 2)
	go func() { runErrors <- pubGossip.Run(ctx) }()
	go func() { runErrors <- subGossip.Run(ctx) }()
	select {
	case got := <-inbox.received:
		if got.MessageID != first.MessageID {
			t.Fatalf("first message=%x", got.MessageID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first message was not delivered")
	}
	staticReady.Store(false)
	if err := publisher.Network().ClosePeer(receiver.ID()); err != nil {
		t.Fatal(err)
	}
	second, err := tmp2p.NewDependencyEvidenceEnvelope(publisher.ID(), first.ObservedAt.Add(time.Second), first.Locator())
	if err != nil {
		t.Fatal(err)
	}
	outbox.reset(second)
	time.Sleep(300 * time.Millisecond)
	if published, _ := outbox.snapshot(); published {
		t.Fatal("new pending message was published after static readiness was lost")
	}
	if err := publisher.Connect(ctx, peer.AddrInfo{ID: receiver.ID(), Addrs: receiver.Addrs()}); err != nil {
		t.Fatal(err)
	}
	staticReady.Store(true)
	select {
	case got := <-inbox.received:
		if got.MessageID != second.MessageID {
			t.Fatalf("second message=%x want=%x", got.MessageID, second.MessageID)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("pending message was not delivered after static peer reconnected")
	}
	cancel()
	for range 2 {
		if err := <-runErrors; !errors.Is(err, context.Canceled) {
			t.Fatalf("worker shutdown=%v", err)
		}
	}
}

func TestTwoRealLibp2pHostsGossipDependencyEvidenceOnLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := newLoopbackHost(t)
	subscriber := newLoopbackHost(t)
	defer publisher.Close()
	defer subscriber.Close()
	if err := subscriber.Connect(ctx, peer.AddrInfo{ID: publisher.ID(), Addrs: publisher.Addrs()}); err != nil {
		t.Fatal(err)
	}
	envelope := gossipEnvelope(t, publisher.ID())
	outbox := &testOutbox{envelope: envelope}
	inbox := &testInbox{received: make(chan tmp2p.DependencyEvidenceEnvelope, 1)}
	pubGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, &testInbox{}, outbox, tmp2p.GossipConfig{PublishInterval: 25 * time.Millisecond, RetryBackoff: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	subGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, subscriber, inbox, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer pubGossip.Close()
	defer subGossip.Close()
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- pubGossip.Run(ctx) }()
	go func() { errorsCh <- subGossip.Run(ctx) }()
	select {
	case got := <-inbox.received:
		if got.MessageID != envelope.MessageID {
			t.Fatalf("received %x want %x", got.MessageID, envelope.MessageID)
		}
	case err := <-errorsCh:
		t.Fatalf("gossip stopped early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for GossipSub delivery")
	}
	cancel()
	for range 2 {
		select {
		case err := <-errorsCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() shutdown=%v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("gossip goroutine did not stop")
		}
	}
}

func TestCopiedEnvelopeIsDurablyRejectedWithoutPreemptingGenuineSenderCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	genuineHost := newLoopbackHost(t)
	copyHost := newLoopbackHost(t)
	receiverHost := newLoopbackHost(t)
	defer genuineHost.Close()
	defer copyHost.Close()
	defer receiverHost.Close()
	for _, source := range []host.Host{genuineHost, copyHost} {
		if err := source.Connect(ctx, peer.AddrInfo{ID: receiverHost.ID(), Addrs: receiverHost.Addrs()}); err != nil {
			t.Fatal(err)
		}
	}
	database, err := store.Open(filepath.Join(t.TempDir(), "ingress.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	inbox := store.NewEvidenceInboxRepository(database)
	envelope := gossipEnvelope(t, genuineHost.ID())
	encoded, _ := envelope.MarshalBinary()
	rejectionProbe, _ := tmp2p.NewIngressRejection(copyHost.ID(), envelope, encoded, "probe", time.Now().UTC())
	receiver, err := tmp2p.NewDependencyEvidenceGossip(ctx, receiverHost, inbox, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	copier := newRawDependencyGossip(t, ctx, copyHost)
	defer receiver.Close()
	defer copier.close()
	runErrors := make(chan error, 1)
	go func() { runErrors <- receiver.Run(ctx) }()
	publishRawDependency(t, copier, encoded)
	rejectionDeadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(rejectionDeadline) {
		if _, err := inbox.LoadIngressRejection(ctx, rejectionProbe.RejectionID); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	rejection, err := inbox.LoadIngressRejection(ctx, rejectionProbe.RejectionID)
	if err != nil || rejection.ActualOrigin != copyHost.ID() || rejection.ClaimedOrigin != genuineHost.ID() {
		t.Fatalf("copied ingress rejection=%+v err=%v", rejection, err)
	}
	if pending, err := inbox.Pending(ctx, 10, time.Now().Add(time.Hour)); err != nil || len(pending) != 0 {
		t.Fatalf("copied envelope polluted candidate inbox: pending=%+v err=%v", pending, err)
	}
	genuine := newRawDependencyGossip(t, ctx, genuineHost)
	defer genuine.close()
	publishRawDependency(t, genuine, encoded)
	candidateDeadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(candidateDeadline) {
		pending, pendingErr := inbox.Pending(ctx, 10, time.Now().Add(time.Hour))
		if pendingErr == nil && len(pending) == 1 {
			if pending[0].Envelope.MessageID != envelope.MessageID || pending[0].OriginPeer != genuineHost.ID() {
				t.Fatalf("genuine candidate=%+v", pending[0])
			}
			cancel()
			if err := <-runErrors; !errors.Is(err, context.Canceled) {
				t.Fatalf("gossip shutdown=%v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("genuine sender message did not enter candidate inbox after copied rejection")
}

func newLoopbackHost(t *testing.T) host.Host {
	t.Helper()
	key, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(libp2p.Identity(key), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func gossipEnvelope(t *testing.T, origin peer.ID) tmp2p.DependencyEvidenceEnvelope {
	t.Helper()
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(99)
	locator := evidence.Locator{ChainID: chainID, ContractAddress: common.HexToAddress("0x1111111111111111111111111111111111111111"), BlockNumber: height, BlockHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), TxHash: common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), PayloadDigest: common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")}
	envelope, err := tmp2p.NewDependencyEvidenceEnvelope(origin, time.Now().UTC(), locator)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
