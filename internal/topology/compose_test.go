package topology_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinzjj/TrustMap_prototype/internal/topology"
	"go.yaml.in/yaml/v3"
)

type composeDocument struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
	Networks map[string]struct {
		Driver   string `yaml:"driver"`
		External bool   `yaml:"external"`
	} `yaml:"networks"`
}

type composeService struct {
	Build struct {
		Context    string            `yaml:"context"`
		Dockerfile string            `yaml:"dockerfile"`
		Args       map[string]string `yaml:"args"`
	} `yaml:"build"`
	Command     []string                     `yaml:"command"`
	Environment map[string]string            `yaml:"environment"`
	DependsOn   map[string]map[string]string `yaml:"depends_on"`
	Ports       []string                     `yaml:"ports"`
	Volumes     []string                     `yaml:"volumes"`
	Networks    []string                     `yaml:"networks"`
	GroupAdd    []string                     `yaml:"group_add"`
	Healthcheck *struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

func TestComposeHasExactlyThreeServicesPerChainAndRequiredWiring(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	var compose composeDocument
	if err := yaml.Unmarshal(readFile(t, filepath.Join(output, "compose.yaml")), &compose); err != nil {
		t.Fatalf("decode compose: %v", err)
	}
	if compose.Name != cfg.NetworkName || len(compose.Services) != len(cfg.Chains)*3 || len(compose.Volumes) != len(cfg.Chains)*2 || len(compose.Networks) != 1 {
		t.Fatalf("unexpected compose cardinality: services=%d volumes=%d networks=%d", len(compose.Services), len(compose.Volumes), len(compose.Networks))
	}
	network := compose.Networks[cfg.NetworkName]
	if network.Driver != "bridge" || network.External {
		t.Fatalf("network = %+v", network)
	}
	for _, chain := range cfg.Chains {
		gethName, deployName, mapNodeName := "geth-"+chain.Name, "deploy-"+chain.Name, "mapnode-"+chain.Name
		geth, ok := compose.Services[gethName]
		if !ok {
			t.Fatalf("missing %s", gethName)
		}
		assertBuild(t, geth, "docker/geth.Dockerfile")
		if geth.Build.Args["GETH_IMAGE"] != cfg.Defaults.GethImage || geth.Environment["CHAIN_ID"] != fmt.Sprint(chain.ChainID) || geth.Environment["BLOCK_PERIOD"] != fmt.Sprint(chain.BlockPeriodSeconds) || geth.Environment["DEPLOYER_ADDRESS"] == "" {
			t.Fatalf("invalid geth environment/build args: %+v %+v", geth.Build.Args, geth.Environment)
		}
		if len(geth.Command) != 0 {
			t.Fatalf("geth command must be owned by entrypoint, got duplicate arguments %v", geth.Command)
		}
		if geth.Environment["HTTP_VHOSTS"] != gethName+",localhost" {
			t.Fatalf("HTTP_VHOSTS = %q", geth.Environment["HTTP_VHOSTS"])
		}
		if geth.Healthcheck == nil || len(geth.Healthcheck.Test) != 2 || geth.Healthcheck.Test[1] != "/usr/local/bin/trustmap-geth-healthcheck" {
			t.Fatalf("invalid geth healthcheck: %+v", geth.Healthcheck)
		}
		assertContains(t, geth.Ports, fmt.Sprintf("127.0.0.1:%d:8545", chain.HostPorts.HTTP))
		assertContains(t, geth.Ports, fmt.Sprintf("127.0.0.1:%d:8546", chain.HostPorts.WS))
		assertContains(t, geth.Volumes, fmt.Sprintf("geth-%s-data:/data", chain.Name))

		deploy, ok := compose.Services[deployName]
		if !ok {
			t.Fatalf("missing %s", deployName)
		}
		assertBuild(t, deploy, "docker/deployer.Dockerfile")
		if deploy.Build.Args["FOUNDRY_IMAGE"] != cfg.Defaults.FoundryImage || deploy.DependsOn[gethName]["condition"] != "service_healthy" {
			t.Fatalf("invalid deploy wiring: %+v", deploy)
		}
		if deploy.Environment["RPC_URL"] != fmt.Sprintf("http://%s:8545", gethName) || deploy.Environment["DEPLOYER_PASSWORD_FILE"] != "/run/secrets/deployer-password" || deploy.Environment["MANIFEST_PATH"] != "/output/gateway-manifest.json" || deploy.Environment["AUTHORIZED_SIGNERS_FILE"] != "/runtime/direct-verifier-profile.json" {
			t.Fatalf("invalid deploy environment: %+v", deploy.Environment)
		}
		if deploy.Environment["MERKLE_DEPTH"] != fmt.Sprint(chain.MerkleDepth) {
			t.Fatalf("MERKLE_DEPTH = %q, want %d", deploy.Environment["MERKLE_DEPTH"], chain.MerkleDepth)
		}
		for _, redundant := range []string{"PROFILE_ID", "DIRECT_SIGNATURE_CHECKS", "DIRECT_HASH_ROUNDS"} {
			if _, exists := deploy.Environment[redundant]; exists {
				t.Fatalf("deploy environment duplicates profile field %s", redundant)
			}
		}
		assertContains(t, deploy.Volumes, fmt.Sprintf("./chains/%s/deployment:/output:rw", chain.Name))

		mapNode, ok := compose.Services[mapNodeName]
		if !ok {
			t.Fatalf("missing %s", mapNodeName)
		}
		assertBuild(t, mapNode, "docker/mapnode.Dockerfile")
		if mapNode.Environment["MAPNODE_CONFIG"] != "/runtime/mapnode.json" {
			t.Fatalf("invalid mapnode wiring: %+v", mapNode)
		}
		for _, dependency := range cfg.Chains {
			if mapNode.DependsOn["deploy-"+dependency.Name]["condition"] != "service_completed_successfully" {
				t.Fatalf("mapnode %s does not wait for deploy-%s: %+v", chain.Name, dependency.Name, mapNode.DependsOn)
			}
			assertContains(t, mapNode.Volumes, fmt.Sprintf("./chains/%s/deployment:/runtime/chains/%s/deployment:ro", dependency.Name, dependency.Name))
		}
		assertContains(t, mapNode.Ports, fmt.Sprintf("127.0.0.1:%d:8080", chain.HostPorts.MapNodeAPI))
		assertContains(t, mapNode.Ports, fmt.Sprintf("127.0.0.1:%d:9000", chain.HostPorts.MapNodeP2P))
		assertContains(t, mapNode.Volumes, fmt.Sprintf("mapnode-%s-data:/runtime/data", chain.Name))
		if len(mapNode.GroupAdd) != 1 || mapNode.GroupAdd[0] != fmt.Sprint(os.Getegid()) {
			t.Fatalf("mapnode group_add = %v, want host effective GID %d", mapNode.GroupAdd, os.Getegid())
		}
		for _, service := range []composeService{geth, deploy, mapNode} {
			assertContains(t, service.Networks, cfg.NetworkName)
		}

		mapNodeJSON := string(readFile(t, filepath.Join(output, "chains", chain.Name, "mapnode.json")))
		for _, other := range cfg.Chains {
			if !strings.Contains(mapNodeJSON, "geth-"+other.Name) {
				t.Fatalf("mapnode %s lacks read-only chain catalog entry %s", chain.Name, other.Name)
			}
		}
	}
}

func TestComposeMountsOnlyRoleSpecificRuntimeFilesAndSecrets(t *testing.T) {
	cfg := decodeTopology(t, renderYAML)
	output := t.TempDir()
	if err := topology.Render(cfg, output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	var compose composeDocument
	if err := yaml.Unmarshal(readFile(t, filepath.Join(output, "compose.yaml")), &compose); err != nil {
		t.Fatalf("decode compose: %v", err)
	}

	for _, chain := range cfg.Chains {
		chainRoot := fmt.Sprintf("./chains/%s", chain.Name)
		secretRoot := chainRoot + "/secrets"
		deployerSecrets := []string{
			secretRoot + "/deployer-keystore.json:/run/secrets/deployer-keystore.json:ro",
			secretRoot + "/deployer-password:/run/secrets/deployer-password:ro",
		}
		directSignerSecrets := make([]string, 0, chain.DirectVerifier.AuthorizedSignerCount*2)
		for index := 0; index < chain.DirectVerifier.AuthorizedSignerCount; index++ {
			directSignerSecrets = append(directSignerSecrets,
				fmt.Sprintf("%s/direct-signer-%d-keystore.json:/run/secrets/direct-signer-%d-keystore.json:ro", secretRoot, index, index),
				fmt.Sprintf("%s/direct-signer-%d-password:/run/secrets/direct-signer-%d-password:ro", secretRoot, index, index),
			)
		}

		gethWant := append([]string{
			chainRoot + "/genesis.json:/runtime/genesis.json:ro",
			fmt.Sprintf("geth-%s-data:/data", chain.Name),
		}, deployerSecrets...)
		gethVolumes := compose.Services["geth-"+chain.Name].Volumes
		assertExactVolumes(t, gethVolumes, gethWant)
		for _, forbidden := range append([]string{
			secretRoot + "/mapnode-keystore.json:/run/secrets/mapnode-keystore.json:ro",
			secretRoot + "/mapnode-password:/run/secrets/mapnode-password:ro",
			secretRoot + "/p2p-private-key:/run/secrets/p2p-private-key:ro",
		}, directSignerSecrets...) {
			assertNotContains(t, gethVolumes, forbidden)
		}

		deployWant := append([]string{
			chainRoot + "/direct-verifier-profile.json:/runtime/direct-verifier-profile.json:ro",
			chainRoot + "/deployment:/output:rw",
		}, deployerSecrets...)
		deployWant = append(deployWant, directSignerSecrets...)
		deployVolumes := compose.Services["deploy-"+chain.Name].Volumes
		assertExactVolumes(t, deployVolumes, deployWant)
		for _, forbidden := range []string{
			secretRoot + "/mapnode-keystore.json:/run/secrets/mapnode-keystore.json:ro",
			secretRoot + "/mapnode-password:/run/secrets/mapnode-password:ro",
			secretRoot + "/p2p-private-key:/run/secrets/p2p-private-key:ro",
		} {
			assertNotContains(t, deployVolumes, forbidden)
		}

		mapNodeWant := []string{
			chainRoot + "/mapnode.json:/runtime/mapnode.json:ro",
			chainRoot + "/direct-verifier-profile.json:/runtime/direct-verifier-profile.json:ro",
			chainRoot + "/deployment:/runtime/deployment:ro",
			"./p2p/bootstrap.json:/runtime/p2p/bootstrap.json:ro",
			secretRoot + "/mapnode-keystore.json:/run/secrets/mapnode-keystore.json:ro",
			secretRoot + "/mapnode-password:/run/secrets/mapnode-password:ro",
			secretRoot + "/p2p-private-key:/run/secrets/p2p-private-key:ro",
			fmt.Sprintf("mapnode-%s-data:/runtime/data", chain.Name),
		}
		for _, catalogChain := range cfg.Chains {
			mapNodeWant = append(mapNodeWant, fmt.Sprintf("./chains/%s/deployment:/runtime/chains/%s/deployment:ro", catalogChain.Name, catalogChain.Name))
		}
		mapNodeWant = append(mapNodeWant, directSignerSecrets...)
		mapNodeVolumes := compose.Services["mapnode-"+chain.Name].Volumes
		assertExactVolumes(t, mapNodeVolumes, mapNodeWant)
		for _, forbidden := range deployerSecrets {
			assertNotContains(t, mapNodeVolumes, forbidden)
		}

		for serviceName, service := range map[string]composeService{
			"geth": compose.Services["geth-"+chain.Name], "deploy": compose.Services["deploy-"+chain.Name], "mapnode": compose.Services["mapnode-"+chain.Name],
		} {
			for _, forbidden := range []string{chainRoot + ":/runtime:ro", secretRoot + ":/run/secrets:ro"} {
				if contains(service.Volumes, forbidden) {
					t.Fatalf("%s %s has forbidden parent-directory mount %q", chain.Name, serviceName, forbidden)
				}
			}
		}
	}
}

func assertBuild(t *testing.T, service composeService, dockerfile string) {
	t.Helper()
	if service.Build.Context != "../.." || service.Build.Dockerfile != dockerfile {
		t.Fatalf("build = %+v, want context ../.. dockerfile %s", service.Build, dockerfile)
	}
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q not found in %v", want, values)
}

func assertExactVolumes(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("volume count = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for _, volume := range want {
		if !contains(got, volume) {
			t.Fatalf("required volume %q not found in %v", volume, got)
		}
	}
}

func assertNotContains(t *testing.T, values []string, forbidden string) {
	t.Helper()
	if contains(values, forbidden) {
		t.Fatalf("forbidden volume %q found in %v", forbidden, values)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
