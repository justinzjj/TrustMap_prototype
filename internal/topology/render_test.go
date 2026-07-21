package topology_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/params"
	"github.com/justinzjj/TrustMap_prototype/internal/config"
	"github.com/justinzjj/TrustMap_prototype/internal/topology"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const renderYAML = `version: 1
network_name: trustmap-test
defaults:
  geth_image: ethereum/client-go:v1.17.3
  foundry_image: ghcr.io/foundry-rs/foundry:v1.4.4
  block_period_seconds: 2
  confirmations: 2
  merkle_depth: 8
  direct_verifier:
    profile_id: committee-1
    authorized_signer_count: 1
    signature_checks: 1
    hash_rounds: 4
  host_ports: {base: 18545, stride: 100}
chains:
  - {name: alpha, chain_id: 31337}
  - {name: beta, chain_id: 31338, block_period_seconds: 5}
mapnodes: {p2p_enabled: true, database: sqlite}
`

type publicIdentity struct {
	Version           int      `json:"version"`
	DeployerAddress   string   `json:"deployer_address"`
	MapNodeAddress    string   `json:"mapnode_address"`
	AuthorizedSigners []string `json:"authorized_signers"`
	PeerID            string   `json:"peer_id"`
}

type verifierProfile struct {
	Version           int    `json:"version"`
	ProfileID         string `json:"profile_id"`
	ContractName      string `json:"contract_name"`
	AuthorizedSigners []struct {
		Address      string `json:"address"`
		KeystoreFile string `json:"keystore_file"`
		PasswordFile string `json:"password_file"`
	} `json:"authorized_signers"`
	SignatureChecks       int  `json:"signature_checks"`
	HashRounds            int  `json:"hash_rounds"`
	MeasuredDirectCostGas *int `json:"measured_direct_cost_gas"`
}

func TestRenderCreatesValidIdentitiesGenesisAndProfiles(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	bootstrap := readJSON[struct {
		Version int `json:"version"`
		Nodes   []struct {
			Name      string `json:"name"`
			PeerID    string `json:"peer_id"`
			Multiaddr string `json:"multiaddr"`
		} `json:"nodes"`
	}](t, filepath.Join(output, "p2p", "bootstrap.json"))
	if bootstrap.Version != 1 || len(bootstrap.Nodes) != 2 {
		t.Fatalf("unexpected bootstrap: %+v", bootstrap)
	}

	for _, chain := range cfg.Chains {
		chainDir := filepath.Join(output, "chains", chain.Name)
		identity := readJSON[publicIdentity](t, filepath.Join(chainDir, "identity.json"))
		if identity.Version != 1 || len(identity.AuthorizedSigners) != chain.DirectVerifier.AuthorizedSignerCount {
			t.Fatalf("unexpected identity for %s: %+v", chain.Name, identity)
		}
		addresses := append([]string{identity.DeployerAddress, identity.MapNodeAddress}, identity.AuthorizedSigners...)
		seen := map[string]bool{}
		for _, address := range addresses {
			if !common.IsHexAddress(address) || seen[strings.ToLower(address)] {
				t.Fatalf("invalid or duplicate EVM address %q", address)
			}
			seen[strings.ToLower(address)] = true
		}

		assertKeystore(t, filepath.Join(chainDir, "secrets", "deployer-keystore.json"), filepath.Join(chainDir, "secrets", "deployer-password"), identity.DeployerAddress)
		assertKeystore(t, filepath.Join(chainDir, "secrets", "mapnode-keystore.json"), filepath.Join(chainDir, "secrets", "mapnode-password"), identity.MapNodeAddress)
		assertKeystore(t, filepath.Join(chainDir, "secrets", "direct-signer-0-keystore.json"), filepath.Join(chainDir, "secrets", "direct-signer-0-password"), identity.AuthorizedSigners[0])

		encodedPrivateKey := strings.TrimSpace(string(readFile(t, filepath.Join(chainDir, "secrets", "p2p-private-key"))))
		marshaledPrivateKey, err := base64.StdEncoding.DecodeString(encodedPrivateKey)
		if err != nil {
			t.Fatalf("decode p2p private key: %v", err)
		}
		privateKey, err := libp2pcrypto.UnmarshalPrivateKey(marshaledPrivateKey)
		if err != nil {
			t.Fatalf("unmarshal p2p private key: %v", err)
		}
		peerID, err := peer.IDFromPrivateKey(privateKey)
		if err != nil || peerID.String() != identity.PeerID {
			t.Fatalf("peer ID mismatch: got %s, err %v", peerID, err)
		}

		var genesis core.Genesis
		if err := json.Unmarshal(readFile(t, filepath.Join(chainDir, "genesis.json")), &genesis); err != nil {
			t.Fatalf("unmarshal genesis: %v", err)
		}
		if genesis.Config.ChainID.Cmp(new(big.Int).SetUint64(chain.ChainID)) != 0 || genesis.Difficulty.Sign() != 0 || genesis.Config.TerminalTotalDifficulty.Sign() != 0 {
			t.Fatalf("invalid genesis consensus fields: %+v", genesis.Config)
		}
		for name, block := range map[string]*big.Int{
			"homestead": genesis.Config.HomesteadBlock, "eip150": genesis.Config.EIP150Block,
			"eip155": genesis.Config.EIP155Block, "eip158": genesis.Config.EIP158Block,
			"byzantium": genesis.Config.ByzantiumBlock, "constantinople": genesis.Config.ConstantinopleBlock,
			"petersburg": genesis.Config.PetersburgBlock, "istanbul": genesis.Config.IstanbulBlock,
			"berlin": genesis.Config.BerlinBlock, "london": genesis.Config.LondonBlock,
		} {
			if block == nil || block.Sign() != 0 {
				t.Fatalf("%s fork is not active at genesis: %v", name, block)
			}
		}
		if genesis.Config.ShanghaiTime == nil || *genesis.Config.ShanghaiTime != 0 || genesis.Config.CancunTime == nil || *genesis.Config.CancunTime != 0 {
			t.Fatalf("timestamp forks are not active at genesis: shanghai=%v cancun=%v", genesis.Config.ShanghaiTime, genesis.Config.CancunTime)
		}
		cancun := genesis.Config.BlobScheduleConfig.Cancun
		if cancun == nil || cancun.Target != 3 || cancun.Max != 6 || cancun.UpdateFraction != 3338477 {
			t.Fatalf("unexpected Cancun blob schedule: %+v", cancun)
		}
		if system, ok := genesis.Alloc[params.BeaconRootsAddress]; !ok || len(system.Code) == 0 {
			t.Fatalf("beacon roots system contract missing from genesis")
		}
		for _, address := range []string{identity.DeployerAddress, identity.MapNodeAddress} {
			account, ok := genesis.Alloc[common.HexToAddress(address)]
			if !ok || account.Balance == nil || account.Balance.Sign() <= 0 {
				t.Fatalf("address %s not prefunded", address)
			}
		}

		profile := readJSON[verifierProfile](t, filepath.Join(chainDir, "direct-verifier-profile.json"))
		if profile.Version != 1 || profile.ProfileID != chain.DirectVerifier.ProfileID || profile.ContractName != "ExperimentalCostedDirectVerifier" || len(profile.AuthorizedSigners) != 1 || profile.MeasuredDirectCostGas != nil {
			t.Fatalf("unexpected verifier profile: %+v", profile)
		}
		if profile.SignatureChecks != chain.DirectVerifier.SignatureChecks || profile.HashRounds != chain.DirectVerifier.HashRounds || profile.AuthorizedSigners[0].Address != identity.AuthorizedSigners[0] {
			t.Fatalf("profile does not match topology/identity: %+v", profile)
		}
		mapNode := readJSON[struct {
			Version int    `json:"version"`
			Name    string `json:"name"`
			Chains  []struct {
				Name               string `json:"name"`
				ChainID            string `json:"chain_id"`
				HTTPRPC            string `json:"http_rpc"`
				Confirmations      int    `json:"confirmations"`
				DeploymentManifest string `json:"deployment_manifest"`
				Home               bool   `json:"home"`
			} `json:"chains"`
			HomeChain struct {
				Name            string `json:"name"`
				ChainID         string `json:"chain_id"`
				HTTPRPC         string `json:"http_rpc"`
				WSRPC           string `json:"ws_rpc"`
				Confirmations   int    `json:"confirmations"`
				GatewayManifest string `json:"gateway_manifest"`
			} `json:"home_chain"`
			P2P struct {
				BootstrapFile string `json:"bootstrap_file"`
			} `json:"p2p"`
			DirectVerifier struct {
				ProfileFile string `json:"profile_file"`
			} `json:"direct_verifier"`
		}](t, filepath.Join(chainDir, "mapnode.json"))
		if mapNode.Version != 1 || mapNode.Name != chain.Name || mapNode.HomeChain.ChainID != fmt.Sprint(chain.ChainID) || mapNode.HomeChain.HTTPRPC != fmt.Sprintf("http://geth-%s:8545", chain.Name) || mapNode.HomeChain.WSRPC != fmt.Sprintf("ws://geth-%s:8546", chain.Name) || mapNode.HomeChain.Confirmations != chain.Confirmations {
			t.Fatalf("unexpected mapnode home chain: %+v", mapNode)
		}
		if mapNode.HomeChain.GatewayManifest != "/runtime/deployment/gateway-manifest.json" || mapNode.P2P.BootstrapFile != "/runtime/p2p/bootstrap.json" || mapNode.DirectVerifier.ProfileFile != "/runtime/direct-verifier-profile.json" {
			t.Fatalf("unexpected mapnode runtime paths: %+v", mapNode)
		}
		if len(mapNode.Chains) != len(cfg.Chains) {
			t.Fatalf("catalog length=%d want=%d", len(mapNode.Chains), len(cfg.Chains))
		}
		homes := 0
		for index, catalog := range mapNode.Chains {
			want := cfg.Chains[index]
			if catalog.Name != want.Name || catalog.ChainID != fmt.Sprint(want.ChainID) || catalog.HTTPRPC != fmt.Sprintf("http://geth-%s:8545", want.Name) || catalog.Confirmations != want.Confirmations || catalog.DeploymentManifest != fmt.Sprintf("/runtime/chains/%s/deployment/gateway-manifest.json", want.Name) {
				t.Fatalf("catalog[%d]=%+v", index, catalog)
			}
			if catalog.Home {
				homes++
				if catalog.Name != chain.Name {
					t.Fatalf("catalog home=%s want=%s", catalog.Name, chain.Name)
				}
			}
		}
		if homes != 1 {
			t.Fatalf("catalog homes=%d", homes)
		}
	}
}

func TestRenderPublishesMeasuredCostOnlyForExactReservedProfile(t *testing.T) {
	reserved := `version: 1
network_name: trustmap-reserved
defaults:
  direct_verifier:
    profile_id: pow-spv-3m
    authorized_signer_count: 3
    signature_checks: 3
    hash_rounds: 4497
chains:
  - {name: alpha, chain_id: 31337}
  - {name: beta, chain_id: 31338}
mapnodes: {database: sqlite}
`
	cfg := decodeTopology(t, reserved)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	profile := readJSON[verifierProfile](t, filepath.Join(output, "chains", "alpha", "direct-verifier-profile.json"))
	if profile.MeasuredDirectCostGas == nil || *profile.MeasuredDirectCostGas != 3000096 {
		t.Fatalf("reserved measured cost = %v", profile.MeasuredDirectCostGas)
	}

	custom := strings.Replace(reserved, "profile_id: pow-spv-3m", "profile_id: custom-same-work", 1)
	cfg = decodeTopology(t, custom)
	output = t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("Render(custom) error = %v", err)
	}
	profile = readJSON[verifierProfile](t, filepath.Join(output, "chains", "alpha", "direct-verifier-profile.json"))
	if profile.MeasuredDirectCostGas != nil {
		t.Fatalf("custom profile received reserved measured cost: %d", *profile.MeasuredDirectCostGas)
	}
}

func TestRenderReusesIdentityAndIsByteDeterministic(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("first Render() error = %v", err)
	}
	before := snapshot(t, output)
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("second Render() error = %v", err)
	}
	after := snapshot(t, output)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if !bytes.Equal(content, after[path]) {
			t.Fatalf("file changed across render: %s", path)
		}
	}
}

func TestRenderRejectsIncompleteExistingIdentity(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("first Render() error = %v", err)
	}
	missing := filepath.Join(output, "chains", "alpha", "secrets", "mapnode-password")
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	if err := topology.Render(cfg, output); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-secret error, got %v", err)
	}
}

func TestRenderUsesAtomicFilesPermissionsAndNoSecretLeakage(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	var public bytes.Buffer
	err := filepath.Walk(output, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if strings.HasSuffix(path, string(filepath.Separator)+"secrets") && info.Mode().Perm() != 0750 {
				t.Errorf("secret directory mode %s = %o, want 750", path, info.Mode().Perm())
			}
			return nil
		}
		if strings.Contains(info.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", path)
		}
		content := readFile(t, path)
		if strings.Contains(path, string(filepath.Separator)+"secrets"+string(filepath.Separator)) {
			if info.Mode().Perm() != 0640 {
				t.Errorf("secret mode %s = %o, want 640", path, info.Mode().Perm())
			}
			for _, candidate := range []string{"privateKey", "private_key", "BEGIN PRIVATE KEY"} {
				if bytes.Contains(content, []byte(candidate)) && !strings.Contains(info.Name(), "keystore") {
					t.Errorf("raw private key marker in %s", path)
				}
			}
		} else {
			if info.Mode().Perm() != 0644 {
				t.Errorf("ordinary mode %s = %o", path, info.Mode().Perm())
			}
			public.Write(content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, chain := range cfg.Chains {
		secretDir := filepath.Join(output, "chains", chain.Name, "secrets")
		entries, err := os.ReadDir(secretDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			secret := bytes.TrimSpace(readFile(t, filepath.Join(secretDir, entry.Name())))
			if len(secret) > 0 && bytes.Contains(public.Bytes(), secret) {
				t.Fatalf("secret %s leaked into public metadata", entry.Name())
			}
		}
	}
}

func TestRenderRepairsExistingSecretPermissions(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("first Render() error = %v", err)
	}
	secretDir := filepath.Join(output, "chains", "alpha", "secrets")
	secretFile := filepath.Join(secretDir, "mapnode-password")
	if err := os.Chmod(secretDir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secretFile, 0666); err != nil {
		t.Fatal(err)
	}

	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("second Render() error = %v", err)
	}
	assertMode(t, secretDir, 0750)
	assertMode(t, secretFile, 0640)
}

func decodeTopology(t *testing.T, input string) *config.Topology {
	t.Helper()
	cfg, err := config.Decode(strings.NewReader(input))
	if err != nil {
		t.Fatalf("config.Decode() error = %v", err)
	}
	return cfg
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(readFile(t, path), &value); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return value
}

func assertKeystore(t *testing.T, keystorePath, passwordPath, wantAddress string) {
	t.Helper()
	password := strings.TrimSpace(string(readFile(t, passwordPath)))
	key, err := keystore.DecryptKey(readFile(t, keystorePath), password)
	if err != nil {
		t.Fatalf("decrypt %s: %v", keystorePath, err)
	}
	if !strings.EqualFold(key.Address.Hex(), wantAddress) {
		t.Fatalf("keystore address = %s, want %s", key.Address.Hex(), wantAddress)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}

func snapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	paths := make([]string, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			paths = append(paths, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	result := make(map[string][]byte, len(paths))
	for _, path := range paths {
		relative, _ := filepath.Rel(root, path)
		result[relative] = readFile(t, path)
	}
	return result
}
