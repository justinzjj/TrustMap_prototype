package trustview_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestTrustRootObservationBindsConfirmationAndCreatesOnlySyntheticNodeEvidence(t *testing.T) {
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(40)
	head, _ := domain.NewBlockHeight(43)
	observation, err := trustview.NewTrustRootObservation(trustview.TrustRootObservationContent{
		ChainID: chainID, Height: height, BlockHash: common.HexToHash("0x40"),
		Gateway:   common.HexToAddress("0x1000000000000000000000000000000000000001"),
		TrustRoot: trustview.TrustRoot{Hash: common.HexToHash("0x41")}, GatewayCodeHash: common.HexToHash("0x42"),
		RequiredConfirmations: 2, ConfirmedHeadHeight: head, ConfirmedHeadHash: common.HexToHash("0x43"),
	})
	if err != nil {
		t.Fatal(err)
	}
	record := observation.SyntheticEvidence()
	if record.State != evidence.Candidate || record.Locator.TxHash != (common.Hash{}) || record.Locator.TxIndex != 0 || record.Locator.LogIndex != 0 {
		t.Fatalf("synthetic evidence = %+v", record)
	}
	if record.Locator.PayloadDigest == (common.Hash{}) {
		t.Fatal("synthetic evidence lacks domain-separated payload digest")
	}
	if _, err := evidence.ComputeID(record.Locator); err == nil {
		t.Fatal("synthetic observation locator was accepted as transaction/log Evidence")
	}
	node := observation.TrustNode()
	if node.EvidenceID != record.ID || node.Key.ChainID != chainID || node.Root != observation.TrustRoot {
		t.Fatalf("node = %+v", node)
	}
	changed := observation
	changed.ConfirmedHeadHash[31] ^= 1
	if changed.ComputeID() == observation.ID || changed.SyntheticEvidence().ID == record.ID {
		t.Fatal("confirmation evidence is absent from content identity")
	}
}

func TestTrustRootObservationRejectsInsufficientConfirmations(t *testing.T) {
	chainID, _ := domain.NewChainID(1)
	height, _ := domain.NewBlockHeight(10)
	head, _ := domain.NewBlockHeight(11)
	_, err := trustview.NewTrustRootObservation(trustview.TrustRootObservationContent{
		ChainID: chainID, Height: height, BlockHash: common.HexToHash("0x1"), Gateway: common.HexToAddress("0x1000000000000000000000000000000000000001"),
		TrustRoot: trustview.TrustRoot{Hash: common.HexToHash("0x2")}, GatewayCodeHash: common.HexToHash("0x3"), RequiredConfirmations: 2,
		ConfirmedHeadHeight: head, ConfirmedHeadHash: common.HexToHash("0x4"),
	})
	if err == nil {
		t.Fatal("insufficient confirmations accepted")
	}
}

func TestTrustRootObservationAllowsZeroInitialTrustRoot(t *testing.T) {
	chainID, _ := domain.NewChainID(1)
	height, _ := domain.NewBlockHeight(10)
	head, _ := domain.NewBlockHeight(12)
	observation, err := trustview.NewTrustRootObservation(trustview.TrustRootObservationContent{
		ChainID: chainID, Height: height, BlockHash: common.HexToHash("0x1"), Gateway: common.HexToAddress("0x1000000000000000000000000000000000000001"),
		TrustRoot: trustview.TrustRoot{}, GatewayCodeHash: common.HexToHash("0x3"), RequiredConfirmations: 2,
		ConfirmedHeadHeight: head, ConfirmedHeadHash: common.HexToHash("0x4"),
	})
	if err != nil {
		t.Fatalf("zero initial TrustRoot rejected: %v", err)
	}
	if observation.TrustRoot.Hash != (common.Hash{}) || observation.ID == (trustview.TrustRootObservationID{}) {
		t.Fatalf("zero-root observation = %+v", observation)
	}
}
