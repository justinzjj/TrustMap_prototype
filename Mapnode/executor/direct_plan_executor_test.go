package executor_test

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/executor"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type digestCaller struct {
	wantTo   common.Address
	wantData []byte
	digest   common.Hash
	calls    int
}

func (caller *digestCaller) CallContract(_ context.Context, message ethereum.CallMsg, block *big.Int) ([]byte, error) {
	caller.calls++
	if message.To == nil || *message.To != caller.wantTo || string(message.Data) != string(caller.wantData) || block != nil {
		return nil, ethereum.NotFound
	}
	return caller.digest[:], nil
}

func TestDirectExecutorSignsReturnedDigestOnceInProfileOrder(t *testing.T) {
	keys := []*ecdsa.PrivateKey{mustKey(t), mustKey(t), mustKey(t)}
	signers := make([]executor.AuthorizedSigner, len(keys))
	for index, key := range keys {
		signers[index] = executor.AuthorizedSigner{Address: crypto.PubkeyToAddress(key.PublicKey), PrivateKey: key}
	}
	verifier := common.HexToAddress("0x2222222222222222222222222222222222222222")
	source, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(42)
	blockHash := common.HexToHash("0x1234")
	root := common.HexToHash("0x5678")
	digest := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	caller := &digestCaller{
		wantTo:   verifier,
		wantData: chainabi.EncodeAttestationDigestCall(source.BigInt(), height.BigInt(), blockHash, root),
		digest:   digest,
	}
	worker, err := executor.NewDirectPlanExecutor(caller, verifier, signers, 2)
	if err != nil {
		t.Fatal(err)
	}
	proof, gotDigest, err := worker.BuildProof(context.Background(), executor.DirectProofInput{
		SourceChainID: source, SourceHeight: height, SourceBlockHash: blockHash,
		SourceTrustRoot: trustview.TrustRoot{Hash: root},
	})
	if err != nil || gotDigest != digest || caller.calls != 1 {
		t.Fatalf("digest=%s calls=%d err=%v", gotDigest, caller.calls, err)
	}
	decodedRoot, signatures, err := chainabi.DecodeDirectProof(proof)
	if err != nil || decodedRoot != root || len(signatures) != 2 {
		t.Fatalf("root=%s signatures=%d err=%v", decodedRoot, len(signatures), err)
	}
	for index, signature := range signatures {
		if len(signature) != 65 || (signature[64] != 27 && signature[64] != 28) {
			t.Fatalf("signature %d has invalid V: %x", index, signature)
		}
		recovery := append([]byte(nil), signature...)
		recovery[64] -= 27
		publicKey, recoverErr := crypto.SigToPub(digest[:], recovery)
		if recoverErr != nil || crypto.PubkeyToAddress(*publicKey) != signers[index].Address {
			t.Fatalf("signature %d was not a direct signature of returned digest: %v", index, recoverErr)
		}
	}
}

func TestDirectExecutorRejectsSignerOrderOrCheckReduction(t *testing.T) {
	key := mustKey(t)
	address := crypto.PubkeyToAddress(key.PublicKey)
	if _, err := executor.NewDirectPlanExecutor(&digestCaller{}, common.HexToAddress("0x1"), []executor.AuthorizedSigner{{Address: common.HexToAddress("0x2"), PrivateKey: key}}, 1); err == nil {
		t.Fatal("signer key/address mismatch accepted")
	}
	if _, err := executor.NewDirectPlanExecutor(&digestCaller{}, common.HexToAddress("0x1"), []executor.AuthorizedSigner{{Address: address, PrivateKey: key}}, 0); err == nil {
		t.Fatal("zero checks accepted")
	}
	if _, err := executor.NewDirectPlanExecutor(&digestCaller{}, common.HexToAddress("0x1"), []executor.AuthorizedSigner{{Address: address, PrivateKey: key}}, 2); err == nil {
		t.Fatal("checks exceeding ordered signer count accepted")
	}
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}
