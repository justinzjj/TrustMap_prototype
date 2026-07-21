package topology

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/justinzjj/TrustMap_prototype/internal/config"
	"go.yaml.in/yaml/v3"
)

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]map[string]any `yaml:"volumes"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeNetwork struct {
	Driver   string `yaml:"driver"`
	External bool   `yaml:"external"`
}

type composeService struct {
	Build       composeBuild                 `yaml:"build"`
	Command     []string                     `yaml:"command,omitempty"`
	Environment map[string]string            `yaml:"environment"`
	DependsOn   map[string]composeDependency `yaml:"depends_on,omitempty"`
	Ports       []string                     `yaml:"ports,omitempty"`
	Volumes     []string                     `yaml:"volumes"`
	Networks    []string                     `yaml:"networks"`
	GroupAdd    []string                     `yaml:"group_add,omitempty"`
	Healthcheck *composeHealthcheck          `yaml:"healthcheck,omitempty"`
	Restart     string                       `yaml:"restart,omitempty"`
}

type composeBuild struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile"`
	Args       map[string]string `yaml:"args,omitempty"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test        []string `yaml:"test"`
	Interval    string   `yaml:"interval"`
	Timeout     string   `yaml:"timeout"`
	Retries     int      `yaml:"retries"`
	StartPeriod string   `yaml:"start_period"`
}

func renderCompose(topology *config.Topology, identities map[string]identity, output string) error {
	document := composeFile{
		Name:     topology.NetworkName,
		Services: make(map[string]composeService, len(topology.Chains)*3),
		Volumes:  make(map[string]map[string]any, len(topology.Chains)*2),
		Networks: map[string]composeNetwork{topology.NetworkName: {Driver: "bridge", External: false}},
	}
	for _, chain := range topology.Chains {
		chainIdentity := identities[chain.Name]
		gethName := "geth-" + chain.Name
		deployName := "deploy-" + chain.Name
		mapNodeName := "mapnode-" + chain.Name
		gethVolume := "geth-" + chain.Name + "-data"
		mapNodeVolume := "mapnode-" + chain.Name + "-data"
		chainRoot := fmt.Sprintf("./chains/%s", chain.Name)
		secretRoot := chainRoot + "/secrets"
		deployerSecretMounts := []string{
			secretRoot + "/deployer-keystore.json:/run/secrets/deployer-keystore.json:ro",
			secretRoot + "/deployer-password:/run/secrets/deployer-password:ro",
		}
		directSignerMounts := make([]string, 0, chain.DirectVerifier.AuthorizedSignerCount*2)
		for index := 0; index < chain.DirectVerifier.AuthorizedSignerCount; index++ {
			directSignerMounts = append(directSignerMounts,
				fmt.Sprintf("%s/direct-signer-%d-keystore.json:/run/secrets/direct-signer-%d-keystore.json:ro", secretRoot, index, index),
				fmt.Sprintf("%s/direct-signer-%d-password:/run/secrets/direct-signer-%d-password:ro", secretRoot, index, index),
			)
		}
		document.Volumes[gethVolume] = map[string]any{}
		document.Volumes[mapNodeVolume] = map[string]any{}
		gethMounts := []string{chainRoot + "/genesis.json:/runtime/genesis.json:ro"}
		gethMounts = append(gethMounts, deployerSecretMounts...)
		gethMounts = append(gethMounts, gethVolume+":/data")

		document.Services[gethName] = composeService{
			Build: composeBuild{
				Context: "../..", Dockerfile: "docker/geth.Dockerfile",
				Args: map[string]string{"GETH_IMAGE": topology.Defaults.GethImage},
			},
			Environment: map[string]string{
				"CHAIN_ID":         fmt.Sprint(chain.ChainID),
				"BLOCK_PERIOD":     fmt.Sprint(chain.BlockPeriodSeconds),
				"DEPLOYER_ADDRESS": chainIdentity.DeployerAddress,
				"HTTP_VHOSTS":      gethName + ",localhost",
			},
			Ports:    []string{fmt.Sprintf("127.0.0.1:%d:8545", chain.HostPorts.HTTP), fmt.Sprintf("127.0.0.1:%d:8546", chain.HostPorts.WS)},
			Volumes:  gethMounts,
			Networks: []string{topology.NetworkName},
			Healthcheck: &composeHealthcheck{
				Test:     []string{"CMD", "/usr/local/bin/trustmap-geth-healthcheck"},
				Interval: "2s", Timeout: "2s", Retries: 30, StartPeriod: "5s",
			},
			Restart: "unless-stopped",
		}

		deployMounts := []string{chainRoot + "/direct-verifier-profile.json:/runtime/direct-verifier-profile.json:ro"}
		deployMounts = append(deployMounts, deployerSecretMounts...)
		deployMounts = append(deployMounts, directSignerMounts...)
		deployMounts = append(deployMounts, chainRoot+"/deployment:/output:rw")
		document.Services[deployName] = composeService{
			Build: composeBuild{
				Context: "../..", Dockerfile: "docker/deployer.Dockerfile",
				Args: map[string]string{"FOUNDRY_IMAGE": topology.Defaults.FoundryImage},
			},
			Environment: map[string]string{
				"CHAIN_ID":                fmt.Sprint(chain.ChainID),
				"MERKLE_DEPTH":            fmt.Sprint(chain.MerkleDepth),
				"RPC_URL":                 fmt.Sprintf("http://%s:8545", gethName),
				"DEPLOYER_ADDRESS":        chainIdentity.DeployerAddress,
				"DEPLOYER_KEYSTORE":       "/run/secrets/deployer-keystore.json",
				"DEPLOYER_PASSWORD_FILE":  "/run/secrets/deployer-password",
				"AUTHORIZED_SIGNERS_FILE": "/runtime/direct-verifier-profile.json",
				"MANIFEST_PATH":           "/output/gateway-manifest.json",
			},
			DependsOn: map[string]composeDependency{gethName: {Condition: "service_healthy"}},
			Volumes:   deployMounts,
			Networks:  []string{topology.NetworkName},
		}

		mapNodeMounts := []string{
			chainRoot + "/mapnode.json:/runtime/mapnode.json:ro",
			chainRoot + "/direct-verifier-profile.json:/runtime/direct-verifier-profile.json:ro",
			chainRoot + "/deployment:/runtime/deployment:ro",
			"./p2p/bootstrap.json:/runtime/p2p/bootstrap.json:ro",
			secretRoot + "/mapnode-keystore.json:/run/secrets/mapnode-keystore.json:ro",
			secretRoot + "/mapnode-password:/run/secrets/mapnode-password:ro",
			secretRoot + "/p2p-private-key:/run/secrets/p2p-private-key:ro",
		}
		mapNodeMounts = append(mapNodeMounts, directSignerMounts...)
		mapNodeMounts = append(mapNodeMounts, mapNodeVolume+":/runtime/data")
		document.Services[mapNodeName] = composeService{
			Build: composeBuild{Context: "../..", Dockerfile: "docker/mapnode.Dockerfile"},
			Environment: map[string]string{
				"MAPNODE_CONFIG": "/runtime/mapnode.json",
			},
			DependsOn: map[string]composeDependency{deployName: {Condition: "service_completed_successfully"}},
			Ports: []string{
				fmt.Sprintf("127.0.0.1:%d:8080", chain.HostPorts.MapNodeAPI),
				fmt.Sprintf("127.0.0.1:%d:9000", chain.HostPorts.MapNodeP2P),
			},
			Volumes:  mapNodeMounts,
			Networks: []string{topology.NetworkName},
			GroupAdd: []string{fmt.Sprint(os.Getegid())},
			Restart:  "unless-stopped",
		}
	}
	content, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode compose file: %w", err)
	}
	return atomicWrite(filepath.Join(output, "compose.yaml"), content, 0644)
}
