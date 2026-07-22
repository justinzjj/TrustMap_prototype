package executor_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/executor"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestPathExecutorMapsPersistedPathProofWithoutReversal(t *testing.T) {
	wBA, _ := domain.NewMembershipWitness(3, []common.Hash{common.HexToHash("0x31")})
	wCB, _ := domain.NewMembershipWitness(4, []common.Hash{common.HexToHash("0x41"), common.HexToHash("0x42")})
	requestID := domain.RequestID(common.HexToHash("0x99"))
	persisted := pathproof.PathProof{
		RequestID:     requestID,
		BaseTrustRoot: trustview.TrustRoot{Hash: common.HexToHash("0xaa")},
		BlockHashes:   []common.Hash{common.HexToHash("0xab"), common.HexToHash("0xbc")},
		Witnesses:     []domain.MembershipWitness{wBA, wCB},
	}
	persisted.ID = pathproof.ComputePathProofID(persisted)
	call, err := executor.BuildPathCall(requestID, persisted)
	if err != nil {
		t.Fatal(err)
	}
	want := chainabi.EncodeVerifyPathAndRecordCall(requestID, chainabi.PathCallProof{
		BaseTrustRoot: common.HexToHash("0xaa"),
		BlockHashes:   []common.Hash{common.HexToHash("0xab"), common.HexToHash("0xbc")},
		Witnesses: []chainabi.SolidityWitness{
			{LeafIndex: 3, Siblings: []common.Hash{common.HexToHash("0x31")}},
			{LeafIndex: 4, Siblings: []common.Hash{common.HexToHash("0x41"), common.HexToHash("0x42")}},
		},
	})
	if string(call) != string(want) {
		t.Fatalf("persisted path order changed\n got %x\nwant %x", call, want)
	}
}

func TestPathExecutorRejectsMismatchedPersistedProof(t *testing.T) {
	if _, err := executor.BuildPathCall(domain.RequestID(common.HexToHash("0x1")), pathproof.PathProof{RequestID: domain.RequestID(common.HexToHash("0x2"))}); err == nil {
		t.Fatal("mismatched request proof accepted")
	}
}
