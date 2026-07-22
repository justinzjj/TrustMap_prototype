package topology_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/justinzjj/TrustMap_prototype/internal/config"
	"github.com/justinzjj/TrustMap_prototype/internal/topology"
	"go.yaml.in/yaml/v3"
)

func TestExampleTopologiesRenderOneToOne(t *testing.T) {
	tests := []struct {
		file       string
		chainCount int
	}{
		{"topology.yaml", 3},
		{"topology-2.yaml", 2},
		{"topology-21.yaml", 21},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			cfg, err := config.LoadFile(filepath.Join("..", "..", "configs", tc.file))
			if err != nil {
				t.Fatalf("LoadFile() error = %v", err)
			}
			if len(cfg.Chains) != tc.chainCount {
				t.Fatalf("chain count = %d, want %d", len(cfg.Chains), tc.chainCount)
			}
			periods := map[int]bool{}
			names, ids, ports := map[string]bool{}, map[uint64]bool{}, map[int]bool{}
			for _, chain := range cfg.Chains {
				if got := chain.DirectVerifier; got.ProfileID != "pow-spv-3m" || got.AuthorizedSignerCount != 3 || got.SignatureChecks != 3 || got.HashRounds != 4497 {
					t.Fatalf("example chain %s uses non-calibrated profile: %+v", chain.Name, got)
				}
				periods[chain.BlockPeriodSeconds] = true
				if names[chain.Name] || ids[chain.ChainID] {
					t.Fatalf("duplicate chain identity: %+v", chain)
				}
				names[chain.Name], ids[chain.ChainID] = true, true
				for _, port := range []int{chain.HostPorts.HTTP, chain.HostPorts.WS, chain.HostPorts.MapNodeAPI, chain.HostPorts.MapNodeP2P} {
					if ports[port] {
						t.Fatalf("duplicate host port %d", port)
					}
					ports[port] = true
				}
			}
			if len(periods) < 2 {
				t.Fatalf("example must demonstrate a block-period override: %v", periods)
			}

			output := t.TempDir()
			if err := topology.Render(cfg, output); err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			profile := readJSON[verifierProfile](t, filepath.Join(output, "chains", cfg.Chains[0].Name, "direct-verifier-profile.json"))
			if profile.MeasuredDirectCostGas == nil || *profile.MeasuredDirectCostGas != 3000096 {
				t.Fatalf("example measured direct cost = %v", profile.MeasuredDirectCostGas)
			}
			var compose composeDocument
			if err := yaml.Unmarshal(readFile(t, filepath.Join(output, "compose.yaml")), &compose); err != nil {
				t.Fatalf("decode compose: %v", err)
			}
			if len(compose.Services) != tc.chainCount*3 || len(compose.Volumes) != tc.chainCount*2 {
				t.Fatalf("compose cardinality services=%d volumes=%d", len(compose.Services), len(compose.Volumes))
			}
			counts := map[string]int{"geth": 0, "deploy": 0, "mapnode": 0}
			for _, chain := range cfg.Chains {
				for kind, name := range map[string]string{"geth": "geth-" + chain.Name, "deploy": "deploy-" + chain.Name, "mapnode": "mapnode-" + chain.Name} {
					if _, ok := compose.Services[name]; !ok {
						t.Fatalf("missing service %s", name)
					}
					counts[kind]++
				}
				if compose.Services["geth-"+chain.Name].Environment["BLOCK_PERIOD"] != fmt.Sprint(chain.BlockPeriodSeconds) {
					t.Fatalf("period not propagated for %s", chain.Name)
				}
			}
			for kind, count := range counts {
				if count != tc.chainCount {
					t.Fatalf("%s count = %d, want %d", kind, count, tc.chainCount)
				}
			}
			bootstrap := readJSON[struct {
				Nodes []struct {
					Name      string `json:"name"`
					PeerID    string `json:"peer_id"`
					Multiaddr string `json:"multiaddr"`
				} `json:"nodes"`
			}](t, filepath.Join(output, "p2p", "bootstrap.json"))
			if len(bootstrap.Nodes) != tc.chainCount {
				t.Fatalf("bootstrap count = %d, want %d", len(bootstrap.Nodes), tc.chainCount)
			}
			for index, node := range bootstrap.Nodes {
				wantName := cfg.Chains[index].Name
				wantAddress := fmt.Sprintf("/dns4/mapnode-%s/tcp/9000/p2p/%s", wantName, node.PeerID)
				if node.Name != wantName || node.PeerID == "" || node.Multiaddr != wantAddress {
					t.Fatalf("invalid bootstrap node %d: %+v", index, node)
				}
			}
		})
	}
}
