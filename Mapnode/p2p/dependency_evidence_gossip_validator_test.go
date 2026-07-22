package p2p_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type rawDependencyGossip struct {
	topic *pubsub.Topic
	sub   *pubsub.Subscription
}

func newRawDependencyGossip(t *testing.T, ctx context.Context, h host.Host) *rawDependencyGossip {
	t.Helper()
	ps, err := pubsub.NewGossipSub(ctx, h,
		pubsub.WithMessageSigning(true),
		pubsub.WithStrictSignatureVerification(true),
		pubsub.WithMaxMessageSize(tmp2p.DependencyEvidenceMaxWireMessageSize),
		pubsub.WithGossipSubProtocols([]protocol.ID{tmp2p.DependencyEvidenceGossipProtocol}, func(feature pubsub.GossipSubFeature, _ protocol.ID) bool {
			return pubsub.GossipSubDefaultFeatures(feature, pubsub.GossipSubID_v13)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := ps.Join(tmp2p.DependencyEvidenceTopic)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := topic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	return &rawDependencyGossip{topic: topic, sub: sub}
}

func (raw *rawDependencyGossip) close() {
	raw.sub.Cancel()
	_ = raw.topic.Close()
}

func publishRawDependency(t *testing.T, raw *rawDependencyGossip, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := raw.topic.Publish(ctx, data, pubsub.WithReadiness(func(router pubsub.PubSubRouter, topic string) (bool, error) {
		return router.EnoughPeers(topic, 1), nil
	})); err != nil {
		t.Fatal(err)
	}
}

func nextRawDependency(t *testing.T, raw *rawDependencyGossip, timeout time.Duration) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	message, err := raw.sub.Next(ctx)
	if err != nil {
		return nil, err
	}
	return message.GetData(), nil
}

func TestGossipValidatorPropagatesCanonicalAndStopsOversizeAndMalformedAtRelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sourceHost := newLoopbackHost(t)
	relayHost := newLoopbackHost(t)
	observerHost := newLoopbackHost(t)
	defer sourceHost.Close()
	defer relayHost.Close()
	defer observerHost.Close()
	if err := sourceHost.Connect(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if err := observerHost.Connect(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}); err != nil {
		t.Fatal(err)
	}
	source := newRawDependencyGossip(t, ctx, sourceHost)
	observer := newRawDependencyGossip(t, ctx, observerHost)
	relay, err := tmp2p.NewDependencyEvidenceGossip(ctx, relayHost, &testInbox{}, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		source.close()
		observer.close()
		_ = relay.Close()
	}()
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run(ctx) }()
	envelope := gossipEnvelope(t, sourceHost.ID())
	canonical, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) > tmp2p.MaxDependencyEvidenceEnvelopeSize {
		t.Fatalf("canonical envelope size=%d", len(canonical))
	}
	publishRawDependency(t, source, canonical)
	if got, err := nextRawDependency(t, observer, 5*time.Second); err != nil || string(got) != string(canonical) {
		t.Fatalf("canonical propagation bytes=%d err=%v", len(got), err)
	}
	publishRawDependency(t, source, make([]byte, tmp2p.MaxDependencyEvidenceEnvelopeSize+1))
	if got, err := nextRawDependency(t, observer, 750*time.Millisecond); err == nil {
		t.Fatalf("oversize dependency evidence propagated through relay: %d bytes", len(got))
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("oversize observer error=%v", err)
	}
	publishRawDependency(t, source, []byte("malformed dependency evidence"))
	if got, err := nextRawDependency(t, observer, 750*time.Millisecond); err == nil {
		t.Fatalf("malformed dependency evidence propagated through relay: %x", got)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("malformed observer error=%v", err)
	}
	cancel()
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("relay shutdown=%v", err)
	}
}

func TestGossipValidatorPersistsSenderMismatchWithoutForwardingAndAllowsGenuineReplay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
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
	genuine := newRawDependencyGossip(t, ctx, genuineHost)
	copier := newRawDependencyGossip(t, ctx, copyHost)
	database, err := store.Open(filepath.Join(t.TempDir(), "validator.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	inbox := store.NewEvidenceInboxRepository(database)
	receiver, err := tmp2p.NewDependencyEvidenceGossip(ctx, receiverHost, inbox, nil, tmp2p.GossipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		genuine.close()
		copier.close()
		_ = receiver.Close()
	}()
	runDone := make(chan error, 1)
	go func() { runDone <- receiver.Run(ctx) }()
	envelope := gossipEnvelope(t, genuineHost.ID())
	encoded, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	rejectionProbe, err := tmp2p.NewIngressRejection(copyHost.ID(), envelope, encoded, "probe", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	publishRawDependency(t, copier, encoded)
	if got, err := nextRawDependency(t, genuine, 750*time.Millisecond); err == nil {
		t.Fatalf("sender-mismatched envelope was forwarded: %x", got)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("genuine observer error=%v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rejection, loadErr := inbox.LoadIngressRejection(ctx, rejectionProbe.RejectionID)
		if loadErr == nil {
			if rejection.ActualOrigin != copyHost.ID() || rejection.ClaimedOrigin != genuineHost.ID() || rejection.ClaimedMessageID != envelope.MessageID || rejection.PayloadFingerprint != rejectionProbe.PayloadFingerprint || rejection.Reason == "" {
				t.Fatalf("persisted rejection=%+v", rejection)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := inbox.LoadIngressRejection(ctx, rejectionProbe.RejectionID); err != nil {
		t.Fatalf("sender mismatch was not persisted by validator: %v", err)
	}
	if pending, err := inbox.Pending(ctx, 10, time.Now().Add(time.Hour)); err != nil || len(pending) != 0 {
		t.Fatalf("sender mismatch created candidate=%+v err=%v", pending, err)
	}
	publishRawDependency(t, genuine, encoded)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, pendingErr := inbox.Pending(ctx, 10, time.Now().Add(time.Hour))
		if pendingErr == nil && len(pending) == 1 && pending[0].Envelope.MessageID == envelope.MessageID && pending[0].OriginPeer == genuineHost.ID() {
			cancel()
			if err := <-runDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("receiver shutdown=%v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("genuine replay did not reach candidate inbox after copy-first rejection")
}
