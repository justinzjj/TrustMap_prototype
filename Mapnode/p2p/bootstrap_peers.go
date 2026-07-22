package p2p

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const maxBootstrapFileBytes = 1 << 20

var bootstrapNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func LoadPersistentEd25519Identity(path string) (libp2pcrypto.PrivKey, peer.ID, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read persistent p2p identity: %w", err)
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" || strings.ContainsAny(trimmed, " \t\r\n") {
		return nil, "", errors.New("persistent p2p identity must be one base64 value")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(trimmed)
	if err != nil {
		return nil, "", errors.New("decode persistent p2p identity")
	}
	key, err := libp2pcrypto.UnmarshalPrivateKey(raw)
	if err != nil || key.Type() != libp2pcrypto.Ed25519 {
		return nil, "", errors.New("persistent p2p identity must be a marshaled Ed25519 private key")
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("derive persistent p2p peer ID: %w", err)
	}
	return key, id, nil
}

type bootstrapDocument struct {
	Version int `json:"version"`
	Nodes   []struct {
		Name      string `json:"name"`
		PeerID    string `json:"peer_id"`
		Multiaddr string `json:"multiaddr"`
	} `json:"nodes"`
}

func LoadBootstrapPeers(path string, self peer.ID) ([]peer.AddrInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open bootstrap peers: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBootstrapFileBytes+1))
	if err != nil || len(content) > maxBootstrapFileBytes {
		return nil, errors.New("read bounded bootstrap peers")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document bootstrapDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode bootstrap peers: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("bootstrap peers contain trailing JSON")
	}
	if document.Version != 1 || len(document.Nodes) == 0 {
		return nil, errors.New("bootstrap peers require version 1 and nodes")
	}
	names := make(map[string]struct{}, len(document.Nodes))
	ids := make(map[peer.ID]struct{}, len(document.Nodes))
	addresses := make(map[string]struct{}, len(document.Nodes))
	result := make([]peer.AddrInfo, 0, len(document.Nodes))
	for index, node := range document.Nodes {
		if !bootstrapNamePattern.MatchString(node.Name) {
			return nil, fmt.Errorf("bootstrap node %d has invalid name", index)
		}
		if _, exists := names[node.Name]; exists {
			return nil, fmt.Errorf("duplicate bootstrap node name %q", node.Name)
		}
		names[node.Name] = struct{}{}
		id, err := peer.Decode(node.PeerID)
		if err != nil || id.String() != node.PeerID {
			return nil, fmt.Errorf("bootstrap node %d has invalid peer ID", index)
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("duplicate bootstrap peer ID %s", id)
		}
		ids[id] = struct{}{}
		address, err := ma.NewMultiaddr(node.Multiaddr)
		if err != nil || address.String() != node.Multiaddr {
			return nil, fmt.Errorf("bootstrap node %d has invalid canonical multiaddr", index)
		}
		if _, exists := addresses[node.Multiaddr]; exists {
			return nil, fmt.Errorf("duplicate bootstrap multiaddr %q", node.Multiaddr)
		}
		addresses[node.Multiaddr] = struct{}{}
		info, err := peer.AddrInfoFromP2pAddr(address)
		if err != nil || info.ID != id || len(info.Addrs) != 1 {
			return nil, fmt.Errorf("bootstrap node %d multiaddr does not bind declared peer", index)
		}
		if id != self {
			result = append(result, *info)
		}
	}
	return result, nil
}
