package chain_test

import (
	"testing"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestRegistryRequiresUniqueChainsNamesAndExactlyOneHome(t *testing.T) {
	a := catalogChain(t, "alpha", 10001, true)
	b := catalogChain(t, "beta", 10002, false)
	registry, err := chain.NewRegistry([]chain.Chain{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if home, err := registry.HomeChain(); err != nil || home != a {
		t.Fatalf("home = %+v, %v", home, err)
	}
	if all := registry.All(); len(all) != 2 || all[0].Name != "alpha" || all[1].Name != "beta" {
		t.Fatalf("All() = %+v", all)
	}

	sameIDDifferentName := b
	sameIDDifferentName.Name = "gamma"
	sameNameDifferentID := b
	sameNameDifferentID.Name = a.Name
	bad := [][]chain.Chain{{a, sameIDDifferentName, b}, {a, sameNameDifferentID}, {a, a}, {{Name: "beta", ChainID: b.ChainID, HTTPRPC: b.HTTPRPC, Confirmations: 2, DeploymentManifest: b.DeploymentManifest}}}
	for index, entries := range bad {
		if _, err := chain.NewRegistry(entries); err == nil {
			t.Fatalf("invalid registry %d accepted", index)
		}
	}
}

func catalogChain(t *testing.T, name string, id uint64, home bool) chain.Chain {
	t.Helper()
	chainID, err := domain.NewChainID(id)
	if err != nil {
		t.Fatal(err)
	}
	return chain.Chain{Name: name, ChainID: chainID, HTTPRPC: "http://geth-" + name + ":8545", Confirmations: 2, DeploymentManifest: "/runtime/chains/" + name + "/deployment/gateway-manifest.json", Home: home}
}
