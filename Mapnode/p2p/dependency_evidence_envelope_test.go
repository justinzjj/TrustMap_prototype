package p2p

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func testEnvelope(t *testing.T) DependencyEvidenceEnvelope {
	t.Helper()
	key, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(42)
	locator := evidence.Locator{
		ChainID: chainID, ContractAddress: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		BlockNumber: height, BlockHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		TxHash:  common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		TxIndex: 7, LogIndex: 9, PayloadDigest: common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
	}
	envelope, err := NewDependencyEvidenceEnvelope(origin, time.Unix(1_700_000_000, 123).UTC(), locator)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestDependencyEvidenceEnvelopeCanonicalRoundTripAndMessageID(t *testing.T) {
	envelope := testEnvelope(t)
	first, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	second, err := envelope.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("canonical serialization changed between calls")
	}
	decoded, err := ParseDependencyEvidenceEnvelope(first)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != envelope {
		t.Fatalf("round trip = %+v, want %+v", decoded, envelope)
	}
	if want, err := envelope.ComputeMessageID(); err != nil || want != envelope.MessageID {
		t.Fatalf("ComputeMessageID() = %x, %v; want %x", want, err, envelope.MessageID)
	}
	mutated := append([]byte(nil), first...)
	mutated[len(mutated)-1] ^= 1
	if _, err := ParseDependencyEvidenceEnvelope(mutated); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestDependencyEvidenceEnvelopeRejectsWrongVersionOriginAndOversize(t *testing.T) {
	envelope := testEnvelope(t)
	envelope.ProtocolVersion++
	if _, err := envelope.MarshalBinary(); err == nil {
		t.Fatal("unsupported version accepted")
	}
	envelope = testEnvelope(t)
	envelope.OriginPeer = "not-a-peer"
	if _, err := envelope.MarshalBinary(); err == nil {
		t.Fatal("invalid origin accepted")
	}
	if _, err := ParseDependencyEvidenceEnvelope(make([]byte, MaxDependencyEvidenceEnvelopeSize+1)); err == nil {
		t.Fatal("oversize envelope accepted")
	}
}

func TestDependencyEvidenceEnvelopeRequiresSignedSenderBinding(t *testing.T) {
	envelope := testEnvelope(t)
	if err := envelope.ValidateFrom(envelope.OriginPeer); err != nil {
		t.Fatal(err)
	}
	key, _, _ := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	other, _ := peer.IDFromPrivateKey(key)
	if err := envelope.ValidateFrom(other); err == nil {
		t.Fatal("wrong signed sender accepted")
	}
}
