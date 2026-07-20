package proof

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type fixtureWitness struct {
	LeafIndex uint32   `json:"leafIndex"`
	Siblings  []string `json:"siblings"`
}

type merkleFixture struct {
	SchemaVersion int `json:"schemaVersion"`
	PackedLeaf    struct {
		SourceTrustRoot string `json:"sourceTrustRoot"`
		SourceBlockHash string `json:"sourceBlockHash"`
		ExpectedLeaf    string `json:"expectedLeaf"`
	} `json:"packedLeaf"`
	ZeroHashes struct {
		Depth  uint8    `json:"depth"`
		Hashes []string `json:"hashes"`
	} `json:"zeroHashes"`
	IncrementalTree struct {
		Depth             uint8    `json:"depth"`
		Leaves            []string `json:"leaves"`
		RootsAfterAppend  []string `json:"rootsAfterAppend"`
		ExpectedNextIndex uint64   `json:"expectedNextIndex"`
		ExpectedCapacity  uint64   `json:"expectedCapacity"`
	} `json:"incrementalTree"`
	Witness struct {
		Depth        uint8    `json:"depth"`
		Leaf         string   `json:"leaf"`
		LeafIndex    uint32   `json:"leafIndex"`
		Siblings     []string `json:"siblings"`
		ExpectedRoot string   `json:"expectedRoot"`
	} `json:"witness"`
	RecursivePath struct {
		Depth             uint8            `json:"depth"`
		BaseTrustRoot     string           `json:"baseTrustRoot"`
		BlockHashes       []string         `json:"blockHashes"`
		Witnesses         []fixtureWitness `json:"witnesses"`
		IntermediateRoots []string         `json:"intermediateRoots"`
		ExpectedHomeRoot  string           `json:"expectedHomeRoot"`
	} `json:"recursivePath"`
}

func TestMerkleFixtureGoldenVectors(t *testing.T) {
	fixture := loadFixture(t)
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", fixture.SchemaVersion)
	}

	t.Run("packed leaf", func(t *testing.T) {
		got := domain.LeafHash(hash(t, fixture.PackedLeaf.SourceTrustRoot), hash(t, fixture.PackedLeaf.SourceBlockHash))
		want := hash(t, fixture.PackedLeaf.ExpectedLeaf)
		if got != want {
			t.Fatalf("domain.LeafHash() = %s, want %s", got, want)
		}
	})

	t.Run("zero hashes", func(t *testing.T) {
		got, err := ZeroHashes(fixture.ZeroHashes.Depth)
		if err != nil {
			t.Fatalf("ZeroHashes() error = %v", err)
		}
		want := hashes(t, fixture.ZeroHashes.Hashes)
		if len(got) != len(want) {
			t.Fatalf("len(ZeroHashes()) = %d, want %d", len(got), len(want))
		}
		for level := range want {
			if got[level] != want[level] {
				t.Fatalf("ZeroHashes()[%d] = %s, want %s", level, got[level], want[level])
			}
		}
	})

	t.Run("incremental roots", func(t *testing.T) {
		tree, err := NewIncrementalTree(fixture.IncrementalTree.Depth)
		if err != nil {
			t.Fatalf("NewIncrementalTree() error = %v", err)
		}
		leaves := hashes(t, fixture.IncrementalTree.Leaves)
		roots := hashes(t, fixture.IncrementalTree.RootsAfterAppend)
		for i, leaf := range leaves {
			if err := tree.Append(leaf); err != nil {
				t.Fatalf("Append(%d) error = %v", i, err)
			}
			if got := tree.Root(); got != roots[i] {
				t.Fatalf("Root() after append %d = %s, want %s", i+1, got, roots[i])
			}
		}
		if got := tree.NextIndex(); got != fixture.IncrementalTree.ExpectedNextIndex {
			t.Fatalf("NextIndex() = %d, want %d", got, fixture.IncrementalTree.ExpectedNextIndex)
		}
		if got := tree.Capacity(); got != fixture.IncrementalTree.ExpectedCapacity {
			t.Fatalf("Capacity() = %d, want %d", got, fixture.IncrementalTree.ExpectedCapacity)
		}
	})

	t.Run("membership witness", func(t *testing.T) {
		got, err := RootFromWitness(
			hash(t, fixture.Witness.Leaf),
			fixture.Witness.LeafIndex,
			hashes(t, fixture.Witness.Siblings),
			fixture.Witness.Depth,
		)
		if err != nil {
			t.Fatalf("RootFromWitness() error = %v", err)
		}
		want := hash(t, fixture.Witness.ExpectedRoot)
		if got != want {
			t.Fatalf("RootFromWitness() = %s, want %s", got, want)
		}
	})

	t.Run("recursive path", func(t *testing.T) {
		baseRoot := hash(t, fixture.RecursivePath.BaseTrustRoot)
		blockHashes := hashes(t, fixture.RecursivePath.BlockHashes)
		intermediateRoots := hashes(t, fixture.RecursivePath.IntermediateRoots)
		reconstructedRoot := baseRoot
		for i, witness := range fixture.RecursivePath.Witnesses {
			leaf := crypto.Keccak256Hash(reconstructedRoot[:], blockHashes[i][:])
			reconstructedRoot = referenceWitnessRoot(
				leaf,
				witness.LeafIndex,
				hashes(t, witness.Siblings),
			)
			if reconstructedRoot != intermediateRoots[i] {
				t.Fatalf(
					"independent root after hop %d = %s, want %s",
					i,
					reconstructedRoot,
					intermediateRoots[i],
				)
			}
		}

		witnesses := fixtureWitnesses(t, fixture.RecursivePath.Witnesses)
		err := VerifyPath(
			baseRoot,
			blockHashes,
			witnesses,
			hash(t, fixture.RecursivePath.ExpectedHomeRoot),
			fixture.RecursivePath.Depth,
		)
		if err != nil {
			t.Fatalf("VerifyPath() error = %v", err)
		}
	})
}

func TestHashPairPreservesLeftRightOrder(t *testing.T) {
	fixture := loadFixture(t)
	left := hash(t, fixture.IncrementalTree.Leaves[0])
	right := hash(t, fixture.IncrementalTree.Leaves[1])
	want := common.HexToHash("0x037fd715441fd2ad3d0377ef74079ad743d29c09303ca301614df1ad14da48a7")
	if got := HashPair(left, right); got != want {
		t.Fatalf("HashPair(left, right) = %s, want %s", got, want)
	}
	if got := HashPair(right, left); got == want {
		t.Fatalf("HashPair(right, left) unexpectedly equals ordered hash %s", want)
	}
}

func TestRejectsInvalidTreeDepth(t *testing.T) {
	for _, depth := range []uint8{0, MaxTreeDepth + 1} {
		if _, err := ZeroHashes(depth); !errors.Is(err, ErrInvalidTreeDepth) {
			t.Fatalf("ZeroHashes(%d) error = %v, want %v", depth, err, ErrInvalidTreeDepth)
		}
		if _, err := NewIncrementalTree(depth); !errors.Is(err, ErrInvalidTreeDepth) {
			t.Fatalf("NewIncrementalTree(%d) error = %v, want %v", depth, err, ErrInvalidTreeDepth)
		}
		if _, err := RootFromWitness(common.Hash{}, 0, nil, depth); !errors.Is(err, ErrInvalidTreeDepth) {
			t.Fatalf("RootFromWitness(depth=%d) error = %v, want %v", depth, err, ErrInvalidTreeDepth)
		}
		if err := VerifyPath(common.Hash{}, nil, nil, common.Hash{}, depth); !errors.Is(err, ErrInvalidTreeDepth) {
			t.Fatalf("VerifyPath(depth=%d) error = %v, want %v", depth, err, ErrInvalidTreeDepth)
		}
	}
}

func TestRootFromWitnessRejectsMalformedWitness(t *testing.T) {
	tests := []struct {
		name     string
		index    uint32
		siblings []common.Hash
		wantErr  error
	}{
		{name: "wrong sibling count", index: 0, siblings: make([]common.Hash, 2), wantErr: ErrInvalidSiblingCount},
		{name: "index at capacity", index: 8, siblings: make([]common.Hash, 3), wantErr: ErrLeafIndexOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RootFromWitness(common.Hash{}, tt.index, tt.siblings, 3); !errors.Is(err, tt.wantErr) {
				t.Fatalf("RootFromWitness() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyPathRejectsInvalidInputs(t *testing.T) {
	fixture := loadFixture(t)
	baseRoot := hash(t, fixture.RecursivePath.BaseTrustRoot)
	blockHashes := hashes(t, fixture.RecursivePath.BlockHashes)
	witnesses := fixtureWitnesses(t, fixture.RecursivePath.Witnesses)
	homeRoot := hash(t, fixture.RecursivePath.ExpectedHomeRoot)

	tests := []struct {
		name      string
		blocks    []common.Hash
		witnesses []domain.MembershipWitness
		expected  common.Hash
		wantErr   error
	}{
		{name: "empty path", wantErr: ErrEmptyPath},
		{name: "length mismatch", blocks: blockHashes[:1], wantErr: ErrPathLengthMismatch},
		{name: "final root mismatch", blocks: blockHashes, witnesses: witnesses, expected: common.HexToHash("0xdead"), wantErr: ErrFinalRootMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VerifyPath(baseRoot, tt.blocks, tt.witnesses, tt.expected, 3); !errors.Is(err, tt.wantErr) {
				t.Fatalf("VerifyPath() error = %v, want %v", err, tt.wantErr)
			}
		})
	}

	shortWitness, err := domain.NewMembershipWitness(0, make([]common.Hash, 2))
	if err != nil {
		t.Fatalf("domain.NewMembershipWitness(short) error = %v", err)
	}
	if err := VerifyPath(baseRoot, blockHashes[:1], []domain.MembershipWitness{shortWitness}, homeRoot, 3); !errors.Is(err, ErrInvalidSiblingCount) {
		t.Fatalf("VerifyPath(short witness) error = %v, want %v", err, ErrInvalidSiblingCount)
	}

	outOfRange, err := domain.NewMembershipWitness(8, make([]common.Hash, 3))
	if err != nil {
		t.Fatalf("domain.NewMembershipWitness(out of range) error = %v", err)
	}
	if err := VerifyPath(baseRoot, blockHashes[:1], []domain.MembershipWitness{outOfRange}, homeRoot, 3); !errors.Is(err, ErrLeafIndexOutOfRange) {
		t.Fatalf("VerifyPath(out-of-range witness) error = %v, want %v", err, ErrLeafIndexOutOfRange)
	}
}

func TestIncrementalTreeRejectsAppendWhenFullWithoutMutation(t *testing.T) {
	tree, err := NewIncrementalTree(1)
	if err != nil {
		t.Fatalf("NewIncrementalTree() error = %v", err)
	}
	if got := tree.Root(); got != common.HexToHash("0xad3228b676f7d3cd4284a5443f17f1962b36e491b30a40b2405849e597ba5fb5") {
		t.Fatalf("empty Root() = %s", got)
	}
	if err := tree.Append(common.HexToHash("0x01")); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := tree.Append(common.HexToHash("0x02")); err != nil {
		t.Fatalf("second Append() error = %v", err)
	}
	rootBefore := tree.Root()
	if err := tree.Append(common.HexToHash("0x03")); !errors.Is(err, ErrTreeFull) {
		t.Fatalf("third Append() error = %v, want %v", err, ErrTreeFull)
	}
	if got := tree.NextIndex(); got != 2 {
		t.Fatalf("NextIndex() after rejected append = %d, want 2", got)
	}
	if got := tree.Root(); got != rootBefore {
		t.Fatalf("Root() changed after rejected append: got %s, want %s", got, rootBefore)
	}
}

func TestIncrementalTreeMatchesIndependentFullTreeAtEveryAppend(t *testing.T) {
	const depth uint8 = 4
	tree, err := NewIncrementalTree(depth)
	if err != nil {
		t.Fatalf("NewIncrementalTree() error = %v", err)
	}

	capacity := int(uint64(1) << depth)
	appendedLeaves := make([]common.Hash, 0, capacity)
	for index := 0; index < capacity; index++ {
		leaf := crypto.Keccak256Hash([]byte{byte(index + 1)})
		appendedLeaves = append(appendedLeaves, leaf)
		if err := tree.Append(leaf); err != nil {
			t.Fatalf("Append(index=%d) error = %v", index, err)
		}

		want := referenceFullTreeRoot(depth, appendedLeaves)
		if got := tree.Root(); got != want {
			t.Fatalf("Root() after index %d = %s, want independent full-tree root %s", index, got, want)
		}
		if got := tree.NextIndex(); got != uint64(index+1) {
			t.Fatalf("NextIndex() after index %d = %d, want %d", index, got, index+1)
		}
	}

	if got := tree.NextIndex(); got != tree.Capacity() {
		t.Fatalf("full tree NextIndex() = %d, capacity = %d", got, tree.Capacity())
	}
	if got, want := tree.Root(), referenceFullTreeRoot(depth, appendedLeaves); got != want {
		t.Fatalf("full tree Root() = %s, want %s", got, want)
	}
}

func TestDepth32CapacityAndMaximumLeafIndex(t *testing.T) {
	const depth uint8 = 32
	tree, err := NewIncrementalTree(depth)
	if err != nil {
		t.Fatalf("NewIncrementalTree(32) error = %v", err)
	}
	const wantCapacity uint64 = 1 << 32
	if got := tree.Capacity(); got != wantCapacity {
		t.Fatalf("Capacity() = %d, want %d", got, wantCapacity)
	}

	leaf := crypto.Keccak256Hash([]byte("depth-32-leaf"))
	siblings := make([]common.Hash, depth)
	for level := range siblings {
		siblings[level] = crypto.Keccak256Hash([]byte{byte(level)})
	}
	leafIndex := uint32(math.MaxUint32)
	got, err := RootFromWitness(leaf, leafIndex, siblings, depth)
	if err != nil {
		t.Fatalf("RootFromWitness(MaxUint32) error = %v", err)
	}
	if want := referenceWitnessRoot(leaf, leafIndex, siblings); got != want {
		t.Fatalf("RootFromWitness(MaxUint32) = %s, want %s", got, want)
	}

	if _, err := RootFromWitness(leaf, leafIndex, siblings[:depth-1], depth); !errors.Is(err, ErrInvalidSiblingCount) {
		t.Fatalf("RootFromWitness(31 siblings) error = %v, want %v", err, ErrInvalidSiblingCount)
	}
}

func TestFixtureLoadingDoesNotDependOnWorkingDirectory(t *testing.T) {
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWorkingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change working directory: %v", err)
	}

	fixture := loadFixture(t)
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", fixture.SchemaVersion)
	}
}

func loadFixture(t *testing.T) merkleFixture {
	t.Helper()
	_, testFilename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate proof_test.go with runtime.Caller")
	}
	fixturePath := filepath.Join(filepath.Dir(testFilename), "..", "..", "tests", "fixtures", "merkle_vectors.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture merkleFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	validateFixture(t, fixture)
	return fixture
}

func validateFixture(t *testing.T, fixture merkleFixture) {
	t.Helper()
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", fixture.SchemaVersion)
	}
	if got, want := len(fixture.ZeroHashes.Hashes), int(fixture.ZeroHashes.Depth)+1; got != want {
		t.Fatalf("zero hash count = %d, want depth+1 = %d", got, want)
	}
	if got, want := len(fixture.IncrementalTree.RootsAfterAppend), len(fixture.IncrementalTree.Leaves); got != want {
		t.Fatalf("incremental root count = %d, want leaf count = %d", got, want)
	}
	if got, want := fixture.IncrementalTree.ExpectedNextIndex, uint64(len(fixture.IncrementalTree.Leaves)); got != want {
		t.Fatalf("incremental expectedNextIndex = %d, want leaf count = %d", got, want)
	}
	if got, want := fixture.IncrementalTree.ExpectedCapacity, uint64(1)<<fixture.IncrementalTree.Depth; got != want {
		t.Fatalf("incremental expectedCapacity = %d, want %d", got, want)
	}
	if got, want := len(fixture.Witness.Siblings), int(fixture.Witness.Depth); got != want {
		t.Fatalf("witness sibling count = %d, want depth = %d", got, want)
	}

	pathLength := len(fixture.RecursivePath.BlockHashes)
	if pathLength == 0 {
		t.Fatal("recursive path must contain at least one hop")
	}
	if got := len(fixture.RecursivePath.Witnesses); got != pathLength {
		t.Fatalf("recursive witness count = %d, want block hash count = %d", got, pathLength)
	}
	if got := len(fixture.RecursivePath.IntermediateRoots); got != pathLength {
		t.Fatalf("recursive intermediate root count = %d, want block hash count = %d", got, pathLength)
	}
	for i, witness := range fixture.RecursivePath.Witnesses {
		if got, want := len(witness.Siblings), int(fixture.RecursivePath.Depth); got != want {
			t.Fatalf("recursive witness %d sibling count = %d, want depth = %d", i, got, want)
		}
	}
}

func referenceFullTreeRoot(depth uint8, appendedLeaves []common.Hash) common.Hash {
	capacity := int(uint64(1) << depth)
	level := make([]common.Hash, capacity)
	copy(level, appendedLeaves)
	for len(level) > 1 {
		parents := make([]common.Hash, len(level)/2)
		for i := range parents {
			parents[i] = crypto.Keccak256Hash(level[2*i][:], level[2*i+1][:])
		}
		level = parents
	}
	return level[0]
}

func referenceWitnessRoot(leaf common.Hash, leafIndex uint32, siblings []common.Hash) common.Hash {
	root := leaf
	index := leafIndex
	for _, sibling := range siblings {
		if index&1 == 0 {
			root = crypto.Keccak256Hash(root[:], sibling[:])
		} else {
			root = crypto.Keccak256Hash(sibling[:], root[:])
		}
		index >>= 1
	}
	return root
}

func fixtureWitnesses(t *testing.T, input []fixtureWitness) []domain.MembershipWitness {
	t.Helper()
	result := make([]domain.MembershipWitness, len(input))
	for i, witness := range input {
		var err error
		result[i], err = domain.NewMembershipWitness(witness.LeafIndex, hashes(t, witness.Siblings))
		if err != nil {
			t.Fatalf("domain.NewMembershipWitness(%d): %v", i, err)
		}
	}
	return result
}

func hashes(t *testing.T, values []string) []common.Hash {
	t.Helper()
	result := make([]common.Hash, len(values))
	for i, value := range values {
		result[i] = hash(t, value)
	}
	return result
}

func hash(t *testing.T, value string) common.Hash {
	t.Helper()
	if len(value) != 66 || value[:2] != "0x" {
		t.Fatalf("fixture hash %q is not a 32-byte 0x-prefixed value", value)
	}
	raw, err := hex.DecodeString(value[2:])
	if err != nil {
		t.Fatalf("decode fixture hash %q: %v", value, err)
	}
	return common.BytesToHash(raw)
}
