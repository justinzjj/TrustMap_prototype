package chain_test

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type readerRPC struct {
	chainID   *big.Int
	target    *types.Header
	head      *types.Header
	code      []byte
	root      common.Hash
	callBlock common.Hash
	callData  []byte
}

type switchingHeaderRPC struct {
	first, replacement, head *types.Header
	targetReads              int
}

func (rpc *switchingHeaderRPC) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	if number == nil {
		return rpc.head, nil
	}
	rpc.targetReads++
	if rpc.targetReads == 1 {
		return rpc.first, nil
	}
	return rpc.replacement, nil
}

func (rpc *readerRPC) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	if number == nil {
		return rpc.head, nil
	}
	if rpc.target != nil && number.Cmp(rpc.target.Number) == 0 {
		return rpc.target, nil
	}
	return nil, errors.New("unknown block")
}

func (rpc *readerRPC) ChainID(context.Context) (*big.Int, error) {
	return new(big.Int).Set(rpc.chainID), nil
}

func (rpc *readerRPC) CodeAtHash(_ context.Context, _ common.Address, hash common.Hash) ([]byte, error) {
	if rpc.target == nil || hash != rpc.target.Hash() {
		return nil, errors.New("code requested without exact historical block hash")
	}
	return append([]byte(nil), rpc.code...), nil
}

func (rpc *readerRPC) CallContractAtHash(_ context.Context, call ethereum.CallMsg, hash common.Hash) ([]byte, error) {
	if rpc.target == nil || hash != rpc.target.Hash() {
		return nil, errors.New("non-canonical hash tag forbidden")
	}
	rpc.callBlock = hash
	rpc.callData = append([]byte(nil), call.Data...)
	return rpc.root[:], nil
}

func TestTrustRootReaderObservesExactConfirmedHistoricalBlock(t *testing.T) {
	target := &types.Header{Number: big.NewInt(40), ParentHash: common.HexToHash("0x39"), Extra: []byte("target")}
	head := &types.Header{Number: big.NewInt(43), ParentHash: common.HexToHash("0x42"), Extra: []byte("head")}
	code := []byte{0x60, 0x00, 0x60, 0x01}
	root := common.HexToHash("0xabcdef")
	rpc := &readerRPC{chainID: big.NewInt(10001), target: target, head: head, code: code, root: root}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(40)
	gateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	client, err := chain.NewGatewayClient(rpc, chain.GatewayDeployment{ChainID: chainID, Address: gateway, CodeHash: crypto.Keccak256Hash(code)})
	if err != nil {
		t.Fatal(err)
	}
	reader := chain.NewTrustRootReader(chain.NewCanonicalBlockReader(rpc), client)
	observation, err := reader.Observe(context.Background(), height, target.Hash(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if observation.BlockHash != target.Hash() || observation.TrustRoot.Hash != root || observation.ConfirmedHeadHash != head.Hash() {
		t.Fatalf("observation = %+v", observation)
	}
	if rpc.callBlock != target.Hash() {
		t.Fatalf("eth_call block = %v, want %v", rpc.callBlock, target.Hash())
	}
	if string(rpc.callData) != string(chainabi.EncodeCurrentTrustRootCall()) {
		t.Fatalf("eth_call data = %x", rpc.callData)
	}
}

func TestTrustRootReaderObservesZeroInitialTrustRootAtExactBlockHash(t *testing.T) {
	target := &types.Header{Number: big.NewInt(40), Extra: []byte("initial")}
	head := &types.Header{Number: big.NewInt(42), Extra: []byte("confirmed")}
	code := []byte{0x60, 0x00}
	rpc := &readerRPC{chainID: big.NewInt(10001), target: target, head: head, code: code, root: common.Hash{}}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(40)
	gateway := common.HexToAddress("0x1000000000000000000000000000000000000001")
	client, err := chain.NewGatewayClient(rpc, chain.GatewayDeployment{ChainID: chainID, Address: gateway, CodeHash: crypto.Keccak256Hash(code)})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := chain.NewTrustRootReader(chain.NewCanonicalBlockReader(rpc), client).Observe(context.Background(), height, target.Hash(), 2)
	if err != nil {
		t.Fatalf("zero initial TrustRoot observation failed: %v", err)
	}
	if observation.TrustRoot.Hash != (common.Hash{}) || rpc.callBlock != target.Hash() {
		t.Fatalf("observation=%+v callBlock=%s", observation, rpc.callBlock)
	}
}

func TestTrustRootReaderFailsClosedOnCanonicalConfirmationChainAndCodeMismatch(t *testing.T) {
	target := &types.Header{Number: big.NewInt(40), Extra: []byte("target")}
	head := &types.Header{Number: big.NewInt(41), Extra: []byte("head")}
	code := []byte{1, 2, 3}
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(40)
	gateway := common.HexToAddress("0x1000000000000000000000000000000000000001")

	tests := []struct {
		name        string
		actualChain int64
		codeHash    common.Hash
		blockHash   common.Hash
		depth       uint64
	}{
		{"canonical hash", 10001, crypto.Keccak256Hash(code), common.HexToHash("0xbad"), 1},
		{"confirmations", 10001, crypto.Keccak256Hash(code), target.Hash(), 2},
		{"chain ID", 10002, crypto.Keccak256Hash(code), target.Hash(), 1},
		{"Gateway code", 10001, common.HexToHash("0xbad"), target.Hash(), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rpc := &readerRPC{chainID: big.NewInt(test.actualChain), target: target, head: head, code: code, root: common.HexToHash("0x1")}
			client, err := chain.NewGatewayClient(rpc, chain.GatewayDeployment{ChainID: chainID, Address: gateway, CodeHash: test.codeHash})
			if err != nil {
				t.Fatal(err)
			}
			reader := chain.NewTrustRootReader(chain.NewCanonicalBlockReader(rpc), client)
			if _, err := reader.Observe(context.Background(), height, test.blockHash, test.depth); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}

func TestCanonicalBlockReaderRejectsTargetReorgBetweenHeadChecks(t *testing.T) {
	first := &types.Header{Number: big.NewInt(40), ParentHash: common.HexToHash("0x39"), Extra: []byte("fork-a")}
	replacement := &types.Header{Number: big.NewInt(40), ParentHash: common.HexToHash("0x390"), Extra: []byte("fork-b")}
	head := &types.Header{Number: big.NewInt(42), ParentHash: common.HexToHash("0x41")}
	height, _ := domain.NewBlockHeight(40)
	rpc := &switchingHeaderRPC{first: first, replacement: replacement, head: head}
	block, err := chain.NewCanonicalBlockReader(rpc).Confirmed(context.Background(), height, first.Hash(), 2)
	if !errors.Is(err, chain.ErrCanonicalBlockMismatch) {
		t.Fatalf("TOCTOU block=%+v err=%v", block, err)
	}
}

func TestNewTrustRootReaderForChainStrictlyLoadsRemoteManifestLazily(t *testing.T) {
	manifest := `{"version":1,"status":"deployed","chainId":"10001","deploymentBlock":5,"merkleDepth":8,"gateway":"0x1000000000000000000000000000000000000001","directVerifier":"0x2000000000000000000000000000000000000002","profileId":"p","authorizedSigners":[],"signatureChecks":1,"hashRounds":0,"measuredDirectCostGas":null,"codeHashes":{"gateway":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","directVerifier":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`
	path := filepath.Join(t.TempDir(), "gateway-manifest.json")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := catalogChain(t, "alpha", 10001, true)
	entry.DeploymentManifest = path
	rpc := &readerRPC{chainID: big.NewInt(10001)}
	reader, deployment, block, err := chain.NewTrustRootReaderForChain(entry, rpc)
	if err != nil || reader == nil || deployment.ChainID != entry.ChainID || deployment.CodeHash != common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") || block.BigInt().Cmp(big.NewInt(5)) != 0 {
		t.Fatalf("factory reader=%v deployment=%+v block=%s err=%v", reader, deployment, block.BigInt(), err)
	}
	runtimeConfig, err := chain.LoadGatewayDeploymentConfig(entry)
	if err != nil || runtimeConfig.Deployment != deployment || runtimeConfig.DeploymentBlock != block || runtimeConfig.MerkleDepth != 8 || runtimeConfig.PathStepCostGas != 30_713 {
		t.Fatalf("runtime config=%+v err=%v", runtimeConfig, err)
	}

	wrongChain := []byte(string([]byte(manifest)))
	wrongChain = []byte(strings.Replace(string(wrongChain), `"chainId":"10001"`, `"chainId":"10002"`, 1))
	_ = os.WriteFile(path, wrongChain, 0o600)
	if _, _, _, err := chain.NewTrustRootReaderForChain(entry, rpc); err == nil {
		t.Fatal("remote manifest chain ID mismatch accepted")
	}
	badHash := strings.Replace(manifest, `"gateway":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"gateway":"0x1234"`, 1)
	_ = os.WriteFile(path, []byte(badHash), 0o600)
	if _, _, _, err := chain.NewTrustRootReaderForChain(entry, rpc); err == nil {
		t.Fatal("remote manifest Gateway code hash mismatch accepted")
	}
}

func TestRemoteManifestLimitDoesNotHideTrailingJSON(t *testing.T) {
	manifest := []byte(`{"version":1,"status":"deployed","chainId":"10001","deploymentBlock":5,"merkleDepth":8,"gateway":"0x1000000000000000000000000000000000000001","directVerifier":"0x2000000000000000000000000000000000000002","profileId":"p","authorizedSigners":[],"signatureChecks":1,"hashRounds":0,"measuredDirectCostGas":null,"codeHashes":{"gateway":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","directVerifier":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`)
	const limit = 1 << 20
	manifest = append(manifest, bytes.Repeat([]byte{' '}, limit-len(manifest))...)
	path := filepath.Join(t.TempDir(), "gateway-manifest.json")
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := catalogChain(t, "alpha", 10001, true)
	entry.DeploymentManifest = path
	rpc := &readerRPC{chainID: big.NewInt(10001)}
	if _, _, _, err := chain.NewTrustRootReaderForChain(entry, rpc); err != nil {
		t.Fatalf("valid manifest at exact limit rejected: %v", err)
	}
	manifest = append(manifest, []byte(`{}`)...)
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := chain.NewTrustRootReaderForChain(entry, rpc); err == nil {
		t.Fatal("second JSON value beyond manifest limit was hidden")
	}
}
