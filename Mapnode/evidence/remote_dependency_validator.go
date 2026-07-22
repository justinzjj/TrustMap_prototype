package evidence

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

var (
	ErrRemoteDependencyInvalid     = errors.New("invalid remote dependency evidence")
	ErrRemoteDependencyRetryable   = errors.New("retryable remote dependency validation failure")
	ErrInvalidTrustRootObservation = errors.New("invalid hash-bound TrustRoot observation")
)

type RemoteDependencyConfig struct {
	ChainID         domain.ChainID
	Gateway         common.Address
	GatewayCodeHash common.Hash
	DeploymentBlock uint64
	Confirmations   uint64
	MerkleDepth     uint8
	PathStepCostGas uint64
}

type RemoteDependencyRPC interface {
	ChainID(context.Context) (*big.Int, error)
	BlockNumber(context.Context) (uint64, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	CodeAtHash(context.Context, common.Address, common.Hash) ([]byte, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error)
}

type ValidatedTrustRootObservation struct {
	ChainID    domain.ChainID
	Height     domain.BlockHeight
	BlockHash  common.Hash
	TrustRoot  common.Hash
	EvidenceID ID
}

type RemoteTrustRootObserver interface {
	ObserveExpectedTrustRoot(context.Context, domain.ChainID, domain.BlockHeight, common.Hash, common.Hash) (ValidatedTrustRootObservation, error)
}

type RemoteDependencyValidation struct {
	Record            Record
	Dependency        chainabi.DependencyRecorded
	HomeTrustRoot     common.Hash
	WitnessSiblings   []common.Hash
	SourceObservation ValidatedTrustRootObservation
	HomeObservation   ValidatedTrustRootObservation
	PathStepCostGas   uint64
}

type RemoteDependencyValidator struct {
	config       RemoteDependencyConfig
	rpc          RemoteDependencyRPC
	observations RemoteTrustRootObserver
}

func NewRemoteDependencyValidator(config RemoteDependencyConfig, rpc RemoteDependencyRPC, observations RemoteTrustRootObserver) (*RemoteDependencyValidator, error) {
	if config.ChainID.Validate() != nil || config.Gateway == (common.Address{}) || config.GatewayCodeHash == (common.Hash{}) || config.DeploymentBlock == 0 || config.Confirmations == 0 || config.MerkleDepth == 0 || config.MerkleDepth > internalproof.MaxTreeDepth || config.PathStepCostGas == 0 || config.PathStepCostGas > math.MaxInt64 || rpc == nil || observations == nil {
		return nil, errors.New("remote dependency validator requires trusted chain deployment, RPC, observations, depth, and cost")
	}
	return &RemoteDependencyValidator{config: config, rpc: rpc, observations: observations}, nil
}

func (validator *RemoteDependencyValidator) Validate(ctx context.Context, locator Locator) (RemoteDependencyValidation, error) {
	if validator == nil || validator.rpc == nil {
		return RemoteDependencyValidation{}, invalidRemote("validator is not initialized")
	}
	if err := locator.Validate(); err != nil {
		return RemoteDependencyValidation{}, invalidRemote(err.Error())
	}
	if locator.ChainID != validator.config.ChainID || locator.ContractAddress != validator.config.Gateway {
		return RemoteDependencyValidation{}, invalidRemote("locator does not match configured recording chain and Gateway")
	}
	chainID, err := validator.rpc.ChainID(ctx)
	if err != nil {
		return RemoteDependencyValidation{}, retryRemote("read chain ID", err)
	}
	if chainID == nil || chainID.Cmp(validator.config.ChainID.BigInt()) != 0 {
		return RemoteDependencyValidation{}, invalidRemote("configured chain ID mismatch")
	}
	if !locator.BlockNumber.BigInt().IsUint64() {
		return RemoteDependencyValidation{}, invalidRemote("recording block exceeds HTTP RPC range")
	}
	number := locator.BlockNumber.BigInt().Uint64()
	if number < validator.config.DeploymentBlock {
		return RemoteDependencyValidation{}, invalidRemote("recording block predates Gateway deployment")
	}
	head, err := validator.rpc.BlockNumber(ctx)
	if err != nil {
		return RemoteDependencyValidation{}, retryRemote("read confirmed head", err)
	}
	if head < number || head-number < validator.config.Confirmations {
		return RemoteDependencyValidation{}, retryRemote("wait for configured confirmation depth", errors.New("remote head is not deep enough"))
	}
	header, err := validator.rpc.HeaderByNumber(ctx, new(big.Int).SetUint64(number))
	if err != nil {
		return RemoteDependencyValidation{}, retryRemote("read canonical recording header", err)
	}
	if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != number || header.Hash() != locator.BlockHash {
		return RemoteDependencyValidation{}, invalidRemote("canonical recording block number/hash mismatch")
	}
	code, err := validator.rpc.CodeAtHash(ctx, validator.config.Gateway, locator.BlockHash)
	if err != nil {
		return RemoteDependencyValidation{}, retryRemote("read hash-bound Gateway code", err)
	}
	if len(code) == 0 || crypto.Keccak256Hash(code) != validator.config.GatewayCodeHash {
		return RemoteDependencyValidation{}, invalidRemote("Gateway code hash mismatch")
	}
	receipt, err := validator.rpc.TransactionReceipt(ctx, locator.TxHash)
	if err != nil {
		return RemoteDependencyValidation{}, retryRemote("read verification receipt", err)
	}
	if receipt == nil {
		return RemoteDependencyValidation{}, retryRemote("read verification receipt", errors.New("receipt unavailable"))
	}
	bundle, err := validator.validateReceipt(locator, header, receipt)
	if err != nil {
		return RemoteDependencyValidation{}, err
	}
	request, err := validator.loadRequest(ctx, bundle.dependency.RequestID, number)
	if err != nil {
		return RemoteDependencyValidation{}, err
	}
	if err := bindRequest(request, bundle); err != nil {
		return RemoteDependencyValidation{}, err
	}
	sourceObservation, err := validator.observations.ObserveExpectedTrustRoot(ctx, bundle.dependency.SourceChainID, bundle.dependency.SourceHeight, bundle.dependency.SourceBlockHash, bundle.dependency.SourceTrustRoot)
	if err != nil {
		if errors.Is(err, ErrInvalidTrustRootObservation) {
			return RemoteDependencyValidation{}, invalidRemote(err.Error())
		}
		return RemoteDependencyValidation{}, retryRemote("observe source TrustRoot", err)
	}
	homeObservation, err := validator.observations.ObserveExpectedTrustRoot(ctx, validator.config.ChainID, locator.BlockNumber, locator.BlockHash, bundle.resolution.HomeTrustRoot)
	if err != nil {
		if errors.Is(err, ErrInvalidTrustRootObservation) {
			return RemoteDependencyValidation{}, invalidRemote(err.Error())
		}
		return RemoteDependencyValidation{}, retryRemote("observe recording TrustRoot", err)
	}
	if !observationMatches(sourceObservation, bundle.dependency.SourceChainID, bundle.dependency.SourceHeight, bundle.dependency.SourceBlockHash, bundle.dependency.SourceTrustRoot) || !observationMatches(homeObservation, validator.config.ChainID, locator.BlockNumber, locator.BlockHash, bundle.resolution.HomeTrustRoot) {
		return RemoteDependencyValidation{}, invalidRemote("hash-bound TrustRoot observation mismatch")
	}
	rechecked, err := validator.rpc.HeaderByNumber(ctx, new(big.Int).SetUint64(number))
	if err != nil {
		return RemoteDependencyValidation{}, retryRemote("recheck canonical recording header", err)
	}
	if rechecked == nil || rechecked.Hash() != locator.BlockHash {
		return RemoteDependencyValidation{}, invalidRemote("recording block ceased to be canonical")
	}
	id, err := ComputeID(locator)
	if err != nil {
		return RemoteDependencyValidation{}, invalidRemote(err.Error())
	}
	return RemoteDependencyValidation{Record: Record{ID: id, Locator: locator, State: Candidate}, Dependency: bundle.dependency, HomeTrustRoot: bundle.resolution.HomeTrustRoot, WitnessSiblings: append([]common.Hash(nil), bundle.siblings...), SourceObservation: sourceObservation, HomeObservation: homeObservation, PathStepCostGas: validator.config.PathStepCostGas}, nil
}

type validatedReceiptBundle struct {
	success    chainabi.VerificationSucceeded
	dependency chainabi.DependencyRecorded
	resolution chainabi.RequestResolved
	siblings   []common.Hash
}

func (validator *RemoteDependencyValidator) validateReceipt(locator Locator, header *types.Header, receipt *types.Receipt) (validatedReceiptBundle, error) {
	number := locator.BlockNumber.BigInt().Uint64()
	if receipt.Status != types.ReceiptStatusSuccessful || receipt.TxHash != locator.TxHash || receipt.BlockHash != locator.BlockHash || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.BlockNumber.Uint64() != number || uint64(receipt.TransactionIndex) > math.MaxUint32 || uint32(receipt.TransactionIndex) != locator.TxIndex {
		return validatedReceiptBundle{}, invalidRemote("failed receipt or mismatched receipt block/transaction coordinate")
	}
	var bundle validatedReceiptBundle
	var successSet, dependencySet, resolutionSet bool
	var successIndex, dependencyIndex, resolutionIndex uint
	type updateItem struct {
		event chainabi.TrustRootUpdated
		index uint
	}
	var updates []updateItem
	previous := uint(0)
	for position, item := range receipt.Logs {
		if item == nil {
			return validatedReceiptBundle{}, invalidRemote("nil receipt log")
		}
		if item.Removed || item.BlockHash != locator.BlockHash || item.BlockNumber != number || item.TxHash != locator.TxHash || item.TxIndex != receipt.TransactionIndex || (position > 0 && item.Index <= previous) {
			return validatedReceiptBundle{}, invalidRemote("receipt log coordinate/order mismatch")
		}
		previous = item.Index
		if item.Address != validator.config.Gateway {
			continue
		}
		if len(item.Topics) == 0 {
			return validatedReceiptBundle{}, invalidRemote("Gateway receipt log lacks topic")
		}
		switch item.Topics[0] {
		case chainabi.DirectVerificationSucceededTopic, chainabi.PathVerificationSucceededTopic:
			if successSet {
				return validatedReceiptBundle{}, invalidRemote("multiple verification success logs")
			}
			event, err := chainabi.ParseVerificationSucceeded(*item)
			if err != nil {
				return validatedReceiptBundle{}, invalidRemote(err.Error())
			}
			bundle.success = event
			successIndex = item.Index
			successSet = true
		case chainabi.TrustRootUpdatedTopic:
			event, err := chainabi.ParseTrustRootUpdated(*item)
			if err != nil {
				return validatedReceiptBundle{}, invalidRemote(err.Error())
			}
			updates = append(updates, updateItem{event: event, index: item.Index})
		case chainabi.DependencyRecordedTopic:
			if dependencySet {
				return validatedReceiptBundle{}, invalidRemote("multiple DependencyRecorded logs")
			}
			event, err := chainabi.ParseDependencyRecorded(*item)
			if err != nil {
				return validatedReceiptBundle{}, invalidRemote(err.Error())
			}
			if uint64(item.Index) > math.MaxUint32 || uint32(item.Index) != locator.LogIndex {
				return validatedReceiptBundle{}, invalidRemote("exact DependencyRecorded log coordinate mismatch")
			}
			bundle.dependency = event
			dependencyIndex = item.Index
			dependencySet = true
		case chainabi.RequestResolvedTopic:
			if resolutionSet {
				return validatedReceiptBundle{}, invalidRemote("multiple RequestResolved logs")
			}
			event, err := chainabi.ParseRequestResolved(*item)
			if err != nil {
				return validatedReceiptBundle{}, invalidRemote(err.Error())
			}
			bundle.resolution = event
			resolutionIndex = item.Index
			resolutionSet = true
		case chainabi.VerificationRequestedTopic:
			return validatedReceiptBundle{}, invalidRemote("verification receipt contains request event")
		default:
			return validatedReceiptBundle{}, invalidRemote("unknown Gateway receipt topic")
		}
	}
	if !successSet || !dependencySet || !resolutionSet || len(updates) != 2 || !bundle.resolution.NewDependency {
		return validatedReceiptBundle{}, invalidRemote("incomplete new-dependency receipt")
	}
	anchor, dependencyUpdate := updates[0], updates[1]
	if !(successIndex < anchor.index && anchor.index < dependencyUpdate.index && dependencyUpdate.index < dependencyIndex && dependencyIndex < resolutionIndex) {
		return validatedReceiptBundle{}, invalidRemote("new dependency receipt events are misordered")
	}
	if bundle.success.RequestID != bundle.dependency.RequestID || bundle.resolution.RequestID != bundle.dependency.RequestID || anchor.event.RequestID != bundle.dependency.RequestID || dependencyUpdate.event.RequestID != bundle.dependency.RequestID || bundle.success.SourceChainID != bundle.dependency.SourceChainID || bundle.success.SourceHeight != bundle.dependency.SourceHeight || bundle.success.SourceBlockHash != bundle.dependency.SourceBlockHash || bundle.success.SourceTrustRoot != bundle.dependency.SourceTrustRoot || bundle.success.DependencyKey != bundle.dependency.DependencyKey || bundle.resolution.DependencyKey != bundle.dependency.DependencyKey {
		return validatedReceiptBundle{}, invalidRemote("request/dependency/source tuple mismatch")
	}
	if bundle.dependency.DependencyKey != canonicalDependencyKey(bundle.dependency.SourceChainID, bundle.dependency.SourceHeight, bundle.dependency.SourceBlockHash, bundle.dependency.SourceTrustRoot) {
		return validatedReceiptBundle{}, invalidRemote("dependency key mismatch")
	}
	if canonicalDependencyPayloadDigest(bundle.dependency) != locator.PayloadDigest {
		return validatedReceiptBundle{}, invalidRemote("dependency payload digest mismatch")
	}
	if anchor.event.LeafIndex != 0 || !anchor.event.IsAnchor || dependencyUpdate.event.LeafIndex != 1 || dependencyUpdate.event.IsAnchor || bundle.dependency.LeafIndex != 1 {
		return validatedReceiptBundle{}, invalidRemote("invalid fixed dependency update indices")
	}
	anchorLeaf := domain.LeafHash(anchor.event.OldTrustRoot, header.ParentHash)
	dependencyLeaf := domain.LeafHash(bundle.dependency.SourceTrustRoot, bundle.dependency.SourceBlockHash)
	if anchor.event.Leaf != anchorLeaf || dependencyUpdate.event.Leaf != dependencyLeaf || dependencyUpdate.event.OldTrustRoot != anchor.event.NewTrustRoot || bundle.resolution.HomeTrustRoot != dependencyUpdate.event.NewTrustRoot {
		return validatedReceiptBundle{}, invalidRemote("TrustRootUpdated chain mismatch")
	}
	tree, err := internalproof.NewIncrementalTree(validator.config.MerkleDepth)
	if err != nil {
		return validatedReceiptBundle{}, invalidRemote(err.Error())
	}
	if err := tree.Append(anchorLeaf); err != nil || tree.Root() != anchor.event.NewTrustRoot {
		return validatedReceiptBundle{}, invalidRemote("anchor TrustRoot mismatch")
	}
	if err := tree.Append(dependencyLeaf); err != nil || tree.Root() != dependencyUpdate.event.NewTrustRoot {
		return validatedReceiptBundle{}, invalidRemote("dependency TrustRoot mismatch")
	}
	zeros, _ := internalproof.ZeroHashes(validator.config.MerkleDepth)
	siblings := make([]common.Hash, validator.config.MerkleDepth)
	siblings[0] = anchorLeaf
	for level := 1; level < int(validator.config.MerkleDepth); level++ {
		siblings[level] = zeros[level]
	}
	root, err := internalproof.RootFromWitness(dependencyLeaf, bundle.dependency.LeafIndex, siblings, validator.config.MerkleDepth)
	if err != nil || root != bundle.resolution.HomeTrustRoot {
		return validatedReceiptBundle{}, invalidRemote("fixed-depth membership witness does not close")
	}
	bundle.siblings = siblings
	return bundle, nil
}

func (validator *RemoteDependencyValidator) loadRequest(ctx context.Context, requestID domain.RequestID, to uint64) (chainabi.VerificationRequested, error) {
	logs, err := validator.rpc.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(validator.config.DeploymentBlock), ToBlock: new(big.Int).SetUint64(to), Addresses: []common.Address{validator.config.Gateway}, Topics: [][]common.Hash{{chainabi.VerificationRequestedTopic}, {common.Hash(requestID)}}})
	if err != nil {
		return chainabi.VerificationRequested{}, retryRemote("load VerificationRequested", err)
	}
	if len(logs) != 1 {
		return chainabi.VerificationRequested{}, invalidRemote("request ID does not resolve to exactly one VerificationRequested log")
	}
	item := logs[0]
	if item.Removed || item.Address != validator.config.Gateway || item.BlockNumber > to || item.BlockNumber < validator.config.DeploymentBlock || item.BlockHash == (common.Hash{}) {
		return chainabi.VerificationRequested{}, invalidRemote("VerificationRequested log coordinate mismatch")
	}
	header, err := validator.rpc.HeaderByNumber(ctx, new(big.Int).SetUint64(item.BlockNumber))
	if err != nil {
		return chainabi.VerificationRequested{}, retryRemote("verify request block", err)
	}
	if header == nil || header.Hash() != item.BlockHash {
		return chainabi.VerificationRequested{}, invalidRemote("VerificationRequested block is not canonical")
	}
	request, err := chainabi.ParseVerificationRequested(item)
	if err != nil {
		return chainabi.VerificationRequested{}, invalidRemote(err.Error())
	}
	want, err := ComputeGatewayRequestID(validator.config.ChainID, validator.config.Gateway, request.Requester, new(big.Int).SetBytes(request.RequesterNonce[:]), request.SourceChainID, request.SourceHeight, request.SourceBlockHash)
	if err != nil || want != request.RequestID || want != requestID {
		return chainabi.VerificationRequested{}, invalidRemote("Gateway RequestID mismatch")
	}
	return request, nil
}

func bindRequest(request chainabi.VerificationRequested, bundle validatedReceiptBundle) error {
	if request.RequestID != bundle.dependency.RequestID || request.Requester != bundle.resolution.Requester || request.SourceChainID != bundle.dependency.SourceChainID || request.SourceHeight != bundle.dependency.SourceHeight || request.SourceBlockHash != bundle.dependency.SourceBlockHash {
		return invalidRemote("VerificationRequested tuple does not bind receipt")
	}
	return nil
}
func observationMatches(observation ValidatedTrustRootObservation, chainID domain.ChainID, height domain.BlockHeight, hash, root common.Hash) bool {
	return observation.ChainID == chainID && observation.Height == height && observation.BlockHash == hash && observation.TrustRoot == root && observation.EvidenceID != (ID{})
}
func canonicalDependencyKey(chainID domain.ChainID, height domain.BlockHeight, blockHash, root common.Hash) common.Hash {
	encoded := [4 * 32]byte{}
	copy(encoded[0:32], chainID[:])
	copy(encoded[32:64], height[:])
	copy(encoded[64:96], blockHash[:])
	copy(encoded[96:128], root[:])
	return crypto.Keccak256Hash(encoded[:])
}
func canonicalDependencyPayloadDigest(dependency chainabi.DependencyRecorded) common.Hash {
	encoded := [7 * 32]byte{}
	copy(encoded[0:32], dependency.DependencyKey[:])
	copy(encoded[32:64], dependency.RequestID[:])
	copy(encoded[64:96], dependency.SourceChainID[:])
	copy(encoded[96:128], dependency.SourceHeight[:])
	copy(encoded[128:160], dependency.SourceBlockHash[:])
	copy(encoded[160:192], dependency.SourceTrustRoot[:])
	binary.BigEndian.PutUint32(encoded[220:224], dependency.LeafIndex)
	return crypto.Keccak256Hash(encoded[:])
}
func invalidRemote(reason string) error {
	return fmt.Errorf("%w: %s", ErrRemoteDependencyInvalid, reason)
}
func retryRemote(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrRemoteDependencyRetryable, operation, err)
}
