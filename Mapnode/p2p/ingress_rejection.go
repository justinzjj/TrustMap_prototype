package p2p

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
)

type IngressRejection struct {
	RejectionID        common.Hash
	ActualOrigin       peer.ID
	ClaimedOrigin      peer.ID
	ClaimedMessageID   common.Hash
	PayloadFingerprint common.Hash
	Reason             string
	ReceivedAt         time.Time
}

func NewIngressRejection(actual peer.ID, envelope DependencyEvidenceEnvelope, payload []byte, reason string, receivedAt time.Time) (IngressRejection, error) {
	if actual == "" || actual == envelope.OriginPeer || reason == "" || receivedAt.IsZero() {
		return IngressRejection{}, errors.New("invalid dependency evidence ingress rejection")
	}
	if parsed, err := ParseDependencyEvidenceEnvelope(payload); err != nil || parsed != envelope {
		return IngressRejection{}, errors.New("ingress rejection payload does not match envelope")
	}
	fingerprintHash := sha256.New()
	_, _ = fingerprintHash.Write([]byte("TrustMap/DependencyEvidenceEnvelope/PayloadFingerprint/v1\x00"))
	_, _ = fingerprintHash.Write(payload)
	fingerprint := common.BytesToHash(fingerprintHash.Sum(nil))
	rejectionHash := sha256.New()
	_, _ = rejectionHash.Write([]byte("TrustMap/DependencyEvidenceEnvelope/IngressRejection/v1\x00"))
	actualBytes := []byte(actual)
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(actualBytes)))
	_, _ = rejectionHash.Write(length[:])
	_, _ = rejectionHash.Write(actualBytes)
	_, _ = rejectionHash.Write(fingerprint[:])
	return IngressRejection{
		RejectionID: common.BytesToHash(rejectionHash.Sum(nil)), ActualOrigin: actual,
		ClaimedOrigin: envelope.OriginPeer, ClaimedMessageID: envelope.MessageID,
		PayloadFingerprint: fingerprint, Reason: reason, ReceivedAt: receivedAt.UTC(),
	}, nil
}
