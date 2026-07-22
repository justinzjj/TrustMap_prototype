// Package executor turns durable DirectPlan and PathPlan artifacts into exact
// Gateway calldata and owns live transaction submission/recovery.
package executor

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type ContractCaller interface {
	CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)
}

type AuthorizedSigner struct {
	Address    common.Address
	PrivateKey *ecdsa.PrivateKey
}

type DirectProofInput struct {
	SourceChainID   domain.ChainID
	SourceHeight    domain.BlockHeight
	SourceBlockHash common.Hash
	SourceTrustRoot trustview.TrustRoot
}

type DirectPlanExecutor struct {
	caller          ContractCaller
	verifier        common.Address
	orderedSigners  []AuthorizedSigner
	signatureChecks uint32
}

func NewDirectPlanExecutor(caller ContractCaller, verifier common.Address, orderedSigners []AuthorizedSigner, signatureChecks uint32) (*DirectPlanExecutor, error) {
	if caller == nil || verifier == (common.Address{}) || signatureChecks == 0 || int(signatureChecks) > len(orderedSigners) {
		return nil, errors.New("direct executor requires verifier, caller, and deployment-fixed signature checks")
	}
	copySigners := make([]AuthorizedSigner, len(orderedSigners))
	for index, signer := range orderedSigners {
		if signer.PrivateKey == nil || signer.Address == (common.Address{}) || crypto.PubkeyToAddress(signer.PrivateKey.PublicKey) != signer.Address {
			return nil, errors.New("authorized signer key/address mismatch")
		}
		copySigners[index] = signer
	}
	return &DirectPlanExecutor{caller: caller, verifier: verifier, orderedSigners: copySigners, signatureChecks: signatureChecks}, nil
}

func (executor *DirectPlanExecutor) BuildProof(ctx context.Context, input DirectProofInput) ([]byte, common.Hash, error) {
	if executor == nil || input.SourceChainID.Validate() != nil || input.SourceBlockHash == (common.Hash{}) {
		return nil, common.Hash{}, errors.New("invalid direct proof input")
	}
	call := chainabi.EncodeAttestationDigestCall(input.SourceChainID.BigInt(), input.SourceHeight.BigInt(), input.SourceBlockHash, input.SourceTrustRoot.Hash)
	result, err := executor.caller.CallContract(ctx, ethereum.CallMsg{To: &executor.verifier, Data: call}, nil)
	if err != nil {
		return nil, common.Hash{}, err
	}
	digest, err := chainabi.DecodeHashResult(result)
	if err != nil {
		return nil, common.Hash{}, err
	}
	signatures := make([][]byte, executor.signatureChecks)
	for index := range signatures {
		signature, err := crypto.Sign(digest[:], executor.orderedSigners[index].PrivateKey)
		if err != nil {
			return nil, common.Hash{}, err
		}
		publicKey, err := crypto.SigToPub(digest[:], signature)
		if err != nil || crypto.PubkeyToAddress(*publicKey) != executor.orderedSigners[index].Address {
			return nil, common.Hash{}, errors.New("local direct signature recovery failed")
		}
		// Solidity ecrecover uses 27/28. attestationDigest already includes the
		// Ethereum signed-message prefix, so no TextHash/personal_sign step exists.
		signature[64] += 27
		signatures[index] = signature
	}
	proof, err := chainabi.EncodeDirectProof(input.SourceTrustRoot.Hash, signatures)
	return proof, digest, err
}
