package registry

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestRegistryExposesOnlyDeploymentMatchedCalibratedCost(t *testing.T) {
	home := testChain(t, 101, true)
	remote := testChain(t, 102, false)
	registry, err := New([]Chain{home, remote})
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := registry.DirectProfile(home.ChainID)
	if !ok || !profile.Calibrated || profile.DirectCost != 3_000_096 {
		t.Fatalf("DirectProfile() = %+v, %t", profile, ok)
	}
	if _, ok := registry.DirectProfile(remote.ChainID); ok {
		t.Fatal("remote chain exposed Direct transaction authority")
	}
	if err := registry.RequireTransactionAuthority(home.ChainID); err != nil {
		t.Fatalf("home transaction authority rejected: %v", err)
	}
	if err := registry.RequireTransactionAuthority(remote.ChainID); !errors.Is(err, ErrNoTransactionAuthority) {
		t.Fatalf("remote authority error = %v", err)
	}
	if err := registry.RequireSignerAuthority(home.ChainID); err != nil {
		t.Fatalf("home signer authority rejected: %v", err)
	}
	if err := registry.RequireSignerAuthority(remote.ChainID); !errors.Is(err, ErrNoSignerAuthority) {
		t.Fatalf("remote signer authority error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Chain)
	}{
		{"custom profile", func(chain *Chain) { chain.Profile.Configured.ID = "custom" }},
		{"signer count", func(chain *Chain) {
			chain.Profile.Configured.AuthorizedSigners = chain.Profile.Configured.AuthorizedSigners[:2]
		}},
		{"signature checks", func(chain *Chain) { chain.Profile.Configured.SignatureChecks = 2 }},
		{"hash rounds", func(chain *Chain) { chain.Profile.Configured.HashRounds = 4496 }},
		{"measured cost", func(chain *Chain) { chain.Profile.Configured.MeasuredDirectCostGas = 3_000_095 }},
		{"deployment mismatch", func(chain *Chain) { chain.Profile.Deployed.HashRounds = 1 }},
		{"fingerprint mismatch", func(chain *Chain) { chain.Profile.Fingerprint[0] ^= 1 }},
		{"zero signer", func(chain *Chain) {
			chain.Profile.Configured.AuthorizedSigners[0] = common.Address{}
			chain.Profile.Deployed.AuthorizedSigners[0] = common.Address{}
			chain.Profile.Fingerprint = ComputeProfileFingerprint(chain.Profile.Configured)
		}},
		{"duplicate signer", func(chain *Chain) {
			chain.Profile.Configured.AuthorizedSigners[1] = chain.Profile.Configured.AuthorizedSigners[0]
			chain.Profile.Deployed.AuthorizedSigners[1] = chain.Profile.Deployed.AuthorizedSigners[0]
			chain.Profile.Fingerprint = ComputeProfileFingerprint(chain.Profile.Configured)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := home
			changed.Profile.Configured.AuthorizedSigners = append([]common.Address(nil), home.Profile.Configured.AuthorizedSigners...)
			changed.Profile.Deployed.AuthorizedSigners = append([]common.Address(nil), home.Profile.Deployed.AuthorizedSigners...)
			tt.mutate(&changed)
			got, err := New([]Chain{changed, remote})
			if err != nil {
				t.Fatal(err)
			}
			profile, ok := got.DirectProfile(changed.ChainID)
			if !ok || profile.Calibrated || profile.DirectCost != 0 {
				t.Fatalf("mismatch exposed calibrated cost: %+v, %t", profile, ok)
			}
		})
	}
}

func TestRegistryRequiresOneHomeAndOneAuthorityPerChain(t *testing.T) {
	home := testChain(t, 101, true)
	remote := testChain(t, 102, false)
	for _, test := range []struct {
		name   string
		chains []Chain
	}{
		{"no home", []Chain{remote}},
		{"two homes", []Chain{home, testChain(t, 102, true)}},
		{"remote authority", func() []Chain { remote.TransactionAuthority = true; return []Chain{home, remote} }()},
		{"remote signer", func() []Chain { remote.SignerAuthority = true; return []Chain{home, remote} }()},
		{"home without authority", func() []Chain { home.TransactionAuthority = false; return []Chain{home, remote} }()},
		{"duplicate chain", []Chain{home, home}},
		{"duplicate MapNode", func() []Chain { remote.MapNodeID = home.MapNodeID; return []Chain{home, remote} }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.chains); err == nil {
				t.Fatal("New() accepted invalid registry")
			}
		})
	}
	if _, err := New([]Chain{testChain(t, 101, true), testChain(t, 102, false)}); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	if _, err := (&Registry{}).HomeChain(); !errors.Is(err, ErrHomeChainNotFound) {
		t.Fatalf("HomeChain() error = %v", err)
	}
}

func testChain(t *testing.T, id uint64, home bool) Chain {
	t.Helper()
	chainID, _ := domain.NewChainID(id)
	signers := []common.Address{
		common.HexToAddress("0x1000000000000000000000000000000000000001"),
		common.HexToAddress("0x2000000000000000000000000000000000000002"),
		common.HexToAddress("0x3000000000000000000000000000000000000003"),
	}
	params := DirectVerifierParameters{ID: "pow-spv-3m", AuthorizedSigners: signers, SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: 3_000_096}
	return Chain{
		Name: "chain", ChainID: chainID, MapNodeID: chainID.BigInt().String(), Home: home,
		SignerAuthority: home, TransactionAuthority: home,
		Profile: DeploymentProfile{Configured: params, Deployed: params, Fingerprint: ComputeProfileFingerprint(params)},
	}
}
