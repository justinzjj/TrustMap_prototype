package bootstrap

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

const maxRPCResponseBytes = 1 << 20

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      uint64 `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code int `json:"code"`
}

// ValidateRuntime binds a MapNode to the configured chain and exact deployed
// verifier bytecode before the node advertises readiness.
func ValidateRuntime(ctx context.Context, config Config, manifest DeploymentManifest) error {
	client := &http.Client{}
	var chainIDHex string
	if err := callRPC(ctx, client, config.HomeChain.HTTPRPC, 1, "eth_chainId", nil, &chainIDHex); err != nil {
		return fmt.Errorf("validate home chain ID: %w", err)
	}
	actualChainID, err := parseHexUint64(chainIDHex)
	if err != nil {
		return fmt.Errorf("validate home chain ID: invalid eth_chainId result")
	}
	expectedChainID, err := strconv.ParseUint(config.HomeChain.ChainID, 10, 64)
	if err != nil {
		return errors.New("validate home chain ID: invalid configured chain ID")
	}
	if actualChainID != expectedChainID {
		return fmt.Errorf("runtime chain ID %d does not match configured chain ID %d", actualChainID, expectedChainID)
	}

	if err := validateContractCode(ctx, client, config.HomeChain.HTTPRPC, 2, "gateway", manifest.Gateway, manifest.CodeHashes.Gateway); err != nil {
		return err
	}
	if err := validateContractCode(ctx, client, config.HomeChain.HTTPRPC, 3, "direct verifier", manifest.DirectVerifier, manifest.CodeHashes.DirectVerifier); err != nil {
		return err
	}
	return nil
}

func validateContractCode(ctx context.Context, client *http.Client, endpoint string, requestID uint64, name, address, expectedHash string) error {
	var encodedCode string
	if err := callRPC(ctx, client, endpoint, requestID, "eth_getCode", []any{address, "latest"}, &encodedCode); err != nil {
		return fmt.Errorf("validate %s code: %w", name, err)
	}
	code, err := decodeHexData(encodedCode)
	if err != nil {
		return fmt.Errorf("validate %s code: invalid eth_getCode result", name)
	}
	if len(code) == 0 {
		return fmt.Errorf("validate %s code: empty code at deployment address", name)
	}
	actualHash := crypto.Keccak256Hash(code).Hex()
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("%s code hash mismatch", name)
	}
	return nil
}

func callRPC(ctx context.Context, client *http.Client, endpoint string, requestID uint64, method string, params []any, result any) error {
	if params == nil {
		params = []any{}
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: requestID})
	if err != nil {
		return errors.New("encode RPC request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("create RPC request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("RPC request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("RPC returned HTTP %d", response.StatusCode)
	}
	var decoded rpcResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxRPCResponseBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return errors.New("decode RPC response")
	}
	if decoded.Error != nil {
		return fmt.Errorf("RPC method %s returned error code %d", method, decoded.Error.Code)
	}
	if len(decoded.Result) == 0 || string(decoded.Result) == "null" {
		return fmt.Errorf("RPC method %s returned no result", method)
	}
	if err := json.Unmarshal(decoded.Result, result); err != nil {
		return fmt.Errorf("RPC method %s returned an invalid result", method)
	}
	return nil
}

func parseHexUint64(value string) (uint64, error) {
	if !strings.HasPrefix(value, "0x") || len(value) <= 2 {
		return 0, errors.New("invalid hex quantity")
	}
	return strconv.ParseUint(value[2:], 16, 64)
}

func decodeHexData(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "0x") {
		return nil, errors.New("missing hex prefix")
	}
	encoded := value[2:]
	if len(encoded)%2 != 0 {
		return nil, errors.New("odd-length hex data")
	}
	return hex.DecodeString(encoded)
}
