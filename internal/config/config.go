package config

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	DefaultGethImage          = "ethereum/client-go:v1.17.3"
	DefaultFoundryImage       = "ghcr.io/foundry-rs/foundry:v1.4.4"
	DefaultBlockPeriodSeconds = 3
	DefaultConfirmations      = 2
	DefaultMerkleDepth        = 8
	DefaultDirectProfileID    = "committee-3"
	DefaultAuthorizedSigners  = 3
	DefaultSignatureChecks    = 3
	DefaultHashRounds         = 4
	DefaultHostPortBase       = 18545
	DefaultHostPortStride     = 100
)

var dnsSafeName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

type Topology struct {
	Version     int
	NetworkName string
	Defaults    Defaults
	Chains      []Chain
	MapNodes    MapNodes
}

type Defaults struct {
	GethImage          string
	FoundryImage       string
	BlockPeriodSeconds int
	Confirmations      int
	MerkleDepth        int
	DirectVerifier     DirectVerifier
	HostPorts          HostPortDefaults
}

type HostPortDefaults struct {
	Base   int
	Stride int
}

type Chain struct {
	Name               string
	ChainID            uint64
	BlockPeriodSeconds int
	Confirmations      int
	MerkleDepth        int
	DirectVerifier     DirectVerifier
	HostPorts          HostPorts
}

type DirectVerifier struct {
	ProfileID             string
	AuthorizedSignerCount int
	SignatureChecks       int
	HashRounds            int
}

type HostPorts struct {
	HTTP       int
	WS         int
	MapNodeAPI int
	MapNodeP2P int
}

type MapNodes struct {
	P2PEnabled bool
	Database   string
}

type document struct {
	Version     int         `yaml:"version"`
	NetworkName string      `yaml:"network_name"`
	Defaults    rawDefaults `yaml:"defaults"`
	Chains      []rawChain  `yaml:"chains"`
	MapNodes    rawMapNodes `yaml:"mapnodes"`
}

type rawDefaults struct {
	GethImage          string              `yaml:"geth_image"`
	FoundryImage       string              `yaml:"foundry_image"`
	BlockPeriodSeconds *int                `yaml:"block_period_seconds"`
	Confirmations      *int                `yaml:"confirmations"`
	MerkleDepth        *int                `yaml:"merkle_depth"`
	DirectVerifier     rawDirectVerifier   `yaml:"direct_verifier"`
	HostPorts          rawHostPortDefaults `yaml:"host_ports"`
}

type rawDirectVerifier struct {
	ProfileID             string `yaml:"profile_id"`
	AuthorizedSignerCount *int   `yaml:"authorized_signer_count"`
	SignatureChecks       *int   `yaml:"signature_checks"`
	HashRounds            *int   `yaml:"hash_rounds"`
}

type rawHostPortDefaults struct {
	Base   *int `yaml:"base"`
	Stride *int `yaml:"stride"`
}

type rawChain struct {
	Name               string            `yaml:"name"`
	ChainID            uint64            `yaml:"chain_id"`
	BlockPeriodSeconds *int              `yaml:"block_period_seconds"`
	Confirmations      *int              `yaml:"confirmations"`
	MerkleDepth        *int              `yaml:"merkle_depth"`
	DirectVerifier     rawDirectVerifier `yaml:"direct_verifier"`
	HostPorts          rawHostPorts      `yaml:"host_ports"`
}

type rawHostPorts struct {
	HTTP       *int `yaml:"http"`
	WS         *int `yaml:"ws"`
	MapNodeAPI *int `yaml:"mapnode_api"`
	MapNodeP2P *int `yaml:"mapnode_p2p"`
}

type rawMapNodes struct {
	P2PEnabled bool   `yaml:"p2p_enabled"`
	Database   string `yaml:"database"`
}

func Decode(reader io.Reader) (*Topology, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var raw document
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode topology YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode topology YAML: multiple documents are not allowed")
		}
		return nil, fmt.Errorf("decode topology YAML: %w", err)
	}

	topology := resolve(raw)
	if err := topology.Validate(); err != nil {
		return nil, err
	}
	return topology, nil
}

func LoadFile(path string) (*Topology, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open topology file %q: %w", path, err)
	}
	defer file.Close()
	topology, err := Decode(file)
	if err != nil {
		return nil, fmt.Errorf("topology file %q: %w", path, err)
	}
	return topology, nil
}

func resolve(raw document) *Topology {
	defaults := Defaults{
		GethImage:          valueOrString(raw.Defaults.GethImage, DefaultGethImage),
		FoundryImage:       valueOrString(raw.Defaults.FoundryImage, DefaultFoundryImage),
		BlockPeriodSeconds: valueOr(raw.Defaults.BlockPeriodSeconds, DefaultBlockPeriodSeconds),
		Confirmations:      valueOr(raw.Defaults.Confirmations, DefaultConfirmations),
		MerkleDepth:        valueOr(raw.Defaults.MerkleDepth, DefaultMerkleDepth),
		DirectVerifier: DirectVerifier{
			ProfileID:             valueOrString(raw.Defaults.DirectVerifier.ProfileID, DefaultDirectProfileID),
			AuthorizedSignerCount: valueOr(raw.Defaults.DirectVerifier.AuthorizedSignerCount, DefaultAuthorizedSigners),
			SignatureChecks:       valueOr(raw.Defaults.DirectVerifier.SignatureChecks, DefaultSignatureChecks),
			HashRounds:            valueOr(raw.Defaults.DirectVerifier.HashRounds, DefaultHashRounds),
		},
		HostPorts: HostPortDefaults{
			Base:   valueOr(raw.Defaults.HostPorts.Base, DefaultHostPortBase),
			Stride: valueOr(raw.Defaults.HostPorts.Stride, DefaultHostPortStride),
		},
	}
	mapNodes := MapNodes{P2PEnabled: raw.MapNodes.P2PEnabled, Database: valueOrString(raw.MapNodes.Database, "sqlite")}
	topology := &Topology{Version: raw.Version, NetworkName: raw.NetworkName, Defaults: defaults, MapNodes: mapNodes}
	for index, rawChain := range raw.Chains {
		base := defaults.HostPorts.Base + index*defaults.HostPorts.Stride
		topology.Chains = append(topology.Chains, Chain{
			Name:               rawChain.Name,
			ChainID:            rawChain.ChainID,
			BlockPeriodSeconds: valueOr(rawChain.BlockPeriodSeconds, defaults.BlockPeriodSeconds),
			Confirmations:      valueOr(rawChain.Confirmations, defaults.Confirmations),
			MerkleDepth:        valueOr(rawChain.MerkleDepth, defaults.MerkleDepth),
			DirectVerifier: DirectVerifier{
				ProfileID:             valueOrString(rawChain.DirectVerifier.ProfileID, defaults.DirectVerifier.ProfileID),
				AuthorizedSignerCount: valueOr(rawChain.DirectVerifier.AuthorizedSignerCount, defaults.DirectVerifier.AuthorizedSignerCount),
				SignatureChecks:       valueOr(rawChain.DirectVerifier.SignatureChecks, defaults.DirectVerifier.SignatureChecks),
				HashRounds:            valueOr(rawChain.DirectVerifier.HashRounds, defaults.DirectVerifier.HashRounds),
			},
			HostPorts: HostPorts{
				HTTP:       valueOr(rawChain.HostPorts.HTTP, base),
				WS:         valueOr(rawChain.HostPorts.WS, base+1),
				MapNodeAPI: valueOr(rawChain.HostPorts.MapNodeAPI, base+2),
				MapNodeP2P: valueOr(rawChain.HostPorts.MapNodeP2P, base+3),
			},
		})
	}
	return topology
}

func (topology *Topology) Validate() error {
	if topology.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", topology.Version)
	}
	if !validDNSName(topology.NetworkName) {
		return fmt.Errorf("network_name %q must be DNS-safe", topology.NetworkName)
	}
	if len(topology.Chains) < 2 || len(topology.Chains) > 21 {
		return fmt.Errorf("chains must contain between 2 and 21 entries, got %d", len(topology.Chains))
	}
	if err := validateImage("defaults.geth_image", topology.Defaults.GethImage); err != nil {
		return err
	}
	if topology.Defaults.GethImage != DefaultGethImage {
		return fmt.Errorf("defaults.geth_image must use the supported image %q, got %q", DefaultGethImage, topology.Defaults.GethImage)
	}
	if err := validateImage("defaults.foundry_image", topology.Defaults.FoundryImage); err != nil {
		return err
	}
	if topology.Defaults.FoundryImage != DefaultFoundryImage {
		return fmt.Errorf("defaults.foundry_image must use the supported image %q, got %q", DefaultFoundryImage, topology.Defaults.FoundryImage)
	}
	if topology.Defaults.HostPorts.Stride <= 0 {
		return fmt.Errorf("defaults.host_ports.stride must be positive")
	}
	if topology.MapNodes.Database != "sqlite" {
		return fmt.Errorf("mapnodes.database must be sqlite, got %q", topology.MapNodes.Database)
	}

	names := make(map[string]struct{}, len(topology.Chains))
	chainIDs := make(map[uint64]struct{}, len(topology.Chains))
	ports := make(map[int]string, len(topology.Chains)*4)
	for index, chain := range topology.Chains {
		prefix := fmt.Sprintf("chains[%d]", index)
		if !validDNSName(chain.Name) {
			return fmt.Errorf("%s.name %q must be DNS-safe", prefix, chain.Name)
		}
		if _, exists := names[chain.Name]; exists {
			return fmt.Errorf("duplicate chain name %q", chain.Name)
		}
		names[chain.Name] = struct{}{}
		if chain.ChainID == 0 {
			return fmt.Errorf("%s.chain_id must be non-zero", prefix)
		}
		if _, exists := chainIDs[chain.ChainID]; exists {
			return fmt.Errorf("duplicate chain_id %d", chain.ChainID)
		}
		chainIDs[chain.ChainID] = struct{}{}
		if chain.BlockPeriodSeconds <= 0 {
			return fmt.Errorf("%s.block_period_seconds must be positive", prefix)
		}
		if chain.Confirmations <= 0 {
			return fmt.Errorf("%s.confirmations must be positive", prefix)
		}
		if chain.MerkleDepth < 1 || chain.MerkleDepth > 32 {
			return fmt.Errorf("%s.merkle_depth must be between 1 and 32", prefix)
		}
		if !validDNSName(chain.DirectVerifier.ProfileID) {
			return fmt.Errorf("%s.direct_verifier.profile_id %q must be DNS-safe", prefix, chain.DirectVerifier.ProfileID)
		}
		if chain.DirectVerifier.AuthorizedSignerCount < 1 || chain.DirectVerifier.AuthorizedSignerCount > 4096 {
			return fmt.Errorf("%s.direct_verifier.authorized_signer_count must be between 1 and 4096", prefix)
		}
		if chain.DirectVerifier.SignatureChecks < 1 || chain.DirectVerifier.SignatureChecks > chain.DirectVerifier.AuthorizedSignerCount {
			return fmt.Errorf("%s.direct_verifier.signature_checks must be between 1 and authorized_signer_count (%d)", prefix, chain.DirectVerifier.AuthorizedSignerCount)
		}
		if chain.DirectVerifier.HashRounds < 0 || chain.DirectVerifier.HashRounds > 16384 {
			return fmt.Errorf("%s.direct_verifier.hash_rounds must be between 0 and 16384", prefix)
		}
		for service, port := range map[string]int{
			"http": chain.HostPorts.HTTP, "ws": chain.HostPorts.WS,
			"mapnode_api": chain.HostPorts.MapNodeAPI, "mapnode_p2p": chain.HostPorts.MapNodeP2P,
		} {
			label := prefix + ".host_ports." + service
			if port < 1 || port > 65535 {
				return fmt.Errorf("%s port must be between 1 and 65535, got %d", label, port)
			}
			if previous, exists := ports[port]; exists {
				return fmt.Errorf("duplicate host port %d used by %s and %s", port, previous, label)
			}
			ports[port] = label
		}
	}
	return nil
}

func validDNSName(value string) bool {
	return len(value) <= 63 && dnsSafeName.MatchString(value)
}

func validateImage(field, image string) error {
	if strings.Contains(image, "@") {
		return fmt.Errorf("%s must use a specific tag, got %q", field, image)
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon <= lastSlash || lastColon == len(image)-1 {
		return fmt.Errorf("%s must use a specific tag, got %q", field, image)
	}
	tag := strings.ToLower(image[lastColon+1:])
	if tag == "latest" || tag == "stable" {
		return fmt.Errorf("%s must use a specific tag, got %q", field, image)
	}
	return nil
}

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func valueOrString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
