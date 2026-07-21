package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

func TestRunHealthcheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var stderr bytes.Buffer
	if code := run([]string{"--healthcheck", server.URL}, &stderr); code != 0 {
		t.Fatalf("run healthcheck code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunHealthcheckIgnoresEnvironmentConfigDefault(t *testing.T) {
	t.Setenv("MAPNODE_CONFIG", "/runtime/mapnode.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var stderr bytes.Buffer
	if code := run([]string{"--healthcheck", server.URL}, &stderr); code != 0 {
		t.Fatalf("run healthcheck with MAPNODE_CONFIG code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunRejectsExplicitConfigWithHealthcheck(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--config", "/runtime/mapnode.json", "--healthcheck", "http://127.0.0.1:1"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("run explicit config+healthcheck code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunRequiresConfig(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code == 0 || !strings.Contains(stderr.String(), "--config") {
		t.Fatalf("run without config code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunRejectsRuntimeChainMismatchBeforeServingReady(t *testing.T) {
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2712"}`))
	}))
	defer rpcServer.Close()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	configPath := writeRuntimeMismatchFixture(t, rpcServer.URL, occupied.Addr().String())

	var stderr bytes.Buffer
	code := run([]string{"--config", configPath}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "runtime validation failed") || !strings.Contains(stderr.String(), "chain ID") {
		t.Fatalf("run runtime mismatch code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "fixture-password") {
		t.Fatalf("run exposed fixture password: %q", stderr.String())
	}
}

func writeRuntimeMismatchFixture(t *testing.T, rpcURL, listen string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	privateKey, err := crypto.HexToECDSA(strings.Repeat("3", 64))
	if err != nil {
		t.Fatal(err)
	}
	key := &keystore.Key{Id: uuid.New(), Address: crypto.PubkeyToAddress(privateKey.PublicKey), PrivateKey: privateKey}
	const password = "fixture-password"
	encrypted, err := keystore.EncryptKey(key, password, keystore.LightScryptN, keystore.LightScryptP)
	if err != nil {
		t.Fatal(err)
	}
	directKeystore := write("direct-keystore.json", string(encrypted))
	directPassword := write("direct-password", password+"\n")
	transactionKeystore := write("transaction-keystore.json", `{}`)
	transactionPassword := write("transaction-password", "unused\n")
	profile := write("profile.json", fmt.Sprintf(`{
  "version": 1,
  "profile_id": "runtime-test",
  "contract_name": "ExperimentalCostedDirectVerifier",
  "authorized_signers": [{"address": %q, "keystore_file": %q, "password_file": %q}],
  "signature_checks": 1,
  "hash_rounds": 0,
  "measured_direct_cost_gas": null
}`, key.Address.Hex(), directKeystore, directPassword))
	manifest := write("deployment.json", fmt.Sprintf(`{
  "version": 1,
  "status": "deployed",
  "chainId": "10001",
  "deploymentBlock": 1,
  "merkleDepth": 8,
  "gateway": "0x1111111111111111111111111111111111111111",
  "directVerifier": "0x2222222222222222222222222222222222222222",
  "profileId": "runtime-test",
  "authorizedSigners": [%q],
  "signatureChecks": 1,
  "hashRounds": 0,
  "codeHashes": {
    "gateway": "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "directVerifier": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  }
}`, key.Address.Hex()))
	return write("mapnode.json", fmt.Sprintf(`{
  "version": 1,
  "name": "mapnode-runtime-test",
  "home_chain": {
    "name": "chain-runtime-test",
    "chain_id": "10001",
    "http_rpc": %q,
    "ws_rpc": "ws://127.0.0.1:8546",
    "confirmations": 1,
    "gateway_manifest": %q
  },
  "api": {"listen": %q},
  "p2p": {"enabled": false, "listen": "", "private_key_file": "", "bootstrap_file": ""},
  "database": {"driver": "sqlite", "path": %q},
  "signer": {"keystore_file": %q, "password_file": %q},
  "direct_verifier": {"profile_file": %q}
}`, rpcURL, manifest, listen, filepath.Join(dir, "mapnode.db"), transactionKeystore, transactionPassword, profile))
}
