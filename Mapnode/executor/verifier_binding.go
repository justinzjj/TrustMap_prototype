package executor

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

func ValidateVerifierBinding(ctx context.Context, caller ContractCaller, gateway, verifier common.Address, orderedSigners []common.Address, signatureChecks, hashRounds uint32) error {
	if caller == nil || gateway == (common.Address{}) || verifier == (common.Address{}) || len(orderedSigners) == 0 || signatureChecks == 0 || int(signatureChecks) > len(orderedSigners) {
		return errors.New("invalid direct verifier deployment expectation")
	}
	call := func(to common.Address, data []byte) ([]byte, error) {
		return caller.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	}
	result, err := call(gateway, chainabi.EncodeDirectVerifierCall())
	if err != nil {
		return err
	}
	address, err := chainabi.DecodeAddressResult(result)
	if err != nil || address != verifier {
		return errors.New("Gateway.directVerifier does not match manifest")
	}
	result, err = call(verifier, chainabi.EncodeVerifierGatewayCall())
	if err != nil {
		return err
	}
	address, err = chainabi.DecodeAddressResult(result)
	if err != nil || address != gateway {
		return errors.New("DirectVerifier.gateway does not match manifest")
	}
	result, err = call(verifier, chainabi.EncodeAuthorizedSignerCountCall())
	if err != nil {
		return err
	}
	count, err := chainabi.DecodeUint256Result(result)
	if err != nil || !count.IsUint64() || count.Uint64() != uint64(len(orderedSigners)) {
		return errors.New("DirectVerifier signer count does not match profile")
	}
	for index, want := range orderedSigners {
		result, err = call(verifier, chainabi.EncodeAuthorizedSignerCall(big.NewInt(int64(index))))
		if err != nil {
			return err
		}
		got, decodeErr := chainabi.DecodeAddressResult(result)
		if decodeErr != nil || got != want {
			return fmt.Errorf("DirectVerifier authorizedSigners[%d] does not match profile", index)
		}
	}
	for name, data := range map[string]struct {
		call  []byte
		value uint32
	}{"signatureChecks": {chainabi.EncodeSignatureChecksCall(), signatureChecks}, "hashRounds": {chainabi.EncodeHashRoundsCall(), hashRounds}} {
		result, err = call(verifier, data.call)
		if err != nil {
			return err
		}
		got, decodeErr := chainabi.DecodeUint32Result(result)
		if decodeErr != nil || got != data.value {
			return fmt.Errorf("DirectVerifier %s does not match profile", name)
		}
	}
	return nil
}
