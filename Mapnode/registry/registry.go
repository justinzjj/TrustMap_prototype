// Package registry defines the one-chain/one-MapNode runtime registry and the
// home-chain signing authority boundary.
package registry

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/internal/directprofile"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrHomeChainNotFound      = errors.New("home chain not found")
	ErrChainNotFound          = errors.New("chain not found")
	ErrNoSignerAuthority      = errors.New("MapNode has no signer authority for chain")
	ErrNoTransactionAuthority = errors.New("MapNode has no transaction authority for chain")
)

// DirectVerifierParameters are the security/cost parameters fixed by
// deployment. They are never populated from a verification request.
type DirectVerifierParameters struct {
	ID                    string
	AuthorizedSigners     []common.Address
	SignatureChecks       uint32
	HashRounds            uint32
	MeasuredDirectCostGas uint64
}

// DeploymentProfile binds the locally configured profile to what the
// deployment manifest says is on chain.
type DeploymentProfile struct {
	Configured  DirectVerifierParameters
	Deployed    DirectVerifierParameters
	Fingerprint common.Hash
}

type Chain struct {
	Name                 string
	ChainID              domain.ChainID
	MapNodeID            string
	Home                 bool
	SignerAuthority      bool
	TransactionAuthority bool
	Profile              DeploymentProfile
}

type ValidatedDirectProfile struct {
	ID          string
	Fingerprint common.Hash
	DirectCost  uint64
	Calibrated  bool
}

type Registry struct {
	chains map[domain.ChainID]Chain
	home   domain.ChainID
}

func New(chains []Chain) (*Registry, error) {
	if len(chains) == 0 {
		return nil, errors.New("registry must contain at least one chain")
	}
	result := &Registry{chains: make(map[domain.ChainID]Chain, len(chains))}
	mapNodes := make(map[string]struct{}, len(chains))
	homeCount := 0
	for _, chain := range chains {
		if err := chain.ChainID.Validate(); err != nil {
			return nil, fmt.Errorf("chain %q: %w", chain.Name, err)
		}
		if strings.TrimSpace(chain.Name) == "" || strings.TrimSpace(chain.MapNodeID) == "" {
			return nil, errors.New("every chain requires a name and MapNode ID")
		}
		if strings.TrimSpace(chain.Profile.Configured.ID) == "" || strings.TrimSpace(chain.Profile.Deployed.ID) == "" {
			return nil, fmt.Errorf("chain %q requires configured and deployed direct verifier profile IDs", chain.Name)
		}
		if _, exists := result.chains[chain.ChainID]; exists {
			return nil, fmt.Errorf("duplicate chain ID %s", chain.ChainID.BigInt())
		}
		if _, exists := mapNodes[chain.MapNodeID]; exists {
			return nil, fmt.Errorf("MapNode %q is assigned to more than one chain", chain.MapNodeID)
		}
		if chain.Home != chain.SignerAuthority || chain.Home != chain.TransactionAuthority {
			return nil, fmt.Errorf("chain %q signer and transaction authority must be held exactly by the home MapNode", chain.Name)
		}
		if chain.Home {
			homeCount++
			result.home = chain.ChainID
		}
		chain.Profile.Configured.AuthorizedSigners = cloneAddresses(chain.Profile.Configured.AuthorizedSigners)
		chain.Profile.Deployed.AuthorizedSigners = cloneAddresses(chain.Profile.Deployed.AuthorizedSigners)
		result.chains[chain.ChainID] = chain
		mapNodes[chain.MapNodeID] = struct{}{}
	}
	if homeCount != 1 {
		return nil, fmt.Errorf("registry requires exactly one home chain, got %d", homeCount)
	}
	return result, nil
}

func (registry *Registry) Chain(chainID domain.ChainID) (Chain, bool) {
	if registry == nil {
		return Chain{}, false
	}
	chain, ok := registry.chains[chainID]
	if !ok {
		return Chain{}, false
	}
	chain.Profile.Configured.AuthorizedSigners = cloneAddresses(chain.Profile.Configured.AuthorizedSigners)
	chain.Profile.Deployed.AuthorizedSigners = cloneAddresses(chain.Profile.Deployed.AuthorizedSigners)
	return chain, true
}

func (registry *Registry) HomeChain() (Chain, error) {
	if registry == nil {
		return Chain{}, ErrHomeChainNotFound
	}
	chain, ok := registry.Chain(registry.home)
	if !ok {
		return Chain{}, ErrHomeChainNotFound
	}
	return chain, nil
}

func (registry *Registry) DirectProfile(chainID domain.ChainID) (ValidatedDirectProfile, bool) {
	chain, ok := registry.Chain(chainID)
	if !ok || !chain.Home || !chain.SignerAuthority || !chain.TransactionAuthority {
		return ValidatedDirectProfile{}, false
	}
	configured := chain.Profile.Configured
	status := ValidatedDirectProfile{ID: configured.ID, Fingerprint: chain.Profile.Fingerprint}
	computed := ComputeProfileFingerprint(configured)
	if computed != chain.Profile.Fingerprint || !sameParameters(configured, chain.Profile.Deployed) {
		return status, true
	}
	if configured.ID != directprofile.ID ||
		len(configured.AuthorizedSigners) != directprofile.AuthorizedSignerCount ||
		configured.SignatureChecks != directprofile.SignatureChecks ||
		configured.HashRounds != directprofile.HashRounds ||
		configured.MeasuredDirectCostGas != directprofile.MeasuredCostGas {
		return status, true
	}
	seen := make(map[common.Address]struct{}, len(configured.AuthorizedSigners))
	for _, signer := range configured.AuthorizedSigners {
		if signer == (common.Address{}) {
			return status, true
		}
		if _, exists := seen[signer]; exists {
			return status, true
		}
		seen[signer] = struct{}{}
	}
	status.Calibrated = true
	status.DirectCost = directprofile.MeasuredCostGas
	return status, true
}

func (registry *Registry) RequireTransactionAuthority(chainID domain.ChainID) error {
	chain, ok := registry.Chain(chainID)
	if !ok {
		return ErrChainNotFound
	}
	if !chain.Home || !chain.TransactionAuthority {
		return ErrNoTransactionAuthority
	}
	return nil
}

func (registry *Registry) RequireSignerAuthority(chainID domain.ChainID) error {
	chain, ok := registry.Chain(chainID)
	if !ok {
		return ErrChainNotFound
	}
	if !chain.Home || !chain.SignerAuthority {
		return ErrNoSignerAuthority
	}
	return nil
}

// ComputeProfileFingerprint commits to every deployment-fixed cost parameter,
// including the ordered authorized signer set.
func ComputeProfileFingerprint(parameters DirectVerifierParameters) common.Hash {
	var encoded bytes.Buffer
	_ = binary.Write(&encoded, binary.BigEndian, uint32(len(parameters.ID)))
	_, _ = encoded.WriteString(parameters.ID)
	_ = binary.Write(&encoded, binary.BigEndian, uint32(len(parameters.AuthorizedSigners)))
	for _, signer := range parameters.AuthorizedSigners {
		_, _ = encoded.Write(signer[:])
	}
	_ = binary.Write(&encoded, binary.BigEndian, parameters.SignatureChecks)
	_ = binary.Write(&encoded, binary.BigEndian, parameters.HashRounds)
	_ = binary.Write(&encoded, binary.BigEndian, parameters.MeasuredDirectCostGas)
	return crypto.Keccak256Hash(encoded.Bytes())
}

func sameParameters(left, right DirectVerifierParameters) bool {
	if left.ID != right.ID || left.SignatureChecks != right.SignatureChecks ||
		left.HashRounds != right.HashRounds || left.MeasuredDirectCostGas != right.MeasuredDirectCostGas ||
		len(left.AuthorizedSigners) != len(right.AuthorizedSigners) {
		return false
	}
	for index := range left.AuthorizedSigners {
		if left.AuthorizedSigners[index] != right.AuthorizedSigners[index] {
			return false
		}
	}
	return true
}

func cloneAddresses(addresses []common.Address) []common.Address {
	return append([]common.Address(nil), addresses...)
}
