package p2p_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
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
	pub, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, nil, &testOutbox{envelope: gossipEnvelope(t, publisher.ID())}, tmp2p.GossipConfig{PublishInterval: 25 * time.Millisecond})
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
	mu        sync.Mutex
	envelope  tmp2p.DependencyEvidenceEnvelope
	published bool
}

type failingOutbox struct {
	err error
}

func (outbox *failingOutbox) PendingEnvelopes(context.Context, int, time.Time) ([]tmp2p.DependencyEvidenceEnvelope, error) {
	return nil, outbox.err
}

func (outbox *failingOutbox) MarkPublished(context.Context, common.Hash) error {
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

func (outbox *testOutbox) PendingEnvelopes(context.Context, int, time.Time) ([]tmp2p.DependencyEvidenceEnvelope, error) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if outbox.published {
		return nil, nil
	}
	return []tmp2p.DependencyEvidenceEnvelope{outbox.envelope}, nil
}
func (outbox *testOutbox) MarkPublished(_ context.Context, id common.Hash) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if id != outbox.envelope.MessageID {
		return errors.New("wrong message")
	}
	outbox.published = true
	return nil
}
func (outbox *testOutbox) MarkRetryable(context.Context, common.Hash, string, time.Time) error {
	return nil
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
	pubGossip, err := tmp2p.NewDependencyEvidenceGossip(ctx, publisher, nil, outbox, tmp2p.GossipConfig{PublishInterval: 25 * time.Millisecond, RetryBackoff: 25 * time.Millisecond})
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
