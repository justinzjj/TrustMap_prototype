package topology

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

type identity struct {
	Version           int      `json:"version"`
	DeployerAddress   string   `json:"deployer_address"`
	MapNodeAddress    string   `json:"mapnode_address"`
	AuthorizedSigners []string `json:"authorized_signers"`
	PeerID            string   `json:"peer_id"`
}

type generatedSecret struct {
	name    string
	content []byte
}

func loadOrCreateIdentity(chainDir string, signerCount int) (identity, error) {
	identityPath := filepath.Join(chainDir, "identity.json")
	content, err := os.ReadFile(identityPath)
	if err == nil {
		var existing identity
		if err := json.Unmarshal(content, &existing); err != nil {
			return identity{}, fmt.Errorf("decode existing identity %q: %w", identityPath, err)
		}
		if err := normalizeSecretPermissions(chainDir); err != nil {
			return identity{}, err
		}
		if err := validateIdentity(chainDir, existing, signerCount); err != nil {
			return identity{}, err
		}
		return existing, nil
	}
	if !os.IsNotExist(err) {
		return identity{}, fmt.Errorf("read existing identity %q: %w", identityPath, err)
	}
	secretDir := filepath.Join(chainDir, "secrets")
	entries, err := os.ReadDir(secretDir)
	if err != nil && !os.IsNotExist(err) {
		return identity{}, fmt.Errorf("inspect secret directory %q: %w", secretDir, err)
	}
	if len(entries) != 0 {
		return identity{}, fmt.Errorf("identity is missing but secret directory %q is not empty", secretDir)
	}
	return createIdentity(chainDir, signerCount)
}

func createIdentity(chainDir string, signerCount int) (identity, error) {
	secretDir := filepath.Join(chainDir, "secrets")
	if err := ensureDir(secretDir, 0750); err != nil {
		return identity{}, err
	}
	result := identity{Version: 1}
	secrets := make([]generatedSecret, 0, 5+signerCount*2)

	deployerAddress, deployerSecrets, err := createEVMSecrets("deployer")
	if err != nil {
		return identity{}, err
	}
	result.DeployerAddress = deployerAddress
	secrets = append(secrets, deployerSecrets...)
	mapNodeAddress, mapNodeSecrets, err := createEVMSecrets("mapnode")
	if err != nil {
		return identity{}, err
	}
	result.MapNodeAddress = mapNodeAddress
	secrets = append(secrets, mapNodeSecrets...)
	for index := 0; index < signerCount; index++ {
		name := fmt.Sprintf("direct-signer-%d", index)
		address, signerSecrets, err := createEVMSecrets(name)
		if err != nil {
			return identity{}, err
		}
		result.AuthorizedSigners = append(result.AuthorizedSigners, address)
		secrets = append(secrets, signerSecrets...)
	}

	privateKey, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return identity{}, fmt.Errorf("generate libp2p Ed25519 key: %w", err)
	}
	marshaled, err := libp2pcrypto.MarshalPrivateKey(privateKey)
	if err != nil {
		return identity{}, fmt.Errorf("marshal libp2p private key: %w", err)
	}
	peerID, err := peer.IDFromPrivateKey(privateKey)
	if err != nil {
		return identity{}, fmt.Errorf("derive libp2p peer ID: %w", err)
	}
	result.PeerID = peerID.String()
	secrets = append(secrets, generatedSecret{name: "p2p-private-key", content: []byte(base64.StdEncoding.EncodeToString(marshaled) + "\n")})

	for _, secret := range secrets {
		if err := atomicWrite(filepath.Join(secretDir, secret.name), secret.content, 0640); err != nil {
			return identity{}, err
		}
	}
	identityJSON, err := marshalJSON(result)
	if err != nil {
		return identity{}, fmt.Errorf("encode public identity: %w", err)
	}
	if err := atomicWrite(filepath.Join(chainDir, "identity.json"), identityJSON, 0644); err != nil {
		return identity{}, err
	}
	return result, nil
}

func normalizeSecretPermissions(chainDir string) error {
	secretDir := filepath.Join(chainDir, "secrets")
	if err := ensureDir(secretDir, 0750); err != nil {
		return err
	}
	entries, err := os.ReadDir(secretDir)
	if err != nil {
		return fmt.Errorf("inspect secret directory %q: %w", secretDir, err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect secret entry %q: %w", filepath.Join(secretDir, entry.Name()), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("secret directory %q contains non-regular entry %q", secretDir, entry.Name())
		}
		path := filepath.Join(secretDir, entry.Name())
		if err := os.Chmod(path, 0640); err != nil {
			return fmt.Errorf("set secret permissions %q: %w", path, err)
		}
	}
	return nil
}

func createEVMSecrets(name string) (string, []generatedSecret, error) {
	privateKey, err := gethcrypto.GenerateKey()
	if err != nil {
		return "", nil, fmt.Errorf("generate %s EVM key: %w", name, err)
	}
	keyID, err := uuid.NewRandom()
	if err != nil {
		return "", nil, fmt.Errorf("generate %s keystore ID: %w", name, err)
	}
	address := gethcrypto.PubkeyToAddress(privateKey.PublicKey)
	key := &keystore.Key{Id: keyID, Address: address, PrivateKey: privateKey}
	passwordBytes := make([]byte, 32)
	if _, err := rand.Read(passwordBytes); err != nil {
		return "", nil, fmt.Errorf("generate %s password: %w", name, err)
	}
	password := base64.RawURLEncoding.EncodeToString(passwordBytes)
	encrypted, err := keystore.EncryptKey(key, password, keystore.LightScryptN, keystore.LightScryptP)
	if err != nil {
		return "", nil, fmt.Errorf("encrypt %s keystore: %w", name, err)
	}
	return address.Hex(), []generatedSecret{
		{name: name + "-keystore.json", content: append(encrypted, '\n')},
		{name: name + "-password", content: []byte(password + "\n")},
	}, nil
}

func validateIdentity(chainDir string, value identity, signerCount int) error {
	if value.Version != 1 {
		return fmt.Errorf("existing identity %q has unsupported version %d", chainDir, value.Version)
	}
	if len(value.AuthorizedSigners) != signerCount {
		return fmt.Errorf("existing identity %q has %d authorized signers, topology requires %d", chainDir, len(value.AuthorizedSigners), signerCount)
	}
	if err := validateEVMSecret(chainDir, "deployer", value.DeployerAddress); err != nil {
		return err
	}
	if err := validateEVMSecret(chainDir, "mapnode", value.MapNodeAddress); err != nil {
		return err
	}
	for index, address := range value.AuthorizedSigners {
		if err := validateEVMSecret(chainDir, fmt.Sprintf("direct-signer-%d", index), address); err != nil {
			return err
		}
	}
	privateKeyPath := filepath.Join(chainDir, "secrets", "p2p-private-key")
	encoded, err := os.ReadFile(privateKeyPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("required secret is missing: %s", privateKeyPath)
	}
	if err != nil {
		return fmt.Errorf("read p2p private key %q: %w", privateKeyPath, err)
	}
	marshaled, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return fmt.Errorf("decode p2p private key %q: %w", privateKeyPath, err)
	}
	privateKey, err := libp2pcrypto.UnmarshalPrivateKey(marshaled)
	if err != nil {
		return fmt.Errorf("unmarshal p2p private key %q: %w", privateKeyPath, err)
	}
	peerID, err := peer.IDFromPrivateKey(privateKey)
	if err != nil || peerID.String() != value.PeerID {
		return fmt.Errorf("p2p private key %q does not match identity peer_id", privateKeyPath)
	}
	return nil
}

func validateEVMSecret(chainDir, name, wantAddress string) error {
	if !common.IsHexAddress(wantAddress) {
		return fmt.Errorf("existing identity has invalid %s address %q", name, wantAddress)
	}
	keystorePath := filepath.Join(chainDir, "secrets", name+"-keystore.json")
	passwordPath := filepath.Join(chainDir, "secrets", name+"-password")
	keystoreJSON, err := os.ReadFile(keystorePath)
	if os.IsNotExist(err) {
		return fmt.Errorf("required secret is missing: %s", keystorePath)
	}
	if err != nil {
		return fmt.Errorf("read keystore %q: %w", keystorePath, err)
	}
	password, err := os.ReadFile(passwordPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("required secret is missing: %s", passwordPath)
	}
	if err != nil {
		return fmt.Errorf("read password %q: %w", passwordPath, err)
	}
	key, err := keystore.DecryptKey(keystoreJSON, strings.TrimSpace(string(password)))
	if err != nil {
		return fmt.Errorf("decrypt existing keystore %q: %w", keystorePath, err)
	}
	if !strings.EqualFold(key.Address.Hex(), wantAddress) {
		return fmt.Errorf("existing keystore %q does not match public identity address", keystorePath)
	}
	return nil
}

func marshalJSON(value any) ([]byte, error) {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}
