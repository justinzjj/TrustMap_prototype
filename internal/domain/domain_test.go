package domain

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestChainIDValidation(t *testing.T) {
	chainID, err := NewChainID(11155111)
	if err != nil {
		t.Fatalf("NewChainID() error = %v", err)
	}
	if got := chainID.BigInt(); got.Cmp(big.NewInt(11155111)) != 0 {
		t.Fatalf("NewChainID().BigInt() = %s, want 11155111", got)
	}
	if err := chainID.Validate(); err != nil {
		t.Fatalf("ChainID.Validate() error = %v", err)
	}

	if _, err := NewChainID(0); !errors.Is(err, ErrZeroChainID) {
		t.Fatalf("NewChainID(0) error = %v, want %v", err, ErrZeroChainID)
	}
	if err := (ChainID{}).Validate(); !errors.Is(err, ErrZeroChainID) {
		t.Fatalf("zero ChainID.Validate() error = %v, want %v", err, ErrZeroChainID)
	}
}

func TestUint256TypesAcceptMaximumValueAndDefensivelyExposeIt(t *testing.T) {
	chainInput := maxUint256()
	heightInput := maxUint256()
	chainID, err := NewChainIDFromBig(chainInput)
	if err != nil {
		t.Fatalf("NewChainIDFromBig(max) error = %v", err)
	}
	height, err := NewBlockHeightFromBig(heightInput)
	if err != nil {
		t.Fatalf("NewBlockHeightFromBig(max) error = %v", err)
	}
	chainInput.SetUint64(1)
	heightInput.SetUint64(2)
	max := maxUint256()

	wantBytes := [32]byte{}
	for index := range wantBytes {
		wantBytes[index] = 0xff
	}
	if got := chainID.Bytes32(); got != wantBytes {
		t.Fatalf("ChainID.Bytes32() = %x, want %x", got, wantBytes)
	}
	if got := height.Bytes32(); got != wantBytes {
		t.Fatalf("BlockHeight.Bytes32() = %x, want %x", got, wantBytes)
	}

	chainBig := chainID.BigInt()
	heightBig := height.BigInt()
	chainBig.SetUint64(1)
	heightBig.SetUint64(2)
	if got := chainID.BigInt(); got.Cmp(max) != 0 {
		t.Fatalf("ChainID.BigInt() exposed mutable state: got %s, want %s", got, max)
	}
	if got := height.BigInt(); got.Cmp(max) != 0 {
		t.Fatalf("BlockHeight.BigInt() exposed mutable state: got %s, want %s", got, max)
	}

	chainBytes := chainID.Bytes32()
	heightBytes := height.Bytes32()
	chainBytes[0] = 0
	heightBytes[0] = 0
	if chainID.Bytes32() != wantBytes {
		t.Fatal("ChainID.Bytes32() exposed mutable state")
	}
	if height.Bytes32() != wantBytes {
		t.Fatal("BlockHeight.Bytes32() exposed mutable state")
	}
}

func TestUint256ConstructorsRejectInvalidBigInts(t *testing.T) {
	overflow := new(big.Int).Lsh(big.NewInt(1), 256)
	tests := []struct {
		name    string
		value   *big.Int
		wantErr error
		create  func(*big.Int) error
	}{
		{
			name:    "nil chain ID",
			wantErr: ErrNilUint256,
			create: func(value *big.Int) error {
				_, err := NewChainIDFromBig(value)
				return err
			},
		},
		{
			name:    "negative chain ID",
			value:   big.NewInt(-1),
			wantErr: ErrNegativeUint256,
			create: func(value *big.Int) error {
				_, err := NewChainIDFromBig(value)
				return err
			},
		},
		{
			name:    "overflow chain ID",
			value:   overflow,
			wantErr: ErrUint256Overflow,
			create: func(value *big.Int) error {
				_, err := NewChainIDFromBig(value)
				return err
			},
		},
		{
			name:    "nil block height",
			wantErr: ErrNilUint256,
			create: func(value *big.Int) error {
				_, err := NewBlockHeightFromBig(value)
				return err
			},
		},
		{
			name:    "negative block height",
			value:   big.NewInt(-1),
			wantErr: ErrNegativeUint256,
			create: func(value *big.Int) error {
				_, err := NewBlockHeightFromBig(value)
				return err
			},
		},
		{
			name:    "overflow block height",
			value:   overflow,
			wantErr: ErrUint256Overflow,
			create: func(value *big.Int) error {
				_, err := NewBlockHeightFromBig(value)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.create(tt.value); !errors.Is(err, tt.wantErr) {
				t.Fatalf("constructor error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestBlockHeightAllowsZero(t *testing.T) {
	height, err := NewBlockHeight(0)
	if err != nil {
		t.Fatalf("NewBlockHeight(0) error = %v", err)
	}
	if got := height.BigInt(); got.Sign() != 0 {
		t.Fatalf("NewBlockHeight(0).BigInt() = %s, want 0", got)
	}
}

func TestRequestIDPreservesHashValue(t *testing.T) {
	want := common.HexToHash("0x1234")
	requestID := RequestID(want)
	if common.Hash(requestID) != want {
		t.Fatalf("RequestID = %s, want %s", common.Hash(requestID), want)
	}
}

func TestDependencyValidation(t *testing.T) {
	valid := Dependency{
		SourceChainID:   mustChainID(t, 11155111),
		SourceHeight:    mustBlockHeight(t, 123456789),
		SourceBlockHash: common.HexToHash("0x2222"),
		SourceTrustRoot: common.HexToHash("0x1111"),
	}

	dependency, err := NewDependency(
		valid.SourceChainID,
		valid.SourceHeight,
		valid.SourceBlockHash,
		valid.SourceTrustRoot,
	)
	if err != nil {
		t.Fatalf("NewDependency() error = %v", err)
	}
	if dependency != valid {
		t.Fatalf("NewDependency() = %+v, want %+v", dependency, valid)
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Dependency.Validate() error = %v", err)
	}

	tests := []struct {
		name       string
		dependency Dependency
		wantErr    error
	}{
		{
			name: "zero source chain ID",
			dependency: Dependency{
				SourceHeight:    valid.SourceHeight,
				SourceBlockHash: valid.SourceBlockHash,
				SourceTrustRoot: valid.SourceTrustRoot,
			},
			wantErr: ErrZeroChainID,
		},
		{
			name: "zero source block hash",
			dependency: Dependency{
				SourceChainID:   valid.SourceChainID,
				SourceHeight:    valid.SourceHeight,
				SourceTrustRoot: valid.SourceTrustRoot,
			},
			wantErr: ErrZeroBlockHash,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDependency(
				tt.dependency.SourceChainID,
				tt.dependency.SourceHeight,
				tt.dependency.SourceBlockHash,
				tt.dependency.SourceTrustRoot,
			); !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewDependency() error = %v, want %v", err, tt.wantErr)
			}
			if err := tt.dependency.Validate(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Dependency.Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestDependencyAllowsZeroTrustRoot(t *testing.T) {
	dependency := Dependency{
		SourceChainID:   mustChainID(t, 11155111),
		SourceHeight:    mustBlockHeight(t, 123456789),
		SourceBlockHash: common.HexToHash("0x2222"),
	}

	if err := dependency.Validate(); err != nil {
		t.Fatalf("Dependency.Validate() error = %v, want nil", err)
	}
	if _, err := NewDependency(
		dependency.SourceChainID,
		dependency.SourceHeight,
		dependency.SourceBlockHash,
		dependency.SourceTrustRoot,
	); err != nil {
		t.Fatalf("NewDependency() error = %v, want nil", err)
	}
}

func TestMembershipWitnessValidationAndDefensiveCopies(t *testing.T) {
	first := common.HexToHash("0x01")
	second := common.HexToHash("0x02")
	input := []common.Hash{first, second}

	witness, err := NewMembershipWitness(7, input)
	if err != nil {
		t.Fatalf("NewMembershipWitness() error = %v", err)
	}
	if witness.LeafIndex() != 7 {
		t.Fatalf("LeafIndex() = %d, want 7", witness.LeafIndex())
	}
	if err := witness.Validate(); err != nil {
		t.Fatalf("MembershipWitness.Validate() error = %v", err)
	}

	input[0] = common.HexToHash("0xff")
	if got := witness.Siblings()[0]; got != first {
		t.Fatalf("constructor retained mutable input: first sibling = %s, want %s", got, first)
	}

	output := witness.Siblings()
	output[1] = common.HexToHash("0xee")
	if got := witness.Siblings()[1]; got != second {
		t.Fatalf("Siblings() exposed mutable state: second sibling = %s, want %s", got, second)
	}

	if _, err := NewMembershipWitness(0, nil); !errors.Is(err, ErrEmptyWitness) {
		t.Fatalf("NewMembershipWitness(nil) error = %v, want %v", err, ErrEmptyWitness)
	}
	if err := (MembershipWitness{}).Validate(); !errors.Is(err, ErrEmptyWitness) {
		t.Fatalf("zero MembershipWitness.Validate() error = %v, want %v", err, ErrEmptyWitness)
	}
}

func TestLeafHashGoldenVector(t *testing.T) {
	trustRoot := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	blockHash := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	want := common.HexToHash("0x3e92e0db88d6afea9edc4eedf62fffa4d92bcdfc310dccbe943747fe8302e871")

	if got := LeafHash(trustRoot, blockHash); got != want {
		t.Fatalf("LeafHash() = %s, want %s", got, want)
	}
}

func TestDependencyKeyGoldenVector(t *testing.T) {
	dependency := goldenDependency(t)
	want := common.HexToHash("0x049e1ae5898af8654b068abeb459b601dc683ba80211d7567ac97e7f90162122")

	if got := DependencyKey(dependency); got != want {
		t.Fatalf("DependencyKey() = %s, want %s", got, want)
	}
}

func TestDependencyKeyMaxUint256GoldenVector(t *testing.T) {
	chainID, err := NewChainIDFromBig(maxUint256())
	if err != nil {
		t.Fatalf("NewChainIDFromBig(max) error = %v", err)
	}
	height, err := NewBlockHeightFromBig(maxUint256())
	if err != nil {
		t.Fatalf("NewBlockHeightFromBig(max) error = %v", err)
	}
	dependency := Dependency{
		SourceChainID:   chainID,
		SourceHeight:    height,
		SourceBlockHash: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
	}
	want := common.HexToHash("0xcfe1574b8f4b12f2c1482d5cc3bd148a48979c9433e195edf5f957057a79f467")

	if got := DependencyKey(dependency); got != want {
		t.Fatalf("DependencyKey(max uint256) = %s, want %s", got, want)
	}
}

func TestDependencyKeyChangesWithEveryField(t *testing.T) {
	base := goldenDependency(t)
	baseKey := DependencyKey(base)

	tests := []struct {
		name       string
		dependency Dependency
	}{
		{
			name: "source chain ID",
			dependency: Dependency{
				SourceChainID:   mustChainID(t, 11155112),
				SourceHeight:    base.SourceHeight,
				SourceBlockHash: base.SourceBlockHash,
				SourceTrustRoot: base.SourceTrustRoot,
			},
		},
		{
			name: "source height",
			dependency: Dependency{
				SourceChainID:   base.SourceChainID,
				SourceHeight:    mustBlockHeight(t, 123456790),
				SourceBlockHash: base.SourceBlockHash,
				SourceTrustRoot: base.SourceTrustRoot,
			},
		},
		{
			name: "source block hash",
			dependency: Dependency{
				SourceChainID:   base.SourceChainID,
				SourceHeight:    base.SourceHeight,
				SourceBlockHash: common.HexToHash("0x3333"),
				SourceTrustRoot: base.SourceTrustRoot,
			},
		},
		{
			name: "source trust root",
			dependency: Dependency{
				SourceChainID:   base.SourceChainID,
				SourceHeight:    base.SourceHeight,
				SourceBlockHash: base.SourceBlockHash,
				SourceTrustRoot: common.HexToHash("0x4444"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DependencyKey(tt.dependency); got == baseKey {
				t.Fatalf("DependencyKey() unchanged after changing %s", tt.name)
			}
		})
	}
}

func goldenDependency(t *testing.T) Dependency {
	t.Helper()
	return Dependency{
		SourceChainID:   mustChainID(t, 11155111),
		SourceHeight:    mustBlockHeight(t, 123456789),
		SourceBlockHash: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		SourceTrustRoot: common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
	}
}

func mustChainID(t *testing.T, value uint64) ChainID {
	t.Helper()
	chainID, err := NewChainID(value)
	if err != nil {
		t.Fatalf("NewChainID(%d) error = %v", value, err)
	}
	return chainID
}

func mustBlockHeight(t *testing.T, value uint64) BlockHeight {
	t.Helper()
	height, err := NewBlockHeight(value)
	if err != nil {
		t.Fatalf("NewBlockHeight(%d) error = %v", value, err)
	}
	return height
}

func maxUint256() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
}
