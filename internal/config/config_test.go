package config_test

import (
	"strings"
	"testing"

	"github.com/justinzjj/TrustMap_prototype/internal/config"
)

const validYAML = `version: 1
network_name: trustmap-dev
defaults:
  geth_image: ethereum/client-go:v1.17.3
  foundry_image: ghcr.io/foundry-rs/foundry:v1.4.4
  block_period_seconds: 2
  confirmations: 3
  merkle_depth: 8
  host_ports:
    base: 18545
    stride: 100
chains:
  - name: alpha
    chain_id: 31337
  - name: beta
    chain_id: 31338
    block_period_seconds: 5
    confirmations: 4
    merkle_depth: 12
    host_ports:
      http: 28545
      ws: 28546
      mapnode_api: 28080
      mapnode_p2p: 29000
mapnodes:
  p2p_enabled: true
  database: sqlite
`

func TestDecodeStrictResolvesDefaultsAndOverrides(t *testing.T) {
	topology, err := config.Decode(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if topology.Version != 1 || topology.NetworkName != "trustmap-dev" {
		t.Fatalf("unexpected topology header: %+v", topology)
	}
	if topology.Defaults.GethImage != "ethereum/client-go:v1.17.3" {
		t.Fatalf("geth image = %q", topology.Defaults.GethImage)
	}
	alpha := topology.Chains[0]
	if alpha.BlockPeriodSeconds != 2 || alpha.Confirmations != 3 || alpha.MerkleDepth != 8 {
		t.Fatalf("defaults not resolved: %+v", alpha)
	}
	if alpha.HostPorts.HTTP != 18545 || alpha.HostPorts.WS != 18546 || alpha.HostPorts.MapNodeAPI != 18547 || alpha.HostPorts.MapNodeP2P != 18548 {
		t.Fatalf("default ports not resolved: %+v", alpha.HostPorts)
	}
	beta := topology.Chains[1]
	if beta.BlockPeriodSeconds != 5 || beta.Confirmations != 4 || beta.MerkleDepth != 12 {
		t.Fatalf("overrides not resolved: %+v", beta)
	}
	if beta.HostPorts.HTTP != 28545 || beta.HostPorts.WS != 28546 || beta.HostPorts.MapNodeAPI != 28080 || beta.HostPorts.MapNodeP2P != 29000 {
		t.Fatalf("port overrides not resolved: %+v", beta.HostPorts)
	}
}

func TestDecodeStrictRejectsUnknownField(t *testing.T) {
	input := strings.Replace(validYAML, "network_name: trustmap-dev", "network_name: trustmap-dev\nunknown_field: true", 1)
	_, err := config.Decode(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestDecodeAppliesDocumentedDefaults(t *testing.T) {
	input := `version: 1
network_name: trustmap-dev
chains:
  - {name: alpha, chain_id: 1}
  - {name: beta, chain_id: 2}
mapnodes: {p2p_enabled: false, database: sqlite}
`
	topology, err := config.Decode(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if topology.Defaults.GethImage != config.DefaultGethImage {
		t.Fatalf("geth default = %q", topology.Defaults.GethImage)
	}
	if topology.Defaults.FoundryImage == "" || strings.HasSuffix(topology.Defaults.FoundryImage, ":latest") {
		t.Fatalf("foundry default must be pinned: %q", topology.Defaults.FoundryImage)
	}
}

func TestDecodeResolvesDirectVerifierDefaultsAndOverrides(t *testing.T) {
	input := strings.Replace(validYAML, "  host_ports:\n    base: 18545", "  direct_verifier:\n    profile_id: committee-64\n    authorized_signer_count: 64\n    signature_checks: 32\n    hash_rounds: 256\n  host_ports:\n    base: 18545", 1)
	input = strings.Replace(input, "    block_period_seconds: 5", "    block_period_seconds: 5\n    direct_verifier:\n      profile_id: committee-128\n      authorized_signer_count: 128\n      signature_checks: 96\n      hash_rounds: 512", 1)
	topology, err := config.Decode(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got := topology.Chains[0].DirectVerifier; got.ProfileID != "committee-64" || got.AuthorizedSignerCount != 64 || got.SignatureChecks != 32 || got.HashRounds != 256 {
		t.Fatalf("default direct verifier not resolved: %+v", got)
	}
	if got := topology.Chains[1].DirectVerifier; got.ProfileID != "committee-128" || got.AuthorizedSignerCount != 128 || got.SignatureChecks != 96 || got.HashRounds != 512 {
		t.Fatalf("direct verifier override not resolved: %+v", got)
	}
}

func TestDecodeRejectsInvalidDirectVerifierCosts(t *testing.T) {
	base := strings.Replace(validYAML, "  host_ports:\n    base: 18545", "  direct_verifier:\n    profile_id: committee-3\n    authorized_signer_count: 3\n    signature_checks: 3\n    hash_rounds: 4\n  host_ports:\n    base: 18545", 1)
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{"unsafe profile", "profile_id: committee-3", "profile_id: Bad_Profile", "profile_id"},
		{"zero signer count", "authorized_signer_count: 3", "authorized_signer_count: 0", "authorized_signer_count"},
		{"too many signers", "authorized_signer_count: 3", "authorized_signer_count: 4097", "authorized_signer_count"},
		{"zero signature checks", "signature_checks: 3", "signature_checks: 0", "signature_checks"},
		{"checks exceed signers", "signature_checks: 3", "signature_checks: 4", "authorized_signer_count"},
		{"too many hash rounds", "hash_rounds: 4", "hash_rounds: 16385", "hash_rounds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Decode(strings.NewReader(strings.Replace(base, tc.old, tc.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDecodeRejectsInvalidConfigurations(t *testing.T) {
	tests := map[string]struct {
		old  string
		new  string
		want string
	}{
		"version":             {"version: 1", "version: 2", "version"},
		"too few chains":      {"  - name: beta\n    chain_id: 31338", "", "between 2 and 21"},
		"unsafe network":      {"network_name: trustmap-dev", "network_name: Unsafe_Name", "network_name"},
		"unsafe name":         {"name: alpha", "name: Alpha_bad", "DNS-safe"},
		"duplicate name":      {"name: beta", "name: alpha", "duplicate chain name"},
		"zero ID":             {"chain_id: 31337", "chain_id: 0", "chain_id"},
		"duplicate ID":        {"chain_id: 31338", "chain_id: 31337", "duplicate chain_id"},
		"zero period":         {"block_period_seconds: 5", "block_period_seconds: 0", "block_period_seconds"},
		"zero confirms":       {"confirmations: 4", "confirmations: 0", "confirmations"},
		"depth low":           {"merkle_depth: 12", "merkle_depth: 0", "merkle_depth"},
		"depth high":          {"merkle_depth: 12", "merkle_depth: 33", "merkle_depth"},
		"bad database":        {"database: sqlite", "database: postgres", "sqlite"},
		"latest geth":         {"ethereum/client-go:v1.17.3", "ethereum/client-go:latest", "specific tag"},
		"unsupported geth":    {"ethereum/client-go:v1.17.3", "ethereum/client-go:v1.16.2", "supported image"},
		"unofficial geth":     {"ethereum/client-go:v1.17.3", "example/geth:v1.17.3", "supported image"},
		"stable foundry":      {"ghcr.io/foundry-rs/foundry:v1.4.4", "ghcr.io/foundry-rs/foundry:stable", "specific tag"},
		"unsupported foundry": {"ghcr.io/foundry-rs/foundry:v1.4.4", "ghcr.io/foundry-rs/foundry:v1.4.3", "supported image"},
		"untagged image":      {"ethereum/client-go:v1.17.3", "ethereum/client-go", "specific tag"},
		"digest image":        {"ethereum/client-go:v1.17.3", "ethereum/client-go@sha256:abcdef", "specific tag"},
		"port range":          {"http: 28545", "http: 70000", "port"},
		"port collision":      {"ws: 28546", "ws: 28545", "duplicate host port"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			input := strings.Replace(validYAML, tc.old, tc.new, 1)
			_, err := config.Decode(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateRejectsMoreThanTwentyOneChains(t *testing.T) {
	var input strings.Builder
	input.WriteString("version: 1\nnetwork_name: trustmap-dev\nchains:\n")
	for i := 1; i <= 22; i++ {
		input.WriteString("  - name: chain-")
		input.WriteString(string(rune('a' + i - 1)))
		input.WriteString("\n    chain_id: ")
		input.WriteString(strings.Repeat("1", i%3+1))
		input.WriteString("\n")
	}
	input.WriteString("mapnodes: {database: sqlite}\n")
	_, err := config.Decode(strings.NewReader(input.String()))
	if err == nil || !strings.Contains(err.Error(), "between 2 and 21") {
		t.Fatalf("expected chain-count error, got %v", err)
	}
}
