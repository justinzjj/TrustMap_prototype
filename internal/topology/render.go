package topology

import (
	"fmt"
	"math/big"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/internal/config"
)

type verifierProfile struct {
	Version               int                `json:"version"`
	ProfileID             string             `json:"profile_id"`
	ContractName          string             `json:"contract_name"`
	AuthorizedSigners     []authorizedSigner `json:"authorized_signers"`
	SignatureChecks       int                `json:"signature_checks"`
	HashRounds            int                `json:"hash_rounds"`
	MeasuredDirectCostGas *uint64            `json:"measured_direct_cost_gas"`
}

type authorizedSigner struct {
	Address      string `json:"address"`
	KeystoreFile string `json:"keystore_file"`
	PasswordFile string `json:"password_file"`
}

type bootstrap struct {
	Version int             `json:"version"`
	Nodes   []bootstrapNode `json:"nodes"`
}

type bootstrapNode struct {
	Name      string `json:"name"`
	PeerID    string `json:"peer_id"`
	Multiaddr string `json:"multiaddr"`
}

func Render(topology *config.Topology, output string) error {
	if topology == nil {
		return fmt.Errorf("topology is required")
	}
	if err := topology.Validate(); err != nil {
		return fmt.Errorf("invalid topology: %w", err)
	}
	if err := ensureDir(output, 0755); err != nil {
		return err
	}
	chainsDir := filepath.Join(output, "chains")
	if err := ensureDir(chainsDir, 0755); err != nil {
		return err
	}
	p2pDir := filepath.Join(output, "p2p")
	if err := ensureDir(p2pDir, 0755); err != nil {
		return err
	}

	identities := make(map[string]identity, len(topology.Chains))
	for _, chain := range topology.Chains {
		chainDir := filepath.Join(chainsDir, chain.Name)
		if err := ensureDir(chainDir, 0755); err != nil {
			return err
		}
		chainIdentity, err := loadOrCreateIdentity(chainDir, chain.DirectVerifier.AuthorizedSignerCount)
		if err != nil {
			return fmt.Errorf("prepare identity for chain %q: %w", chain.Name, err)
		}
		identities[chain.Name] = chainIdentity
		if err := ensureDir(filepath.Join(chainDir, "deployment"), 0755); err != nil {
			return err
		}
		if err := renderChain(chainDir, chain, chainIdentity, topology.MapNodes); err != nil {
			return fmt.Errorf("render chain %q: %w", chain.Name, err)
		}
	}

	bootstrapDocument := bootstrap{Version: 1}
	for _, chain := range topology.Chains {
		chainIdentity := identities[chain.Name]
		bootstrapDocument.Nodes = append(bootstrapDocument.Nodes, bootstrapNode{
			Name:      chain.Name,
			PeerID:    chainIdentity.PeerID,
			Multiaddr: fmt.Sprintf("/dns4/mapnode-%s/tcp/9000/p2p/%s", chain.Name, chainIdentity.PeerID),
		})
	}
	content, err := marshalJSON(bootstrapDocument)
	if err != nil {
		return fmt.Errorf("encode bootstrap metadata: %w", err)
	}
	if err := atomicWrite(filepath.Join(p2pDir, "bootstrap.json"), content, 0644); err != nil {
		return err
	}
	return renderCompose(topology, identities, output)
}

func renderChain(chainDir string, chain config.Chain, chainIdentity identity, mapNodes config.MapNodes) error {
	genesis := core.DeveloperGenesisBlock(30_000_000, nil)
	chainConfig := *genesis.Config
	chainConfig.ChainID = new(big.Int).SetUint64(chain.ChainID)
	chainConfig.TerminalTotalDifficulty = big.NewInt(0)
	genesis.Config = &chainConfig
	genesis.Difficulty = big.NewInt(0)
	prefund := new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)
	genesis.Alloc[common.HexToAddress(chainIdentity.DeployerAddress)] = types.Account{Balance: new(big.Int).Set(prefund)}
	genesis.Alloc[common.HexToAddress(chainIdentity.MapNodeAddress)] = types.Account{Balance: new(big.Int).Set(prefund)}
	content, err := marshalJSON(genesis)
	if err != nil {
		return fmt.Errorf("encode genesis: %w", err)
	}
	if err := atomicWrite(filepath.Join(chainDir, "genesis.json"), content, 0644); err != nil {
		return err
	}

	profile := verifierProfile{
		Version:         1,
		ProfileID:       chain.DirectVerifier.ProfileID,
		ContractName:    "ExperimentalCostedDirectVerifier",
		SignatureChecks: chain.DirectVerifier.SignatureChecks,
		HashRounds:      chain.DirectVerifier.HashRounds,
	}
	for index, address := range chainIdentity.AuthorizedSigners {
		profile.AuthorizedSigners = append(profile.AuthorizedSigners, authorizedSigner{
			Address:      address,
			KeystoreFile: fmt.Sprintf("/run/secrets/direct-signer-%d-keystore.json", index),
			PasswordFile: fmt.Sprintf("/run/secrets/direct-signer-%d-password", index),
		})
	}
	content, err = marshalJSON(profile)
	if err != nil {
		return fmt.Errorf("encode direct verifier profile: %w", err)
	}
	if err := atomicWrite(filepath.Join(chainDir, "direct-verifier-profile.json"), content, 0644); err != nil {
		return err
	}

	mapNode := mapNodeConfig(chain, chainIdentity, mapNodes)
	content, err = marshalJSON(mapNode)
	if err != nil {
		return fmt.Errorf("encode mapnode config: %w", err)
	}
	return atomicWrite(filepath.Join(chainDir, "mapnode.json"), content, 0644)
}

func mapNodeConfig(chain config.Chain, chainIdentity identity, mapNodes config.MapNodes) map[string]any {
	return map[string]any{
		"version": 1,
		"name":    chain.Name,
		"home_chain": map[string]any{
			"name": chain.Name, "chain_id": new(big.Int).SetUint64(chain.ChainID).String(),
			"http_rpc":         fmt.Sprintf("http://geth-%s:8545", chain.Name),
			"ws_rpc":           fmt.Sprintf("ws://geth-%s:8546", chain.Name),
			"confirmations":    chain.Confirmations,
			"gateway_manifest": "/runtime/deployment/gateway-manifest.json",
		},
		"api": map[string]any{"listen": "0.0.0.0:8080"},
		"p2p": map[string]any{
			"enabled": mapNodes.P2PEnabled, "listen": "/ip4/0.0.0.0/tcp/9000",
			"private_key_file": "/run/secrets/p2p-private-key",
			"bootstrap_file":   "/runtime/p2p/bootstrap.json",
		},
		"database": map[string]any{"driver": mapNodes.Database, "path": "/runtime/data/mapnode.db"},
		"signer": map[string]any{
			"keystore_file": "/run/secrets/mapnode-keystore.json",
			"password_file": "/run/secrets/mapnode-password",
		},
		"direct_verifier": map[string]any{"profile_file": "/runtime/direct-verifier-profile.json"},
	}
}
