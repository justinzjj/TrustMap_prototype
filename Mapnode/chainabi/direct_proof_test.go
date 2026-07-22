package chainabi_test

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestExecutionSelectorsMatchSolidityGoldenValues(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"verifyDirectAndRecord", chainabi.EncodeVerifyDirectAndRecordCall(domain.RequestID{}, nil), "bb17a440"},
		{"verifyPathAndRecord", chainabi.EncodeVerifyPathAndRecordCall(domain.RequestID{}, chainabi.PathCallProof{}), "f8a3c25c"},
		{"attestationDigest", chainabi.EncodeAttestationDigestCall(big.NewInt(1), big.NewInt(1), common.Hash{}, common.Hash{}), "329757ef"},
		{"requestResolved", chainabi.EncodeRequestResolvedCall(domain.RequestID{}), "44632cf3"},
		{"hasTrustRootUpdateAtBlock", chainabi.EncodeHasTrustRootUpdateAtBlockCall(big.NewInt(1)), "11e9d3cd"},
		{"directVerifier", chainabi.EncodeDirectVerifierCall(), "d85ac10a"},
		{"gateway", chainabi.EncodeVerifierGatewayCall(), "116191b6"},
		{"authorizedSignerCount", chainabi.EncodeAuthorizedSignerCountCall(), "e3a05324"},
		{"authorizedSigners", chainabi.EncodeAuthorizedSignerCall(big.NewInt(0)), "a2b84460"},
		{"signatureChecks", chainabi.EncodeSignatureChecksCall(), "f49675b7"},
		{"hashRounds", chainabi.EncodeHashRoundsCall(), "1b3ee237"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.got) < 4 || hex.EncodeToString(test.got[:4]) != test.want {
				t.Fatalf("selector = %x, want %s", test.got, test.want)
			}
		})
	}
}

func TestVerifyDirectAndRecordAndProofMatchSolidityGoldenEncoding(t *testing.T) {
	requestID := domain.RequestID(common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"))
	call := chainabi.EncodeVerifyDirectAndRecordCall(requestID, []byte{0xaa, 0xbb})
	want := "bb17a440111111111111111111111111111111111111111111111111111111111111111100000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000002aabb000000000000000000000000000000000000000000000000000000000000"
	if hex.EncodeToString(call) != want {
		t.Fatalf("direct calldata mismatch\n got %x\nwant %s", call, want)
	}

	root := common.HexToHash("0x1234")
	proof, err := chainabi.EncodeDirectProof(root, [][]byte{{1, 2}, {3, 4, 5}})
	if err != nil {
		t.Fatal(err)
	}
	decodedRoot, signatures, err := chainabi.DecodeDirectProof(proof)
	if err != nil || decodedRoot != root || len(signatures) != 2 || hex.EncodeToString(signatures[0]) != "0102" || hex.EncodeToString(signatures[1]) != "030405" {
		t.Fatalf("decoded proof root=%s signatures=%x err=%v", decodedRoot, signatures, err)
	}
}

func TestPathProofCallPreservesPersistedSolidityOrderAndTupleOffsets(t *testing.T) {
	requestID := domain.RequestID(common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"))
	proof := chainabi.PathCallProof{
		BaseTrustRoot: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		BlockHashes: []common.Hash{
			common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		},
		Witnesses: []chainabi.SolidityWitness{
			{LeafIndex: 1, Siblings: []common.Hash{common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")}},
			{LeafIndex: 2, Siblings: []common.Hash{
				common.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
				common.HexToHash("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
			}},
		},
	}
	call := chainabi.EncodeVerifyPathAndRecordCall(requestID, proof)
	want := "f8a3c25c11111111111111111111111111111111111111111111111111111111111111112222222222222222222222222222222222222222222222222222222222222222000000000000000000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000002aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb0000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000c0000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000001cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc000000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000002ddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if hex.EncodeToString(call) != want {
		t.Fatalf("path calldata mismatch\n got %x\nwant %s", call, want)
	}
}

func TestCheckedCallResultsRejectNonCanonicalWords(t *testing.T) {
	address := common.HexToAddress("0x1234567890123456789012345678901234567890")
	word := make([]byte, 32)
	copy(word[12:], address[:])
	if got, err := chainabi.DecodeAddressResult(word); err != nil || got != address {
		t.Fatalf("address result = %s, %v", got, err)
	}
	word[0] = 1
	if _, err := chainabi.DecodeAddressResult(word); err == nil {
		t.Fatal("non-canonical address word accepted")
	}
	if _, err := chainabi.DecodeBoolResult(common.HexToHash("0x2").Bytes()); err == nil {
		t.Fatal("non-canonical bool accepted")
	}
	if value, err := chainabi.DecodeUint32Result(common.HexToHash("0xffffffff").Bytes()); err != nil || value != ^uint32(0) {
		t.Fatalf("uint32 = %d, %v", value, err)
	}
	tooLarge := common.HexToHash("0x100000000").Bytes()
	if _, err := chainabi.DecodeUint32Result(tooLarge); err == nil {
		t.Fatal("overflowing uint32 accepted")
	}
}
