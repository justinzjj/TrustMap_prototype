package p2p

import (
	"crypto/rand"
	"testing"
	"time"

	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestDependencyEnvelopeMessageIDBindsCanonicalEnvelopeToSignedSenderAndPublication(t *testing.T) {
	envelope := testEnvelope(t)
	encoded, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	copyKey, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	copyPeer, err := peer.IDFromPrivateKey(copyKey)
	if err != nil {
		t.Fatal(err)
	}
	genuine := dependencyEnvelopeMessageID(&pb.Message{From: []byte(envelope.OriginPeer), Seqno: []byte{1}, Data: encoded})
	samePublication := dependencyEnvelopeMessageID(&pb.Message{From: []byte(envelope.OriginPeer), Seqno: []byte{1}, Data: encoded})
	republished := dependencyEnvelopeMessageID(&pb.Message{From: []byte(envelope.OriginPeer), Seqno: []byte{2}, Data: encoded})
	copied := dependencyEnvelopeMessageID(&pb.Message{From: []byte(copyPeer), Seqno: []byte{1}, Data: encoded})
	if genuine != samePublication {
		t.Fatal("the same signed publication produced different GossipSub message IDs")
	}
	if republished == genuine {
		t.Fatal("periodic canonical republish was suppressed as the original GossipSub publication")
	}
	if copied == genuine {
		t.Fatal("copied envelope occupied the genuine sender's canonical message ID")
	}
}

func TestIngressRejectionDeterministicallyBindsActualAndClaimedProvenance(t *testing.T) {
	envelope := testEnvelope(t)
	encoded, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	copyKey, _, _ := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	copyPeer, _ := peer.IDFromPrivateKey(copyKey)
	receivedAt := time.Unix(1_700_000_000, 0).UTC()
	first, err := NewIngressRejection(copyPeer, envelope, encoded, "signed sender does not match claimed origin", receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewIngressRejection(copyPeer, envelope, encoded, first.Reason, receivedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.RejectionID != second.RejectionID || first.PayloadFingerprint != second.PayloadFingerprint {
		t.Fatal("rejection identity changed across replay")
	}
	if first.ActualOrigin != copyPeer || first.ClaimedOrigin != envelope.OriginPeer || first.ClaimedMessageID != envelope.MessageID || first.ReceivedAt != receivedAt {
		t.Fatalf("rejection provenance=%+v", first)
	}
}
