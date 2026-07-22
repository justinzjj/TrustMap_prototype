package executor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
)

type verifierCallerFake struct{ results map[string][]byte }

func (caller verifierCallerFake) CallContract(_ context.Context, message ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return caller.results[message.To.Hex()+":"+common.Bytes2Hex(message.Data)], nil
}
func wordAddress(address common.Address) []byte {
	word := make([]byte, 32)
	copy(word[12:], address[:])
	return word
}
func wordUint(value uint32) []byte {
	word := make([]byte, 32)
	new(big.Int).SetUint64(uint64(value)).FillBytes(word)
	return word
}

func TestValidateVerifierBindingChecksOrderedDeploymentFixedProfile(t *testing.T) {
	gateway := common.HexToAddress("0x1111111111111111111111111111111111111111")
	verifier := common.HexToAddress("0x2222222222222222222222222222222222222222")
	signers := []common.Address{common.HexToAddress("0x31"), common.HexToAddress("0x32"), common.HexToAddress("0x33")}
	results := map[string][]byte{}
	put := func(to common.Address, call, result []byte) { results[to.Hex()+":"+common.Bytes2Hex(call)] = result }
	put(gateway, chainabi.EncodeDirectVerifierCall(), wordAddress(verifier))
	put(verifier, chainabi.EncodeVerifierGatewayCall(), wordAddress(gateway))
	put(verifier, chainabi.EncodeAuthorizedSignerCountCall(), wordUint(3))
	put(verifier, chainabi.EncodeSignatureChecksCall(), wordUint(2))
	put(verifier, chainabi.EncodeHashRoundsCall(), wordUint(4497))
	for index, signer := range signers {
		put(verifier, chainabi.EncodeAuthorizedSignerCall(big.NewInt(int64(index))), wordAddress(signer))
	}
	if err := ValidateVerifierBinding(context.Background(), verifierCallerFake{results}, gateway, verifier, signers, 2, 4497); err != nil {
		t.Fatal(err)
	}
	signers[0], signers[1] = signers[1], signers[0]
	if err := ValidateVerifierBinding(context.Background(), verifierCallerFake{results}, gateway, verifier, signers, 2, 4497); err == nil {
		t.Fatal("ordered signer mismatch accepted")
	}
}
