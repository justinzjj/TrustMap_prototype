package bootstrap

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

func writeFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeEncryptedKeystore(t *testing.T, dir, name, privateKeyHex, password string) (string, string) {
	t.Helper()
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	key := &keystore.Key{Id: uuid.New(), Address: crypto.PubkeyToAddress(privateKey.PublicKey), PrivateKey: privateKey}
	encrypted, err := keystore.EncryptKey(key, password, keystore.LightScryptN, keystore.LightScryptP)
	if err != nil {
		t.Fatal(err)
	}
	return writeFixture(t, dir, name, string(encrypted)), key.Address.Hex()
}

func validFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	keystore := writeFixture(t, dir, "signer.json", `{}`)
	password := writeFixture(t, dir, "password", "secret\n")
	identity := writeFixture(t, dir, "p2p.key", "identity\n")
	verifierKeystore, verifierAddress := writeEncryptedKeystore(t, dir, "verifier-signer.json", strings.Repeat("1", 64), "secret")
	verifierPassword := writeFixture(t, dir, "verifier-password", "secret\n")
	verifierKeystore2, verifierAddress2 := writeEncryptedKeystore(t, dir, "verifier-signer-2.json", strings.Repeat("2", 64), "secret")
	verifierPassword2 := writeFixture(t, dir, "verifier-password-2", "secret\n")
	bootstrap := writeFixture(t, dir, "bootstrap.json", `{"peers":[]}`)
	profile := writeFixture(t, dir, "direct-verifier-profile.json", fmt.Sprintf(`{
  "version": 1,
  "profile_id": "local-2x3",
  "contract_name": "ExperimentalCostedDirectVerifier",
  "authorized_signers": [
    {"address": %q, "keystore_file": %q, "password_file": %q},
    {"address": %q, "keystore_file": %q, "password_file": %q}
  ],
  "signature_checks": 2,
  "hash_rounds": 3,
  "measured_direct_cost_gas": null
}`, verifierAddress, verifierKeystore, verifierPassword, verifierAddress2, verifierKeystore2, verifierPassword2))
	manifest := writeFixture(t, dir, "deployment.json", fmt.Sprintf(`{
  "version": 1,
  "status": "deployed",
  "chainId": "10001",
  "deploymentBlock": 3,
  "merkleDepth": 8,
  "gateway": "0x1111111111111111111111111111111111111111",
  "directVerifier": "0x2222222222222222222222222222222222222222",
  "profileId": "local-2x3",
  "authorizedSigners": [
    %q,
    %q
  ],
  "signatureChecks": 2,
  "hashRounds": 3,
  "measuredDirectCostGas": null,
  "codeHashes": {
    "gateway": "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "directVerifier": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  }
}`, verifierAddress, verifierAddress2))
	config := writeFixture(t, dir, "mapnode.json", fmt.Sprintf(`{
  "version": 1,
  "name": "mapnode-a",
  "home_chain": {
    "name": "chain-a",
    "chain_id": "10001",
    "http_rpc": "http://geth-a:8545",
    "ws_rpc": "ws://geth-a:8546",
    "confirmations": 2,
    "gateway_manifest": %q
  },
  "api": {"listen": "127.0.0.1:18080"},
  "p2p": {
    "enabled": true,
    "listen": "/ip4/0.0.0.0/tcp/4001",
	"private_key_file": %q,
    "bootstrap_file": %q
  },
  "database": {"driver": "sqlite", "path": %q},
	"signer": {"keystore_file": %q, "password_file": %q},
  "direct_verifier": {"profile_file": %q}
}`, manifest, identity, bootstrap, filepath.Join(dir, "mapnode.db"), keystore, password, profile))
	return config, manifest
}

func TestLoadValidatedRequiresManifestCodeHashes(t *testing.T) {
	configPath, manifestPath := validFixture(t)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"gateway": "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"gateway": ""`, 1))
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "codeHashes.gateway") {
		t.Fatalf("expected required gateway code hash error, got %v", err)
	}
}

func TestLoadValidatedRejectsAuthorizedSignerKeystoreAddressMismatch(t *testing.T) {
	configPath, _ := validFixture(t)
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	profileBody, err := os.ReadFile(config.DirectVerifier.ProfileFile)
	if err != nil {
		t.Fatal(err)
	}
	var profile DirectVerifierProfile
	if err := json.Unmarshal(profileBody, &profile); err != nil {
		t.Fatal(err)
	}
	profile.AuthorizedSigners[0].Address = "0x3333333333333333333333333333333333333333"
	updated, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.DirectVerifier.ProfileFile, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected signer keystore address mismatch, got %v", err)
	}
}

func TestLoadValidatedDoesNotExposeAuthorizedSignerPassword(t *testing.T) {
	configPath, _ := validFixture(t)
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	profileBody, err := os.ReadFile(config.DirectVerifier.ProfileFile)
	if err != nil {
		t.Fatal(err)
	}
	var profile DirectVerifierProfile
	if err := json.Unmarshal(profileBody, &profile); err != nil {
		t.Fatal(err)
	}
	const sensitivePassword = "do-not-leak-this-password"
	if err := os.WriteFile(profile.AuthorizedSigners[0].PasswordFile, []byte(sensitivePassword), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadValidated(configPath)
	if err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("expected decrypt error, got %v", err)
	}
	if strings.Contains(err.Error(), sensitivePassword) {
		t.Fatalf("error exposed signer password: %v", err)
	}
}

func TestValidateDirectVerifierProfileRejectsMoreThan4096Signers(t *testing.T) {
	profile := DirectVerifierProfile{
		Version:           1,
		ProfileID:         "too-many",
		ContractName:      "ExperimentalCostedDirectVerifier",
		SignatureChecks:   1,
		AuthorizedSigners: make([]AuthorizedSigner, 4097),
	}
	if err := validateDirectVerifierProfile(profile); err == nil || !strings.Contains(err.Error(), "at most 4096") {
		t.Fatalf("expected signer count limit error, got %v", err)
	}
}

func TestValidateDirectVerifierProfileLocksReservedCalibration(t *testing.T) {
	measured := uint64(3000096)
	base := DirectVerifierProfile{
		Version: 1, ProfileID: "pow-spv-3m", ContractName: "ExperimentalCostedDirectVerifier",
		AuthorizedSigners: make([]AuthorizedSigner, 3), SignatureChecks: 3, HashRounds: 4497,
		MeasuredDirectCostGas: &measured,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*DirectVerifierProfile)
	}{
		{"missing cost", func(profile *DirectVerifierProfile) { profile.MeasuredDirectCostGas = nil }},
		{"wrong cost", func(profile *DirectVerifierProfile) { value := uint64(3000095); profile.MeasuredDirectCostGas = &value }},
		{"wrong signer count", func(profile *DirectVerifierProfile) { profile.AuthorizedSigners = make([]AuthorizedSigner, 4) }},
		{"wrong checks", func(profile *DirectVerifierProfile) { profile.SignatureChecks = 2 }},
		{"wrong rounds", func(profile *DirectVerifierProfile) { profile.HashRounds = 4496 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := base
			tc.mutate(&profile)
			if err := validateDirectVerifierProfile(profile); err == nil || !strings.Contains(err.Error(), "pow-spv-3m") {
				t.Fatalf("expected reserved calibration error, got %v", err)
			}
		})
	}
}

func TestValidateDirectVerifierProfileRejectsMeasuredCustomProfile(t *testing.T) {
	measured := uint64(3000096)
	profile := DirectVerifierProfile{
		Version: 1, ProfileID: "custom-profile", ContractName: "ExperimentalCostedDirectVerifier",
		AuthorizedSigners: []AuthorizedSigner{{}}, SignatureChecks: 1, MeasuredDirectCostGas: &measured,
	}
	if err := validateDirectVerifierProfile(profile); err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("expected custom measured cost rejection, got %v", err)
	}
}

func TestValidateManifestProfileCrossChecksMeasuredCost(t *testing.T) {
	measured := uint64(3000096)
	wrong := uint64(3000095)
	profile := DirectVerifierProfile{
		ProfileID: "pow-spv-3m", AuthorizedSigners: []AuthorizedSigner{{Address: "0x1111111111111111111111111111111111111111"}},
		SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: &measured,
	}
	manifest := DeploymentManifest{
		ProfileID: "pow-spv-3m", AuthorizedSigners: []string{"0x1111111111111111111111111111111111111111"},
		SignatureChecks: 3, HashRounds: 4497, MeasuredDirectCostGas: &wrong,
	}
	if err := validateManifestProfile(manifest, profile); err == nil || !strings.Contains(err.Error(), "measuredDirectCostGas") {
		t.Fatalf("expected measured cost mismatch, got %v", err)
	}
	manifest.MeasuredDirectCostGas = &measured
	if err := validateManifestProfile(manifest, profile); err != nil {
		t.Fatalf("exact measured cost should match: %v", err)
	}

	custom := DirectVerifierProfile{ProfileID: "custom", AuthorizedSigners: profile.AuthorizedSigners, SignatureChecks: 1}
	customManifest := DeploymentManifest{ProfileID: "custom", AuthorizedSigners: manifest.AuthorizedSigners, SignatureChecks: 1}
	if err := validateManifestProfile(customManifest, custom); err != nil {
		t.Fatalf("nil custom costs should remain compatible: %v", err)
	}
	customManifest.MeasuredDirectCostGas = &measured
	if err := validateManifestProfile(customManifest, custom); err == nil || !strings.Contains(err.Error(), "measuredDirectCostGas") {
		t.Fatalf("expected non-nil custom manifest cost rejection, got %v", err)
	}
}

func TestValidateRuntimeAcceptsMatchingChainAndCodeHashes(t *testing.T) {
	gatewayCode := "0x6001600055"
	directCode := "0x6002600055"
	server := newRuntimeRPCServer(t, "0x2711", map[string]string{
		"0x1111111111111111111111111111111111111111": gatewayCode,
		"0x2222222222222222222222222222222222222222": directCode,
	}, "")
	defer server.Close()

	config, manifest := runtimeFixture(server.URL, gatewayCode, directCode)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ValidateRuntime(ctx, config, manifest); err != nil {
		t.Fatalf("ValidateRuntime() error = %v", err)
	}
}

func TestValidateRuntimeRejectsChainMismatch(t *testing.T) {
	server := newRuntimeRPCServer(t, "0x2712", nil, "")
	defer server.Close()
	config, manifest := runtimeFixture(server.URL, "0x60", "0x61")
	if err := ValidateRuntime(context.Background(), config, manifest); err == nil || !strings.Contains(err.Error(), "chain ID") {
		t.Fatalf("expected chain ID mismatch, got %v", err)
	}
}

func TestValidateRuntimeRejectsEmptyContractCode(t *testing.T) {
	directCode := "0x6002600055"
	server := newRuntimeRPCServer(t, "0x2711", map[string]string{
		"0x1111111111111111111111111111111111111111": "0x",
		"0x2222222222222222222222222222222222222222": directCode,
	}, "")
	defer server.Close()
	config, manifest := runtimeFixture(server.URL, "0x60", directCode)
	if err := ValidateRuntime(context.Background(), config, manifest); err == nil || !strings.Contains(err.Error(), "empty code") {
		t.Fatalf("expected empty code error, got %v", err)
	}
}

func TestValidateRuntimeRejectsContractCodeHashMismatch(t *testing.T) {
	gatewayCode := "0x6001600055"
	directCode := "0x6002600055"
	server := newRuntimeRPCServer(t, "0x2711", map[string]string{
		"0x1111111111111111111111111111111111111111": gatewayCode,
		"0x2222222222222222222222222222222222222222": directCode,
	}, "")
	defer server.Close()
	config, manifest := runtimeFixture(server.URL, gatewayCode, "0x6003600055")
	if err := ValidateRuntime(context.Background(), config, manifest); err == nil || !strings.Contains(err.Error(), "direct verifier code hash") {
		t.Fatalf("expected direct verifier code hash mismatch, got %v", err)
	}
}

func TestValidateRuntimePropagatesRPCError(t *testing.T) {
	gatewayCode := "0x6001600055"
	directCode := "0x6002600055"
	server := newRuntimeRPCServer(t, "0x2711", map[string]string{
		"0x1111111111111111111111111111111111111111": gatewayCode,
		"0x2222222222222222222222222222222222222222": directCode,
	}, "eth_getCode")
	defer server.Close()
	config, manifest := runtimeFixture(server.URL, gatewayCode, directCode)
	if err := ValidateRuntime(context.Background(), config, manifest); err == nil || !strings.Contains(err.Error(), "RPC") {
		t.Fatalf("expected RPC error, got %v", err)
	}
}

func runtimeFixture(rpcURL, gatewayCode, directCode string) (Config, DeploymentManifest) {
	return Config{HomeChain: HomeChain{ChainID: "10001", HTTPRPC: rpcURL}}, DeploymentManifest{
		Gateway:        "0x1111111111111111111111111111111111111111",
		DirectVerifier: "0x2222222222222222222222222222222222222222",
		CodeHashes: CodeHashes{
			Gateway:        crypto.Keccak256Hash(mustDecodeHex(gatewayCode)).Hex(),
			DirectVerifier: crypto.Keccak256Hash(mustDecodeHex(directCode)).Hex(),
		},
	}
}

func mustDecodeHex(value string) []byte {
	decoded, err := fmtHexDecode(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

func fmtHexDecode(value string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(value, "0x"))
}

func newRuntimeRPCServer(t *testing.T, chainID string, codes map[string]string, errorMethod string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string            `json:"method"`
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method == errorMethod {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"error":   map[string]any{"code": -32000, "message": "forced RPC error"},
			})
			return
		}
		var result string
		switch request.Method {
		case "eth_chainId":
			result = chainID
		case "eth_getCode":
			if len(request.Params) != 2 {
				http.Error(w, "invalid eth_getCode params", http.StatusBadRequest)
				return
			}
			var address string
			if err := json.Unmarshal(request.Params[0], &address); err != nil {
				http.Error(w, "invalid address", http.StatusBadRequest)
				return
			}
			var blockTag string
			if err := json.Unmarshal(request.Params[1], &blockTag); err != nil || blockTag != "latest" {
				http.Error(w, "expected latest block", http.StatusBadRequest)
				return
			}
			result = codes[strings.ToLower(address)]
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
}

func TestLoadValidatedRejectsDirectVerifierProfileMismatch(t *testing.T) {
	configPath, manifestPath := validFixture(t)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"signatureChecks": 2`, `"signatureChecks": 9`, 1))
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "signatureChecks") {
		t.Fatalf("expected profile mismatch error, got %v", err)
	}
}

func TestLoadValidatedAcceptsPhaseTwoConfig(t *testing.T) {
	configPath, _ := validFixture(t)
	loaded, manifest, err := LoadValidated(configPath)
	if err != nil {
		t.Fatalf("LoadValidated() error = %v", err)
	}
	if loaded.HomeChain.ChainID != "10001" || manifest.Gateway == "" {
		t.Fatalf("unexpected validated values: %#v %#v", loaded.HomeChain, manifest)
	}
	if loaded.DirectVerifier.Profile == nil || loaded.DirectVerifier.Profile.MeasuredDirectCostGas != nil {
		t.Fatalf("expected an attached uncalibrated profile, got %#v", loaded.DirectVerifier.Profile)
	}
}

func TestLoadValidatedRejectsMissingDatabaseDirectory(t *testing.T) {
	configPath, _ := validFixture(t)
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"path": "`, `"path": "/definitely/missing/runtime/`, 1))
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "database.path") {
		t.Fatalf("expected database directory error, got %v", err)
	}
}

func TestLoadValidatedRejectsUnknownConfigField(t *testing.T) {
	configPath, _ := validFixture(t)
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"name": "mapnode-a",`, `"name": "mapnode-a", "surprise": true,`, 1))
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadValidatedRejectsManifestChainMismatch(t *testing.T) {
	configPath, manifestPath := validFixture(t)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"chainId": "10001"`, `"chainId": "10002"`, 1))
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "chainId") {
		t.Fatalf("expected chain mismatch error, got %v", err)
	}
}

func TestLoadValidatedRequiresDeploymentFixedMerkleDepth(t *testing.T) {
	configPath, manifestPath := validFixture(t)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{`"merkleDepth": 0`, `"merkleDepth": 33`} {
		changed := regexp.MustCompile(`"merkleDepth": 8`).ReplaceAll(body, []byte(replacement))
		if err := os.WriteFile(manifestPath, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadValidated(configPath); err == nil || !strings.Contains(err.Error(), "merkleDepth") {
			t.Fatalf("LoadValidated accepted %s: %v", replacement, err)
		}
	}
}

func TestHealthHandlerReadinessTracksValidation(t *testing.T) {
	for _, tc := range []struct {
		ready bool
		path  string
		want  int
	}{
		{false, "/health/live", http.StatusOK},
		{false, "/health/ready", http.StatusServiceUnavailable},
		{true, "/health/ready", http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		NewHealthHandler(tc.ready).ServeHTTP(recorder, request)
		if recorder.Code != tc.want {
			t.Errorf("ready=%v path=%s: got %d, want %d", tc.ready, tc.path, recorder.Code, tc.want)
		}
	}
}

func TestCheckHealthUsesHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := CheckHealth(server.URL + "/ok"); err != nil {
		t.Fatalf("CheckHealth(ok) error = %v", err)
	}
	if err := CheckHealth(server.URL + "/bad"); err == nil {
		t.Fatal("CheckHealth(bad) unexpectedly succeeded")
	}
}
