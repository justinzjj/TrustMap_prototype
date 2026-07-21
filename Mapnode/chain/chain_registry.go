// Package chain owns the read-only catalog and verified Ethereum RPC adapters.
package chain

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrChainNotFound     = errors.New("live chain not found")
	ErrHomeChainNotFound = errors.New("live home chain not found")
)

type Chain struct {
	Name               string
	ChainID            domain.ChainID
	HTTPRPC            string
	Confirmations      uint64
	DeploymentManifest string
	Home               bool
}

type Registry struct {
	byID   map[domain.ChainID]Chain
	byName map[string]domain.ChainID
	home   domain.ChainID
}

func NewRegistry(chains []Chain) (*Registry, error) {
	if len(chains) == 0 {
		return nil, errors.New("live chain catalog must not be empty")
	}
	registry := &Registry{byID: make(map[domain.ChainID]Chain, len(chains)), byName: make(map[string]domain.ChainID, len(chains))}
	homeCount := 0
	for _, item := range chains {
		if err := validateChain(item); err != nil {
			return nil, err
		}
		if _, exists := registry.byID[item.ChainID]; exists {
			return nil, fmt.Errorf("duplicate live chain ID %s", item.ChainID.BigInt())
		}
		if _, exists := registry.byName[item.Name]; exists {
			return nil, fmt.Errorf("duplicate live chain name %q", item.Name)
		}
		registry.byID[item.ChainID] = item
		registry.byName[item.Name] = item.ChainID
		if item.Home {
			homeCount++
			registry.home = item.ChainID
		}
	}
	if homeCount != 1 {
		return nil, fmt.Errorf("live chain catalog requires exactly one home, got %d", homeCount)
	}
	return registry, nil
}

func validateChain(item Chain) error {
	if err := item.ChainID.Validate(); err != nil {
		return fmt.Errorf("live chain %q: %w", item.Name, err)
	}
	if item.Name == "" || strings.TrimSpace(item.Name) != item.Name {
		return errors.New("live chain name must be non-empty and canonical")
	}
	parsed, err := url.ParseRequestURI(item.HTTPRPC)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("live chain %q HTTP RPC must be an absolute HTTP URL", item.Name)
	}
	if item.Confirmations == 0 || strings.TrimSpace(item.DeploymentManifest) == "" {
		return fmt.Errorf("live chain %q requires confirmations and deployment manifest", item.Name)
	}
	return nil
}

func (registry *Registry) HomeChain() (Chain, error) {
	if registry == nil {
		return Chain{}, ErrHomeChainNotFound
	}
	item, ok := registry.byID[registry.home]
	if !ok {
		return Chain{}, ErrHomeChainNotFound
	}
	return item, nil
}

func (registry *Registry) Chain(chainID domain.ChainID) (Chain, bool) {
	if registry == nil {
		return Chain{}, false
	}
	item, ok := registry.byID[chainID]
	return item, ok
}

func (registry *Registry) Named(name string) (Chain, bool) {
	if registry == nil {
		return Chain{}, false
	}
	id, ok := registry.byName[name]
	if !ok {
		return Chain{}, false
	}
	return registry.Chain(id)
}

func (registry *Registry) All() []Chain {
	if registry == nil {
		return nil
	}
	all := make([]Chain, 0, len(registry.byID))
	for _, item := range registry.byID {
		all = append(all, item)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}
