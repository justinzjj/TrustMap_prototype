package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

type GossipInbox interface {
	Receive(context.Context, DependencyEvidenceEnvelope, peer.ID) (evidence.Record, bool, error)
	RejectProvenance(context.Context, IngressRejection) (bool, error)
}

type GossipOutbox interface {
	PendingEnvelopes(context.Context, int, time.Time) ([]DependencyEvidenceEnvelope, error)
	MarkPublished(context.Context, common.Hash) error
	MarkRetryable(context.Context, common.Hash, string, time.Time) error
}

type GossipConfig struct {
	PublishInterval time.Duration
	PublishTimeout  time.Duration
	RetryBackoff    time.Duration
	BatchSize       int
	PublishReady    func() bool
}

type DependencyEvidenceGossip struct {
	host         host.Host
	topic        *pubsub.Topic
	subscription *pubsub.Subscription
	inbox        GossipInbox
	outbox       GossipOutbox
	config       GossipConfig
}

func NewDependencyEvidenceGossip(ctx context.Context, h host.Host, inbox GossipInbox, outbox GossipOutbox, config GossipConfig) (*DependencyEvidenceGossip, error) {
	if h == nil || (inbox == nil && outbox == nil) {
		return nil, errors.New("dependency evidence gossip requires host and inbox or outbox")
	}
	if config.PublishInterval <= 0 {
		config.PublishInterval = 250 * time.Millisecond
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = time.Second
	}
	if config.PublishTimeout <= 0 {
		config.PublishTimeout = 2 * time.Second
	}
	if config.BatchSize == 0 {
		config.BatchSize = 64
	}
	if config.BatchSize < 1 || config.BatchSize > 1000 {
		return nil, errors.New("invalid dependency evidence gossip batch size")
	}
	ps, err := pubsub.NewGossipSub(ctx, h, pubsub.WithMessageSigning(true), pubsub.WithStrictSignatureVerification(true), pubsub.WithMessageIdFn(dependencyEnvelopeMessageID))
	if err != nil {
		return nil, fmt.Errorf("create dependency evidence GossipSub: %w", err)
	}
	topic, err := ps.Join(DependencyEvidenceTopic)
	if err != nil {
		return nil, fmt.Errorf("join dependency evidence topic: %w", err)
	}
	gossip := &DependencyEvidenceGossip{host: h, topic: topic, inbox: inbox, outbox: outbox, config: config}
	if inbox != nil {
		sub, err := topic.Subscribe()
		if err != nil {
			_ = topic.Close()
			return nil, fmt.Errorf("subscribe dependency evidence topic: %w", err)
		}
		gossip.subscription = sub
	}
	return gossip, nil
}

func dependencyEnvelopeMessageID(message *pb.Message) string {
	if message != nil {
		senderBytes := message.GetFrom()
		if envelope, err := ParseDependencyEvidenceEnvelope(message.GetData()); err == nil {
			if sender, senderErr := peer.IDFromBytes(senderBytes); senderErr == nil && sender == envelope.OriginPeer {
				return string(envelope.MessageID[:])
			}
		}
		hash := sha256.New()
		_, _ = hash.Write([]byte("TrustMap/DependencyEvidenceEnvelope/FallbackMessageID/v1\x00"))
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(senderBytes)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(senderBytes)
		_, _ = hash.Write(message.GetData())
		return string(hash.Sum(nil))
	}
	return ""
}

func (gossip *DependencyEvidenceGossip) Run(ctx context.Context) error {
	if gossip == nil || gossip.topic == nil {
		return errors.New("dependency evidence gossip is not initialized")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	receiveErrors := make(chan error, 1)
	if gossip.subscription != nil {
		go func() { receiveErrors <- gossip.receive(runCtx) }()
	} else {
		receiveErrors = nil
	}
	ticker := time.NewTicker(gossip.config.PublishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErrors:
			return err
		case now := <-ticker.C:
			if gossip.outbox != nil {
				if gossip.config.PublishReady != nil && !gossip.config.PublishReady() {
					continue
				}
				if err := gossip.publishPending(runCtx, now); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("process dependency evidence outbox: %w", err)
				}
			}
		}
	}
}

func (gossip *DependencyEvidenceGossip) receive(ctx context.Context) error {
	for {
		message, err := gossip.subscription.Next(ctx)
		if err != nil {
			return err
		}
		envelope, err := ParseDependencyEvidenceEnvelope(message.GetData())
		if err != nil {
			continue
		}
		author := message.GetFrom()
		if err := envelope.ValidateFrom(author); err != nil {
			rejection, rejectionErr := NewIngressRejection(author, envelope, message.GetData(), err.Error(), time.Now().UTC())
			if rejectionErr != nil {
				return fmt.Errorf("construct dependency evidence provenance rejection: %w", rejectionErr)
			}
			if _, rejectionErr := gossip.inbox.RejectProvenance(ctx, rejection); rejectionErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("persist dependency evidence provenance rejection: %w", rejectionErr)
			}
			continue
		}
		if _, _, err := gossip.inbox.Receive(ctx, envelope, author); err != nil && ctx.Err() != nil {
			return ctx.Err()
		} else if err != nil {
			return fmt.Errorf("persist dependency evidence inbox: %w", err)
		}
	}
}

func (gossip *DependencyEvidenceGossip) publishPending(ctx context.Context, now time.Time) error {
	if gossip.config.PublishReady != nil && !gossip.config.PublishReady() {
		return nil
	}
	items, err := gossip.outbox.PendingEnvelopes(ctx, gossip.config.BatchSize, now)
	if err != nil {
		return err
	}
	for _, envelope := range items {
		if gossip.config.PublishReady != nil && !gossip.config.PublishReady() {
			return nil
		}
		encoded, err := envelope.MarshalBinary()
		if err == nil {
			publishCtx, cancel := context.WithTimeout(ctx, gossip.config.PublishTimeout)
			err = gossip.topic.Publish(publishCtx, encoded, pubsub.WithReadiness(gossip.routerReady))
			cancel()
		}
		if err != nil {
			if markErr := gossip.outbox.MarkRetryable(ctx, envelope.MessageID, err.Error(), now.Add(gossip.config.RetryBackoff)); markErr != nil {
				return markErr
			}
			continue
		}
		if err := gossip.outbox.MarkPublished(ctx, envelope.MessageID); err != nil {
			return err
		}
	}
	return nil
}

func (gossip *DependencyEvidenceGossip) routerReady(router pubsub.PubSubRouter, topic string) (bool, error) {
	if gossip.config.PublishReady != nil && !gossip.config.PublishReady() {
		return false, nil
	}
	return router.EnoughPeers(topic, 1), nil
}

func (gossip *DependencyEvidenceGossip) Close() error {
	if gossip == nil {
		return nil
	}
	if gossip.subscription != nil {
		gossip.subscription.Cancel()
	}
	if gossip.topic != nil {
		return gossip.topic.Close()
	}
	return nil
}
