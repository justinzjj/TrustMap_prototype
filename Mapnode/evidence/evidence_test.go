package evidence

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestComputeIDMatchesABIGoldenAndEveryFieldIsBound(t *testing.T) {
	locator := testLocator(t)
	want := common.HexToHash("0x77be3a0e68fa7c76660eb0336a9f89c74053e39e4470a89e7f6f98e1de448a62")
	got, err := ComputeID(locator)
	if err != nil {
		t.Fatalf("ComputeID() error = %v", err)
	}
	if common.Hash(got) != want {
		t.Fatalf("ComputeID() = %s, want %s", common.Hash(got), want)
	}

	mutations := []func(*Locator){
		func(v *Locator) { v.ChainID, _ = domain.NewChainID(2) },
		func(v *Locator) {
			v.ContractAddress = common.HexToAddress("0x2000000000000000000000000000000000000002")
		},
		func(v *Locator) { v.BlockNumber, _ = domain.NewBlockHeight(43) },
		func(v *Locator) { v.BlockHash[31] ^= 1 },
		func(v *Locator) { v.TxHash[31] ^= 1 },
		func(v *Locator) { v.TxIndex++ },
		func(v *Locator) { v.LogIndex++ },
		func(v *Locator) { v.PayloadDigest[31] ^= 1 },
	}
	for index, mutate := range mutations {
		changed := locator
		mutate(&changed)
		changedID, err := ComputeID(changed)
		if err != nil {
			t.Fatalf("mutation %d ComputeID() error = %v", index, err)
		}
		if changedID == got {
			t.Fatalf("mutation %d did not change ID", index)
		}
	}
}

func TestComputeIDRejectsInvalidLocatorFields(t *testing.T) {
	valid := testLocator(t)
	tests := []struct {
		name string
		edit func(*Locator)
	}{
		{"zero chain", func(v *Locator) { v.ChainID = domain.ChainID{} }},
		{"zero address", func(v *Locator) { v.ContractAddress = common.Address{} }},
		{"zero block hash", func(v *Locator) { v.BlockHash = common.Hash{} }},
		{"zero transaction hash", func(v *Locator) { v.TxHash = common.Hash{} }},
		{"zero payload digest", func(v *Locator) { v.PayloadDigest = common.Hash{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locator := valid
			tt.edit(&locator)
			if _, err := ComputeID(locator); err == nil {
				t.Fatal("ComputeID() error = nil, want validation error")
			}
		})
	}
}

func TestComputeGatewayRequestIDMatchesSolidityGoldenAtUint256Maximum(t *testing.T) {
	home, _ := domain.NewChainIDFromBig(maxUint256())
	source, _ := domain.NewChainIDFromBig(new(big.Int).Sub(maxUint256(), big.NewInt(1)))
	height, _ := domain.NewBlockHeightFromBig(new(big.Int).Sub(maxUint256(), big.NewInt(2)))
	got, err := ComputeGatewayRequestID(
		home,
		common.HexToAddress("0x1000000000000000000000000000000000000001"),
		common.HexToAddress("0x2000000000000000000000000000000000000002"),
		maxUint256(),
		source,
		height,
		common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	)
	if err != nil {
		t.Fatalf("ComputeGatewayRequestID() error = %v", err)
	}
	want := common.HexToHash("0x5eb212e666e38fd8edd72d41533af6b07558cb85e0702d7521a1803b616f0217")
	if common.Hash(got) != want {
		t.Fatalf("ComputeGatewayRequestID() = %s, want %s", common.Hash(got), want)
	}
}

func TestComputeGatewayRequestIDRejectsInvalidInput(t *testing.T) {
	home, _ := domain.NewChainID(1)
	source, _ := domain.NewChainID(2)
	height, _ := domain.NewBlockHeight(3)
	gateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	requester := common.HexToAddress("0x2000000000000000000000000000000000000002")
	hash := common.HexToHash("0x01")
	invalidNonces := []*big.Int{nil, big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 256)}
	for _, nonce := range invalidNonces {
		if _, err := ComputeGatewayRequestID(home, gateway, requester, nonce, source, height, hash); err == nil {
			t.Fatalf("nonce %v accepted", nonce)
		}
	}
	if _, err := ComputeGatewayRequestID(domain.ChainID{}, gateway, requester, big.NewInt(0), source, height, hash); err == nil {
		t.Fatal("zero home chain accepted")
	}
	if _, err := ComputeGatewayRequestID(home, common.Address{}, requester, big.NewInt(0), source, height, hash); err == nil {
		t.Fatal("zero gateway accepted")
	}
	if _, err := ComputeGatewayRequestID(home, gateway, common.Address{}, big.NewInt(0), source, height, hash); err == nil {
		t.Fatal("zero requester accepted")
	}
	if _, err := ComputeGatewayRequestID(home, gateway, requester, big.NewInt(0), domain.ChainID{}, height, hash); err == nil {
		t.Fatal("zero source chain accepted")
	}
	if _, err := ComputeGatewayRequestID(home, gateway, requester, big.NewInt(0), source, height, common.Hash{}); err == nil {
		t.Fatal("zero source block hash accepted")
	}
}

func TestEvidenceStateTransitionGraph(t *testing.T) {
	legal := [][2]State{{Candidate, Verified}, {Verified, Confirmed}, {Confirmed, Active}, {Candidate, Invalid}}
	for _, edge := range legal {
		changed, err := ValidateTransition(edge[0], edge[1], "reason")
		if err != nil || !changed {
			t.Fatalf("%s -> %s = (%t, %v), want (true, nil)", edge[0], edge[1], changed, err)
		}
	}
	for _, state := range AllStates() {
		changed, err := ValidateTransition(state, state, "")
		if err != nil || changed {
			t.Fatalf("%s -> itself = (%t, %v), want idempotent", state, changed, err)
		}
	}
	for _, edge := range [][2]State{{Candidate, Active}, {Verified, Candidate}, {Active, Invalid}, {Invalid, Candidate}} {
		if changed, err := ValidateTransition(edge[0], edge[1], "reason"); err == nil || changed {
			t.Fatalf("%s -> %s accepted", edge[0], edge[1])
		}
	}
	if _, err := ValidateTransition(Candidate, Invalid, ""); !errors.Is(err, ErrInvalidReasonRequired) {
		t.Fatalf("candidate -> invalid empty reason error = %v", err)
	}
}

func testLocator(t *testing.T) Locator {
	t.Helper()
	chainID, _ := domain.NewChainID(1)
	height, _ := domain.NewBlockHeight(42)
	return Locator{
		ChainID: chainID, ContractAddress: common.HexToAddress("0x1000000000000000000000000000000000000001"),
		BlockNumber: height, BlockHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		TxHash:  common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		TxIndex: 7, LogIndex: 9,
		PayloadDigest: common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
	}
}

func maxUint256() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
}
