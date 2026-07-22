package p2p

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	DependencyEvidenceProtocolVersion uint16 = 1
	DependencyEvidenceTopic                  = "/trustmap/dependency-evidence/1"
	MaxDependencyEvidenceEnvelopeSize        = 512
	maxOriginPeerBytes                       = 128
)

var envelopeMagic = [4]byte{'T', 'M', 'D', 'E'}

type DependencyEvidenceEnvelope struct {
	ProtocolVersion  uint16
	MessageID        common.Hash
	OriginPeer       peer.ID
	ObservedAt       time.Time
	RecordingChainID domain.ChainID
	Gateway          common.Address
	BlockNumber      domain.BlockHeight
	BlockHash        common.Hash
	TxHash           common.Hash
	TxIndex          uint32
	LogIndex         uint32
	PayloadDigest    common.Hash
}

func NewDependencyEvidenceEnvelope(origin peer.ID, observedAt time.Time, locator evidence.Locator) (DependencyEvidenceEnvelope, error) {
	envelope := DependencyEvidenceEnvelope{
		ProtocolVersion: DependencyEvidenceProtocolVersion,
		OriginPeer:      origin, ObservedAt: observedAt.UTC(), RecordingChainID: locator.ChainID,
		Gateway: locator.ContractAddress, BlockNumber: locator.BlockNumber, BlockHash: locator.BlockHash,
		TxHash: locator.TxHash, TxIndex: locator.TxIndex, LogIndex: locator.LogIndex, PayloadDigest: locator.PayloadDigest,
	}
	messageID, err := envelope.ComputeMessageID()
	if err != nil {
		return DependencyEvidenceEnvelope{}, err
	}
	envelope.MessageID = messageID
	return envelope, nil
}

func (envelope DependencyEvidenceEnvelope) Locator() evidence.Locator {
	return evidence.Locator{ChainID: envelope.RecordingChainID, ContractAddress: envelope.Gateway,
		BlockNumber: envelope.BlockNumber, BlockHash: envelope.BlockHash, TxHash: envelope.TxHash,
		TxIndex: envelope.TxIndex, LogIndex: envelope.LogIndex, PayloadDigest: envelope.PayloadDigest}
}

func (envelope DependencyEvidenceEnvelope) validateContent() error {
	if envelope.ProtocolVersion != DependencyEvidenceProtocolVersion {
		return fmt.Errorf("unsupported dependency evidence protocol version %d", envelope.ProtocolVersion)
	}
	originBytes := []byte(envelope.OriginPeer)
	if len(originBytes) == 0 || len(originBytes) > maxOriginPeerBytes {
		return errors.New("invalid dependency evidence origin peer length")
	}
	parsed, err := peer.IDFromBytes(originBytes)
	if err != nil || parsed != envelope.OriginPeer {
		return errors.New("invalid dependency evidence origin peer")
	}
	if envelope.ObservedAt.IsZero() || envelope.ObservedAt.UnixNano() <= 0 {
		return errors.New("dependency evidence observation timestamp must be positive")
	}
	if err := envelope.Locator().Validate(); err != nil {
		return err
	}
	return nil
}

func (envelope DependencyEvidenceEnvelope) ComputeMessageID() (common.Hash, error) {
	content, err := envelope.canonicalContent()
	if err != nil {
		return common.Hash{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("TrustMap/DependencyEvidenceEnvelope/MessageID/v1\x00"))
	_, _ = hash.Write(content)
	return common.BytesToHash(hash.Sum(nil)), nil
}

func (envelope DependencyEvidenceEnvelope) canonicalContent() ([]byte, error) {
	if err := envelope.validateContent(); err != nil {
		return nil, err
	}
	origin := []byte(envelope.OriginPeer)
	buffer := bytes.NewBuffer(make([]byte, 0, 204+len(origin)))
	_ = binary.Write(buffer, binary.BigEndian, envelope.ProtocolVersion)
	_ = binary.Write(buffer, binary.BigEndian, uint16(len(origin)))
	_, _ = buffer.Write(origin)
	_ = binary.Write(buffer, binary.BigEndian, envelope.ObservedAt.UnixNano())
	_, _ = buffer.Write(envelope.RecordingChainID[:])
	_, _ = buffer.Write(envelope.Gateway[:])
	_, _ = buffer.Write(envelope.BlockNumber[:])
	_, _ = buffer.Write(envelope.BlockHash[:])
	_, _ = buffer.Write(envelope.TxHash[:])
	_ = binary.Write(buffer, binary.BigEndian, envelope.TxIndex)
	_ = binary.Write(buffer, binary.BigEndian, envelope.LogIndex)
	_, _ = buffer.Write(envelope.PayloadDigest[:])
	return buffer.Bytes(), nil
}

func (envelope DependencyEvidenceEnvelope) MarshalBinary() ([]byte, error) {
	content, err := envelope.canonicalContent()
	if err != nil {
		return nil, err
	}
	want, err := envelope.ComputeMessageID()
	if err != nil {
		return nil, err
	}
	if envelope.MessageID != want {
		return nil, errors.New("dependency evidence MessageID does not match canonical content")
	}
	encoded := make([]byte, 0, len(envelopeMagic)+len(envelope.MessageID)+len(content))
	encoded = append(encoded, envelopeMagic[:]...)
	encoded = append(encoded, envelope.MessageID[:]...)
	encoded = append(encoded, content...)
	if len(encoded) > MaxDependencyEvidenceEnvelopeSize {
		return nil, errors.New("dependency evidence envelope exceeds size limit")
	}
	return encoded, nil
}

func ParseDependencyEvidenceEnvelope(encoded []byte) (DependencyEvidenceEnvelope, error) {
	if len(encoded) > MaxDependencyEvidenceEnvelopeSize || len(encoded) < 236 {
		return DependencyEvidenceEnvelope{}, errors.New("invalid dependency evidence envelope size")
	}
	if !bytes.Equal(encoded[:4], envelopeMagic[:]) {
		return DependencyEvidenceEnvelope{}, errors.New("invalid dependency evidence envelope magic")
	}
	offset := 4
	var envelope DependencyEvidenceEnvelope
	copy(envelope.MessageID[:], encoded[offset:offset+32])
	offset += 32
	envelope.ProtocolVersion = binary.BigEndian.Uint16(encoded[offset : offset+2])
	offset += 2
	originLength := int(binary.BigEndian.Uint16(encoded[offset : offset+2]))
	offset += 2
	if originLength == 0 || originLength > maxOriginPeerBytes || offset+originLength+196 != len(encoded) {
		return DependencyEvidenceEnvelope{}, errors.New("invalid dependency evidence origin peer framing")
	}
	origin, err := peer.IDFromBytes(encoded[offset : offset+originLength])
	if err != nil {
		return DependencyEvidenceEnvelope{}, errors.New("invalid dependency evidence origin peer")
	}
	envelope.OriginPeer = origin
	offset += originLength
	nanos := int64(binary.BigEndian.Uint64(encoded[offset : offset+8]))
	offset += 8
	envelope.ObservedAt = time.Unix(0, nanos).UTC()
	copy(envelope.RecordingChainID[:], encoded[offset:offset+32])
	offset += 32
	copy(envelope.Gateway[:], encoded[offset:offset+20])
	offset += 20
	copy(envelope.BlockNumber[:], encoded[offset:offset+32])
	offset += 32
	copy(envelope.BlockHash[:], encoded[offset:offset+32])
	offset += 32
	copy(envelope.TxHash[:], encoded[offset:offset+32])
	offset += 32
	envelope.TxIndex = binary.BigEndian.Uint32(encoded[offset : offset+4])
	offset += 4
	envelope.LogIndex = binary.BigEndian.Uint32(encoded[offset : offset+4])
	offset += 4
	copy(envelope.PayloadDigest[:], encoded[offset:offset+32])
	offset += 32
	if offset != len(encoded) {
		return DependencyEvidenceEnvelope{}, errors.New("trailing dependency evidence envelope bytes")
	}
	if err := envelope.validateContent(); err != nil {
		return DependencyEvidenceEnvelope{}, err
	}
	want, err := envelope.ComputeMessageID()
	if err != nil || want != envelope.MessageID {
		return DependencyEvidenceEnvelope{}, errors.New("dependency evidence MessageID mismatch")
	}
	return envelope, nil
}

func (envelope DependencyEvidenceEnvelope) ValidateFrom(sender peer.ID) error {
	if sender == "" || sender != envelope.OriginPeer {
		return errors.New("dependency evidence origin does not match signed libp2p sender")
	}
	_, err := envelope.MarshalBinary()
	return err
}
