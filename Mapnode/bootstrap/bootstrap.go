package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/justinzjj/TrustMap_prototype/internal/directprofile"
)

const SupportedConfigVersion = 1

var (
	addressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	hashPattern    = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

type Config struct {
	Version        int            `json:"version"`
	Name           string         `json:"name"`
	HomeChain      HomeChain      `json:"home_chain"`
	API            API            `json:"api"`
	P2P            P2P            `json:"p2p"`
	Database       Database       `json:"database"`
	Signer         Signer         `json:"signer"`
	DirectVerifier DirectVerifier `json:"direct_verifier"`
}

type HomeChain struct {
	Name            string `json:"name"`
	ChainID         string `json:"chain_id"`
	HTTPRPC         string `json:"http_rpc"`
	WSRPC           string `json:"ws_rpc"`
	Confirmations   uint64 `json:"confirmations"`
	GatewayManifest string `json:"gateway_manifest"`
}

type API struct {
	Listen string `json:"listen"`
}

type P2P struct {
	Enabled        bool   `json:"enabled"`
	Listen         string `json:"listen"`
	PrivateKeyFile string `json:"private_key_file"`
	BootstrapFile  string `json:"bootstrap_file"`
}

type Database struct {
	Driver string `json:"driver"`
	Path   string `json:"path"`
}

type Signer struct {
	KeystoreFile string `json:"keystore_file"`
	PasswordFile string `json:"password_file"`
}

type DirectVerifier struct {
	ProfileFile string                 `json:"profile_file"`
	Profile     *DirectVerifierProfile `json:"-"`
}

type DirectVerifierProfile struct {
	Version               int                `json:"version"`
	ProfileID             string             `json:"profile_id"`
	ContractName          string             `json:"contract_name"`
	AuthorizedSigners     []AuthorizedSigner `json:"authorized_signers"`
	SignatureChecks       uint32             `json:"signature_checks"`
	HashRounds            uint32             `json:"hash_rounds"`
	MeasuredDirectCostGas *uint64            `json:"measured_direct_cost_gas"`
}

type AuthorizedSigner struct {
	Address      string `json:"address"`
	KeystoreFile string `json:"keystore_file"`
	PasswordFile string `json:"password_file"`
}

type DeploymentManifest struct {
	Version               int        `json:"version"`
	Status                string     `json:"status"`
	ChainID               string     `json:"chainId"`
	DeploymentBlock       uint64     `json:"deploymentBlock"`
	MerkleDepth           uint8      `json:"merkleDepth"`
	Gateway               string     `json:"gateway"`
	DirectVerifier        string     `json:"directVerifier"`
	ProfileID             string     `json:"profileId"`
	AuthorizedSigners     []string   `json:"authorizedSigners"`
	SignatureChecks       uint32     `json:"signatureChecks"`
	HashRounds            uint32     `json:"hashRounds"`
	MeasuredDirectCostGas *uint64    `json:"measuredDirectCostGas"`
	CodeHashes            CodeHashes `json:"codeHashes"`
	Transactions          any        `json:"transactions,omitempty"`
}

type CodeHashes struct {
	Gateway        string `json:"gateway"`
	DirectVerifier string `json:"directVerifier"`
}

func LoadValidated(configPath string) (Config, DeploymentManifest, error) {
	var config Config
	if configPath == "" {
		return config, DeploymentManifest{}, errors.New("config path is required")
	}
	if err := decodeStrictFile(configPath, &config); err != nil {
		return config, DeploymentManifest{}, fmt.Errorf("load config: %w", err)
	}
	if err := validateConfig(config); err != nil {
		return config, DeploymentManifest{}, fmt.Errorf("validate config: %w", err)
	}
	var profile DirectVerifierProfile
	if err := decodeStrictFile(config.DirectVerifier.ProfileFile, &profile); err != nil {
		return config, DeploymentManifest{}, fmt.Errorf("load direct verifier profile: %w", err)
	}
	if err := validateDirectVerifierProfile(profile); err != nil {
		return config, DeploymentManifest{}, fmt.Errorf("validate direct verifier profile: %w", err)
	}
	config.DirectVerifier.Profile = &profile

	var manifest DeploymentManifest
	if err := decodeStrictFile(config.HomeChain.GatewayManifest, &manifest); err != nil {
		return config, manifest, fmt.Errorf("load deployment manifest: %w", err)
	}
	if err := validateManifest(config.HomeChain.ChainID, manifest); err != nil {
		return config, manifest, fmt.Errorf("validate deployment manifest: %w", err)
	}
	if err := validateManifestProfile(manifest, profile); err != nil {
		return config, manifest, fmt.Errorf("validate deployment manifest profile: %w", err)
	}
	return config, manifest, nil
}

func decodeStrictFile(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateConfig(config Config) error {
	if config.Version != SupportedConfigVersion {
		return fmt.Errorf("unsupported version %d", config.Version)
	}
	if strings.TrimSpace(config.Name) == "" || strings.TrimSpace(config.HomeChain.Name) == "" {
		return errors.New("name and home_chain.name are required")
	}
	if err := validateDecimalID(config.HomeChain.ChainID); err != nil {
		return fmt.Errorf("home_chain.chain_id: %w", err)
	}
	if err := validateURL(config.HomeChain.HTTPRPC, "http", "https"); err != nil {
		return fmt.Errorf("home_chain.http_rpc: %w", err)
	}
	if err := validateURL(config.HomeChain.WSRPC, "ws", "wss"); err != nil {
		return fmt.Errorf("home_chain.ws_rpc: %w", err)
	}
	if config.HomeChain.Confirmations == 0 {
		return errors.New("home_chain.confirmations must be positive")
	}
	if err := validateListen(config.API.Listen); err != nil {
		return fmt.Errorf("api.listen: %w", err)
	}
	if config.Database.Driver != "sqlite" {
		return errors.New("database.driver must be sqlite")
	}
	if config.Database.Path == "" || filepath.Dir(config.Database.Path) == "." {
		return errors.New("database.path must include a runtime directory")
	}
	databaseDir := filepath.Dir(config.Database.Path)
	databaseInfo, err := os.Stat(databaseDir)
	if err != nil || !databaseInfo.IsDir() {
		return fmt.Errorf("database.path parent must be an existing directory: %s", databaseDir)
	}
	if config.P2P.Enabled {
		if strings.TrimSpace(config.P2P.Listen) == "" {
			return errors.New("p2p.listen is required when p2p is enabled")
		}
		if err := requireReadableFile("p2p.private_key_file", config.P2P.PrivateKeyFile); err != nil {
			return err
		}
		if err := requireReadableFile("p2p.bootstrap_file", config.P2P.BootstrapFile); err != nil {
			return err
		}
	}
	for field, path := range map[string]string{
		"home_chain.gateway_manifest":  config.HomeChain.GatewayManifest,
		"signer.keystore_file":         config.Signer.KeystoreFile,
		"signer.password_file":         config.Signer.PasswordFile,
		"direct_verifier.profile_file": config.DirectVerifier.ProfileFile,
	} {
		if err := requireReadableFile(field, path); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectVerifierProfile(profile DirectVerifierProfile) error {
	if profile.Version != 1 {
		return fmt.Errorf("unsupported version %d", profile.Version)
	}
	if strings.TrimSpace(profile.ProfileID) == "" {
		return errors.New("profile_id is required")
	}
	if profile.ContractName != "ExperimentalCostedDirectVerifier" {
		return fmt.Errorf("unsupported contract_name %q", profile.ContractName)
	}
	if profile.ProfileID == directprofile.ID {
		if len(profile.AuthorizedSigners) != directprofile.AuthorizedSignerCount ||
			profile.SignatureChecks != directprofile.SignatureChecks ||
			profile.HashRounds != directprofile.HashRounds ||
			profile.MeasuredDirectCostGas == nil || *profile.MeasuredDirectCostGas != directprofile.MeasuredCostGas {
			return fmt.Errorf(
				"profile %q requires %d authorized signers, signature_checks=%d, hash_rounds=%d, and measured_direct_cost_gas=%d",
				directprofile.ID, directprofile.AuthorizedSignerCount, directprofile.SignatureChecks, directprofile.HashRounds, directprofile.MeasuredCostGas,
			)
		}
	} else if profile.MeasuredDirectCostGas != nil {
		return fmt.Errorf("custom profile %q must not provide measured_direct_cost_gas", profile.ProfileID)
	}
	if len(profile.AuthorizedSigners) == 0 {
		return errors.New("authorized_signers must not be empty")
	}
	if len(profile.AuthorizedSigners) > 4096 {
		return errors.New("authorized_signers must contain at most 4096 entries")
	}
	if profile.SignatureChecks == 0 || profile.SignatureChecks > 4096 || int(profile.SignatureChecks) > len(profile.AuthorizedSigners) {
		return errors.New("signature_checks must be positive, at most 4096, and no greater than authorized_signers length")
	}
	if profile.HashRounds > 16384 {
		return errors.New("hash_rounds must not exceed 16384")
	}
	seen := make(map[string]struct{}, len(profile.AuthorizedSigners))
	for index, signer := range profile.AuthorizedSigners {
		if !addressPattern.MatchString(signer.Address) {
			return fmt.Errorf("authorized_signers[%d].address must be a 20-byte hex address", index)
		}
		canonical := strings.ToLower(signer.Address)
		if _, exists := seen[canonical]; exists {
			return fmt.Errorf("authorized_signers[%d].address is duplicated", index)
		}
		seen[canonical] = struct{}{}
		if err := requireReadableFile(fmt.Sprintf("authorized_signers[%d].keystore_file", index), signer.KeystoreFile); err != nil {
			return err
		}
		if err := requireReadableFile(fmt.Sprintf("authorized_signers[%d].password_file", index), signer.PasswordFile); err != nil {
			return err
		}
		keystoreJSON, err := os.ReadFile(signer.KeystoreFile)
		if err != nil {
			return fmt.Errorf("authorized_signers[%d].keystore_file cannot be read", index)
		}
		password, err := os.ReadFile(signer.PasswordFile)
		if err != nil {
			return fmt.Errorf("authorized_signers[%d].password_file cannot be read", index)
		}
		key, err := keystore.DecryptKey(keystoreJSON, strings.TrimSpace(string(password)))
		if err != nil {
			return fmt.Errorf("authorized_signers[%d].keystore_file cannot be decrypted with password_file", index)
		}
		if !strings.EqualFold(key.Address.Hex(), signer.Address) {
			return fmt.Errorf("authorized_signers[%d].keystore address does not match profile address", index)
		}
	}
	return nil
}

func validateManifest(chainID string, manifest DeploymentManifest) error {
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported version %d", manifest.Version)
	}
	if manifest.Status != "deployed" {
		return fmt.Errorf("status must be deployed, got %q", manifest.Status)
	}
	if manifest.ChainID != chainID {
		return fmt.Errorf("chainId %q does not match home chain %q", manifest.ChainID, chainID)
	}
	if manifest.DeploymentBlock == 0 {
		return errors.New("deploymentBlock must be positive")
	}
	if manifest.MerkleDepth == 0 || manifest.MerkleDepth > 32 {
		return errors.New("merkleDepth must be between 1 and 32")
	}
	if !addressPattern.MatchString(manifest.Gateway) || !addressPattern.MatchString(manifest.DirectVerifier) {
		return errors.New("gateway and directVerifier must be 20-byte hex addresses")
	}
	if !hashPattern.MatchString(manifest.CodeHashes.Gateway) {
		return errors.New("codeHashes.gateway must be a 32-byte hex hash")
	}
	if !hashPattern.MatchString(manifest.CodeHashes.DirectVerifier) {
		return errors.New("codeHashes.directVerifier must be a 32-byte hex hash")
	}
	return nil
}

func validateManifestProfile(manifest DeploymentManifest, profile DirectVerifierProfile) error {
	if manifest.ProfileID != profile.ProfileID {
		return fmt.Errorf("profileId %q does not match profile %q", manifest.ProfileID, profile.ProfileID)
	}
	if manifest.SignatureChecks != profile.SignatureChecks {
		return fmt.Errorf("signatureChecks %d does not match profile %d", manifest.SignatureChecks, profile.SignatureChecks)
	}
	if manifest.HashRounds != profile.HashRounds {
		return fmt.Errorf("hashRounds %d does not match profile %d", manifest.HashRounds, profile.HashRounds)
	}
	if (manifest.MeasuredDirectCostGas == nil) != (profile.MeasuredDirectCostGas == nil) ||
		(manifest.MeasuredDirectCostGas != nil && *manifest.MeasuredDirectCostGas != *profile.MeasuredDirectCostGas) {
		return errors.New("measuredDirectCostGas does not match profile")
	}
	if len(manifest.AuthorizedSigners) != len(profile.AuthorizedSigners) {
		return errors.New("authorizedSigners length does not match profile")
	}
	for index, signer := range profile.AuthorizedSigners {
		if !strings.EqualFold(manifest.AuthorizedSigners[index], signer.Address) {
			return fmt.Errorf("authorizedSigners[%d] does not match profile", index)
		}
	}
	return nil
}

func validateDecimalID(value string) error {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return errors.New("must be a canonical positive decimal string")
	}
	return nil
}

func validateURL(value string, schemes ...string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return errors.New("must be an absolute URL")
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("scheme %q is not allowed", parsed.Scheme)
}

func validateListen(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return errors.New("port must be positive")
	}
	return nil
}

func requireReadableFile(field, path string) error {
	if path == "" {
		return fmt.Errorf("%s is required", field)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s must be a readable file: %w", field, err)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return fmt.Errorf("%s: %w", field, statErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", field)
	}
	if closeErr != nil {
		return fmt.Errorf("%s: %w", field, closeErr)
	}
	return nil
}

func NewHealthHandler(ready bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"live"}`+"\n")
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"not_ready"}`+"\n")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ready"}`+"\n")
	})
	return mux
}

func CheckHealth(endpoint string) error {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("healthcheck URL must be absolute http(s)")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("healthcheck returned HTTP %d", response.StatusCode)
	}
	return nil
}
