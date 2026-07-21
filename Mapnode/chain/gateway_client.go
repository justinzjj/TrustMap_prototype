package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrRemoteChainIDMismatch = errors.New("remote RPC chain ID mismatch")
	ErrGatewayCodeMismatch   = errors.New("Gateway code hash mismatch")
)

type gatewayRPC interface {
	ChainID(context.Context) (*big.Int, error)
	CodeAtHash(context.Context, common.Address, common.Hash) ([]byte, error)
	CallContractAtHash(context.Context, ethereum.CallMsg, common.Hash) ([]byte, error)
}

type TrustRootRPC interface {
	canonicalHeaderRPC
	gatewayRPC
}

type GatewayDeployment struct {
	ChainID  domain.ChainID
	Address  common.Address
	CodeHash common.Hash
}

type GatewayClient struct {
	rpc        gatewayRPC
	deployment GatewayDeployment
}

func NewGatewayClient(rpc gatewayRPC, deployment GatewayDeployment) (*GatewayClient, error) {
	if rpc == nil {
		return nil, errors.New("Gateway RPC is required")
	}
	if err := deployment.ChainID.Validate(); err != nil {
		return nil, err
	}
	if deployment.Address == (common.Address{}) || deployment.CodeHash == (common.Hash{}) {
		return nil, errors.New("Gateway address and code hash are required")
	}
	return &GatewayClient{rpc: rpc, deployment: deployment}, nil
}

func (client *GatewayClient) CurrentTrustRoot(ctx context.Context, block CanonicalBlock) (trustview.TrustRoot, error) {
	chainID, err := client.rpc.ChainID(ctx)
	if err != nil {
		return trustview.TrustRoot{}, fmt.Errorf("read remote chain ID: %w", err)
	}
	if chainID == nil || chainID.Cmp(client.deployment.ChainID.BigInt()) != 0 {
		return trustview.TrustRoot{}, ErrRemoteChainIDMismatch
	}
	code, err := client.rpc.CodeAtHash(ctx, client.deployment.Address, block.Hash)
	if err != nil {
		return trustview.TrustRoot{}, fmt.Errorf("read historical Gateway code: %w", err)
	}
	if len(code) == 0 || crypto.Keccak256Hash(code) != client.deployment.CodeHash {
		return trustview.TrustRoot{}, ErrGatewayCodeMismatch
	}
	encoded, err := client.rpc.CallContractAtHash(ctx, ethereum.CallMsg{To: &client.deployment.Address, Data: chainabi.EncodeCurrentTrustRootCall()}, block.Hash)
	if err != nil {
		return trustview.TrustRoot{}, fmt.Errorf("call historical currentTrustRoot: %w", err)
	}
	root, err := chainabi.DecodeCurrentTrustRootResult(encoded)
	if err != nil {
		return trustview.TrustRoot{}, err
	}
	return trustview.TrustRoot{Hash: root}, nil
}

func (client *GatewayClient) Deployment() GatewayDeployment { return client.deployment }

type gatewayManifest struct {
	Version               int      `json:"version"`
	Status                string   `json:"status"`
	ChainID               string   `json:"chainId"`
	DeploymentBlock       uint64   `json:"deploymentBlock"`
	MerkleDepth           uint8    `json:"merkleDepth"`
	Gateway               string   `json:"gateway"`
	DirectVerifier        string   `json:"directVerifier"`
	ProfileID             string   `json:"profileId"`
	AuthorizedSigners     []string `json:"authorizedSigners"`
	SignatureChecks       uint32   `json:"signatureChecks"`
	HashRounds            uint32   `json:"hashRounds"`
	MeasuredDirectCostGas *uint64  `json:"measuredDirectCostGas"`
	CodeHashes            struct {
		Gateway        string `json:"gateway"`
		DirectVerifier string `json:"directVerifier"`
	} `json:"codeHashes"`
	Transactions json.RawMessage `json:"transactions,omitempty"`
}

func NewTrustRootReaderForChain(entry Chain, rpc TrustRootRPC) (*TrustRootReader, GatewayDeployment, domain.BlockHeight, error) {
	if err := validateChain(entry); err != nil {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, err
	}
	file, err := os.Open(entry.DeploymentManifest)
	if err != nil {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, fmt.Errorf("open Gateway deployment manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest gatewayManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, fmt.Errorf("decode Gateway deployment manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, errors.New("Gateway deployment manifest contains trailing JSON")
	}
	manifestChainID, ok := new(big.Int).SetString(manifest.ChainID, 10)
	if manifest.Version != 1 || manifest.Status != "deployed" || !ok || manifestChainID.String() != manifest.ChainID || manifestChainID.Cmp(entry.ChainID.BigInt()) != 0 {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, errors.New("Gateway deployment manifest chain identity mismatch")
	}
	if manifest.DeploymentBlock == 0 || !common.IsHexAddress(manifest.Gateway) {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, errors.New("Gateway deployment manifest is incomplete")
	}
	decodedHash := common.FromHex(manifest.CodeHashes.Gateway)
	if len(decodedHash) != common.HashLength || !strings.HasPrefix(manifest.CodeHashes.Gateway, "0x") {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, errors.New("Gateway deployment manifest code hash must be 32 bytes")
	}
	deployment := GatewayDeployment{ChainID: entry.ChainID, Address: common.HexToAddress(manifest.Gateway), CodeHash: common.BytesToHash(decodedHash)}
	block, err := domain.NewBlockHeight(manifest.DeploymentBlock)
	if err != nil {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, err
	}
	client, err := NewGatewayClient(rpc, deployment)
	if err != nil {
		return nil, GatewayDeployment{}, domain.BlockHeight{}, err
	}
	return NewTrustRootReader(NewCanonicalBlockReader(rpc), client), deployment, block, nil
}
