package indexer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

const DefaultPathStepCostGas uint64 = 30_713

var (
	ErrDeterministicIndexing    = errors.New("deterministic confirmed event indexing failure")
	ErrIndexerDegraded          = errors.New("confirmed event indexer degraded")
	ErrDeterministicStore       = errors.New("deterministic confirmed block store failure")
	ErrDeterministicObservation = errors.New("deterministic hash-bound TrustRoot observation failure")
	ErrMalformedRPCContent      = errors.New("malformed canonical RPC content")
)

type confirmedEventRPC interface {
	BlockNumber(context.Context) (uint64, error)
	ChainID(context.Context) (*big.Int, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	CodeAtHash(context.Context, common.Address, common.Hash) ([]byte, error)
	FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
}

type OperationalStore interface {
	LoadIndexerCursor(context.Context, domain.ChainID) (reorg.CanonicalCursor, bool, error)
	InitializeIndexerCursor(context.Context, reorg.CanonicalCursor) error
	LoadIndexerRequest(context.Context, domain.RequestID) (coordinator.Request, bool, error)
	ObserveExpectedTrustRoot(context.Context, domain.ChainID, domain.BlockHeight, common.Hash, common.Hash) (trustview.TrustNode, error)
	ApplyIndexerBlock(context.Context, ConfirmedBlock) (bool, error)
	DegradeIndexerCursor(context.Context, domain.ChainID, string) error
}

type ConfirmedEventIndexerConfig struct {
	ChainID          domain.ChainID
	Gateway          common.Address
	GatewayCodeHash  common.Hash
	DeploymentBlock  domain.BlockHeight
	Confirmations    uint64
	MaxBlockRange    uint64
	MerkleDepth      uint8
	PathStepCostGas  uint64
	PollInterval     time.Duration
	TransientBackoff time.Duration
}

type ConfirmedEventIndexer struct {
	config       ConfirmedEventIndexerConfig
	rpc          confirmedEventRPC
	operations   OperationalStore
	observations OperationalStore
}

type StepResult struct {
	Head          uint64
	SafeHead      uint64
	BlocksApplied uint64
	LogsProcessed uint64
}

type IndexedGatewayLog struct {
	Address       common.Address
	BlockNumber   uint64
	BlockHash     common.Hash
	TxHash        common.Hash
	TxIndex       uint32
	LogIndex      uint32
	EventTopic    common.Hash
	ContentDigest common.Hash
	Topics        []common.Hash
	Data          []byte
}

type VerificationReceiptRecord struct {
	TxHash      common.Hash
	BlockNumber uint64
	BlockHash   common.Hash
	TxIndex     uint32
	RequestID   domain.RequestID
}

type RequestResolutionRecord struct {
	RequestID     domain.RequestID
	TxHash        common.Hash
	DependencyKey common.Hash
	NewDependency bool
	HomeTrustRoot common.Hash
	LogIndex      uint32
}

type DependencyMaterialization struct {
	Dependency trustview.VerifiedDependency
	Evidence   evidence.Record
	Witness    trustview.MembershipWitness
	From       trustview.TrustNode
	To         trustview.TrustNode
	Edge       trustview.TrustEdge
}

type ConfirmedBlock struct {
	ChainID         domain.ChainID
	Gateway         common.Address
	Number          uint64
	Hash            common.Hash
	ParentHash      common.Hash
	ExpectedCursor  reorg.CanonicalCursor
	Logs            []IndexedGatewayLog
	Requests        []coordinator.Request
	Receipts        []VerificationReceiptRecord
	Resolutions     []RequestResolutionRecord
	Dependencies    []DependencyMaterialization
	MerkleDepth     uint8
	PathStepCostGas uint64
}

func NewConfirmedEventIndexer(config ConfirmedEventIndexerConfig, rpc confirmedEventRPC, operations OperationalStore, observations OperationalStore) (*ConfirmedEventIndexer, error) {
	if err := config.ChainID.Validate(); err != nil {
		return nil, err
	}
	if rpc == nil || operations == nil || config.Gateway == (common.Address{}) || config.GatewayCodeHash == (common.Hash{}) || config.Confirmations == 0 || config.MaxBlockRange == 0 || config.MerkleDepth == 0 || config.MerkleDepth > 32 || config.PathStepCostGas == 0 {
		return nil, errors.New("confirmed event indexer requires trusted deployment and RPC configuration")
	}
	return &ConfirmedEventIndexer{config: config, rpc: rpc, operations: operations, observations: observations}, nil
}

func (indexer *ConfirmedEventIndexer) Step(ctx context.Context) (StepResult, error) {
	head, err := indexer.rpc.BlockNumber(ctx)
	if err != nil {
		return StepResult{}, fmt.Errorf("read home head: %w", err)
	}
	result := StepResult{Head: head}
	if head < indexer.config.Confirmations {
		return result, nil
	}
	result.SafeHead = head - indexer.config.Confirmations
	deployment := indexer.config.DeploymentBlock.BigInt()
	if !deployment.IsUint64() || deployment.Uint64() > result.SafeHead {
		return result, nil
	}
	cursor, exists, err := indexer.operations.LoadIndexerCursor(ctx, indexer.config.ChainID)
	if err != nil {
		return result, err
	}
	if !exists {
		header, err := indexer.header(ctx, deployment.Uint64())
		if err != nil {
			return result, indexer.classifyRPCError(ctx, err)
		}
		if err := indexer.validateDeployment(ctx, header.Hash()); err != nil {
			var transient transientPreparationError
			if errors.As(err, &transient) {
				return result, transient.err
			}
			return result, indexer.failClosed(ctx, err)
		}
		cursor = reorg.NewCanonicalCursor(indexer.config.ChainID, indexer.config.DeploymentBlock, header.Hash())
		if err := indexer.operations.InitializeIndexerCursor(ctx, cursor); err != nil {
			return result, err
		}
	}
	if cursor.State == reorg.Degraded {
		return result, ErrIndexerDegraded
	}
	if !cursor.Height.BigInt().IsUint64() {
		return result, indexer.failClosed(ctx, errors.New("cursor height exceeds HTTP indexer range"))
	}
	start := cursor.Height.BigInt().Uint64() + 1
	if start == 0 || start > result.SafeHead {
		return result, nil
	}
	for rangeStart := start; rangeStart <= result.SafeHead; {
		rangeEnd := rangeStart + indexer.config.MaxBlockRange - 1
		if rangeEnd < rangeStart || rangeEnd > result.SafeHead {
			rangeEnd = result.SafeHead
		}
		logs, err := indexer.rpc.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(rangeStart), ToBlock: new(big.Int).SetUint64(rangeEnd), Addresses: []common.Address{indexer.config.Gateway}, Topics: [][]common.Hash{{chainabi.VerificationRequestedTopic, chainabi.TrustRootUpdatedTopic, chainabi.DependencyRecordedTopic, chainabi.RequestResolvedTopic}}})
		if err != nil {
			return result, fmt.Errorf("eth_getLogs %d-%d: %w", rangeStart, rangeEnd, err)
		}
		if err := validateAndSortLogs(logs, indexer.config.Gateway, rangeStart, rangeEnd); err != nil {
			return result, indexer.failClosed(ctx, err)
		}
		byBlock := make(map[uint64][]types.Log)
		for _, item := range logs {
			byBlock[item.BlockNumber] = append(byBlock[item.BlockNumber], item)
		}
		for number := rangeStart; number <= rangeEnd; number++ {
			rechecked, err := indexer.header(ctx, cursor.Height.BigInt().Uint64())
			if err != nil {
				return result, indexer.classifyRPCError(ctx, err)
			}
			if rechecked.Hash() != cursor.Hash {
				return result, indexer.failClosed(ctx, errors.New("persisted cursor hash is no longer canonical"))
			}
			header, err := indexer.header(ctx, number)
			if err != nil {
				return result, indexer.classifyRPCError(ctx, err)
			}
			if header.ParentHash != cursor.Hash {
				return result, indexer.failClosed(ctx, errors.New("next confirmed block parent does not match cursor"))
			}
			block, err := indexer.prepareBlock(ctx, cursor, header, byBlock[number])
			if err != nil {
				var transient transientPreparationError
				if errors.As(err, &transient) {
					return result, transient.err
				}
				return result, indexer.failClosed(ctx, err)
			}
			changed, err := indexer.operations.ApplyIndexerBlock(ctx, block)
			if err != nil {
				if errors.Is(err, ErrDeterministicStore) {
					return result, indexer.failClosed(ctx, err)
				}
				return result, err
			}
			if changed {
				result.BlocksApplied++
			}
			result.LogsProcessed += uint64(len(block.Logs))
			height, _ := domain.NewBlockHeight(number)
			cursor = reorg.NewCanonicalCursor(indexer.config.ChainID, height, header.Hash())
		}
		if rangeEnd == math.MaxUint64 {
			break
		}
		rangeStart = rangeEnd + 1
	}
	return result, nil
}

func DeterministicStoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrDeterministicStore, err)
}

func DeterministicObservationError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrDeterministicObservation, err)
}

func (indexer *ConfirmedEventIndexer) Run(ctx context.Context) error {
	poll := indexer.config.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	backoff := indexer.config.TransientBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		_, err := indexer.Step(ctx)
		if errors.Is(err, ErrIndexerDegraded) || errors.Is(err, ErrDeterministicIndexing) {
			return err
		}
		wait := poll
		if err != nil {
			wait = backoff
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (indexer *ConfirmedEventIndexer) validateDeployment(ctx context.Context, hash common.Hash) error {
	chainID, err := indexer.rpc.ChainID(ctx)
	if err != nil {
		return transientPreparationError{fmt.Errorf("read home chain ID: %w", err)}
	}
	if chainID == nil || chainID.Cmp(indexer.config.ChainID.BigInt()) != 0 {
		return errors.New("home RPC chain ID mismatch")
	}
	code, err := indexer.rpc.CodeAtHash(ctx, indexer.config.Gateway, hash)
	if err != nil {
		return transientPreparationError{fmt.Errorf("read hash-bound Gateway code: %w", err)}
	}
	if len(code) == 0 || crypto.Keccak256Hash(code) != indexer.config.GatewayCodeHash {
		return errors.New("hash-bound Gateway code hash mismatch")
	}
	return nil
}

func (indexer *ConfirmedEventIndexer) header(ctx context.Context, number uint64) (*types.Header, error) {
	header, err := indexer.rpc.HeaderByNumber(ctx, new(big.Int).SetUint64(number))
	if err != nil {
		return nil, transientPreparationError{fmt.Errorf("read canonical header %d: %w", number, err)}
	}
	if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != number {
		return nil, fmt.Errorf("%w: canonical header %d has invalid number", ErrMalformedRPCContent, number)
	}
	return header, nil
}

func (indexer *ConfirmedEventIndexer) classifyRPCError(ctx context.Context, err error) error {
	var transient transientPreparationError
	if errors.As(err, &transient) {
		return transient.err
	}
	if errors.Is(err, ErrMalformedRPCContent) {
		return indexer.failClosed(ctx, err)
	}
	return err
}

func validateAndSortLogs(logs []types.Log, gateway common.Address, from, to uint64) error {
	for _, item := range logs {
		if item.Removed || item.Address != gateway || item.BlockNumber < from || item.BlockNumber > to || item.BlockHash == (common.Hash{}) || item.TxHash == (common.Hash{}) || uint64(item.TxIndex) > math.MaxUint32 || uint64(item.Index) > math.MaxUint32 || len(item.Topics) == 0 || !supportedTopic(item.Topics[0]) {
			return errors.New("eth_getLogs returned wrong address, topic, or coordinate")
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].BlockNumber != logs[j].BlockNumber {
			return logs[i].BlockNumber < logs[j].BlockNumber
		}
		if logs[i].TxIndex != logs[j].TxIndex {
			return logs[i].TxIndex < logs[j].TxIndex
		}
		return logs[i].Index < logs[j].Index
	})
	for i := 1; i < len(logs); i++ {
		if logs[i-1].BlockNumber == logs[i].BlockNumber && logs[i-1].TxIndex == logs[i].TxIndex && logs[i-1].Index == logs[i].Index {
			return errors.New("duplicate Gateway log coordinate")
		}
	}
	return nil
}

func supportedTopic(topic common.Hash) bool {
	return topic == chainabi.VerificationRequestedTopic || topic == chainabi.TrustRootUpdatedTopic || topic == chainabi.DependencyRecordedTopic || topic == chainabi.RequestResolvedTopic
}

func (indexer *ConfirmedEventIndexer) failClosed(ctx context.Context, cause error) error {
	reason := fmt.Sprintf("%v: %v", ErrDeterministicIndexing, cause)
	if err := indexer.operations.DegradeIndexerCursor(ctx, indexer.config.ChainID, reason); err != nil {
		return errors.Join(fmt.Errorf("%w: %v", ErrDeterministicIndexing, cause), fmt.Errorf("persist degraded cursor: %w", err))
	}
	return fmt.Errorf("%w: %v", ErrDeterministicIndexing, cause)
}

func rawLog(item types.Log) IndexedGatewayLog {
	content := make([]byte, 0, len(item.Topics)*32+len(item.Data))
	for _, topic := range item.Topics {
		content = append(content, topic[:]...)
	}
	content = append(content, item.Data...)
	return IndexedGatewayLog{Address: item.Address, BlockNumber: item.BlockNumber, BlockHash: item.BlockHash, TxHash: item.TxHash, TxIndex: uint32(item.TxIndex), LogIndex: uint32(item.Index), EventTopic: item.Topics[0], ContentDigest: crypto.Keccak256Hash(content), Topics: append([]common.Hash(nil), item.Topics...), Data: append([]byte(nil), item.Data...)}
}

func (indexer *ConfirmedEventIndexer) prepareBlock(ctx context.Context, cursor reorg.CanonicalCursor, header *types.Header, logs []types.Log) (ConfirmedBlock, error) {
	number := header.Number.Uint64()
	block := ConfirmedBlock{ChainID: indexer.config.ChainID, Gateway: indexer.config.Gateway, Number: number, Hash: header.Hash(), ParentHash: header.ParentHash, ExpectedCursor: cursor, MerkleDepth: indexer.config.MerkleDepth, PathStepCostGas: indexer.config.PathStepCostGas}
	requests := make(map[domain.RequestID]coordinator.Request)
	verificationTxs := make(map[common.Hash]types.Log)
	for _, item := range logs {
		if item.BlockHash != block.Hash {
			return ConfirmedBlock{}, errors.New("Gateway log block hash does not match canonical header")
		}
		block.Logs = append(block.Logs, rawLog(item))
		switch item.Topics[0] {
		case chainabi.VerificationRequestedTopic:
			request, err := VerificationRequested(item, indexer.config.ChainID, indexer.config.Gateway, time.Now().UTC())
			if err != nil {
				return ConfirmedBlock{}, err
			}
			if previous, ok := requests[request.ID]; ok && previous != request {
				return ConfirmedBlock{}, errors.New("conflicting VerificationRequested events")
			}
			requests[request.ID] = request
			block.Requests = append(block.Requests, request)
		case chainabi.RequestResolvedTopic:
			verificationTxs[item.TxHash] = item
		}
	}
	if len(verificationTxs) > 1 {
		return ConfirmedBlock{}, errors.New("multiple verification transactions in one experiment block are unsupported")
	}
	consumed := make(map[logCoordinate]struct{})
	for txHash, resolvedLog := range verificationTxs {
		resolved, err := chainabi.ParseRequestResolved(resolvedLog)
		if err != nil {
			return ConfirmedBlock{}, err
		}
		request, ok := requests[resolved.RequestID]
		if !ok {
			request, ok, err = indexer.operations.LoadIndexerRequest(ctx, resolved.RequestID)
			if err != nil {
				return ConfirmedBlock{}, transientPreparationError{fmt.Errorf("load resolved request: %w", err)}
			}
			if !ok {
				return ConfirmedBlock{}, errors.New("RequestResolved references an unobserved request")
			}
		}
		receipt, err := indexer.rpc.TransactionReceipt(ctx, txHash)
		if err != nil {
			return ConfirmedBlock{}, transientPreparationError{fmt.Errorf("read verification receipt: %w", err)}
		}
		if receipt == nil {
			return ConfirmedBlock{}, transientPreparationError{errors.New("confirmed verification receipt is temporarily unavailable")}
		}
		bundle, err := VerifyVerificationReceipt(VerificationReceiptInput{Gateway: indexer.config.Gateway, BlockHash: block.Hash, BlockNumber: number, ParentHash: header.ParentHash, MerkleDepth: indexer.config.MerkleDepth, Request: request, Receipt: receipt})
		if err != nil {
			return ConfirmedBlock{}, err
		}
		for _, receiptLog := range bundle.Logs {
			if supportedTopic(receiptLog.Topics[0]) {
				coordinate := logCoordinate{TxHash: receiptLog.TxHash, TxIndex: receiptLog.TxIndex, LogIndex: receiptLog.Index}
				if !containsExactLog(logs, receiptLog) {
					return ConfirmedBlock{}, errors.New("receipt Gateway event is absent from eth_getLogs result")
				}
				consumed[coordinate] = struct{}{}
			}
		}
		block.Receipts = append(block.Receipts, VerificationReceiptRecord{TxHash: receipt.TxHash, BlockNumber: number, BlockHash: block.Hash, TxIndex: uint32(receipt.TransactionIndex), RequestID: request.ID})
		block.Resolutions = append(block.Resolutions, RequestResolutionRecord{RequestID: request.ID, TxHash: receipt.TxHash, DependencyKey: bundle.Resolution.DependencyKey, NewDependency: bundle.Resolution.NewDependency, HomeTrustRoot: bundle.Resolution.HomeTrustRoot, LogIndex: uint32(resolvedLog.Index)})
		if bundle.Dependency == nil {
			continue
		}
		if indexer.observations == nil {
			return ConfirmedBlock{}, errors.New("dependency materialization requires hash-bound TrustRoot observations")
		}
		dependency := trustview.NewVerifiedDependency(bundle.Dependency.RequestID, bundle.Dependency.SourceChainID, bundle.Dependency.SourceHeight, bundle.Dependency.SourceBlockHash, trustview.TrustRoot{Hash: bundle.Dependency.SourceTrustRoot}, bundle.Dependency.LeafIndex, evidence.ID{})
		var dependencyLog types.Log
		found := false
		for _, receiptLog := range bundle.Logs {
			if receiptLog.Topics[0] == chainabi.DependencyRecordedTopic {
				dependencyLog, found = receiptLog, true
				break
			}
		}
		if !found {
			return ConfirmedBlock{}, errors.New("verified bundle lost DependencyRecorded coordinate")
		}
		payloadDigest := trustview.ComputeDependencyRecordedPayloadDigest(dependency)
		blockHeight, _ := domain.NewBlockHeight(number)
		locator := evidence.Locator{ChainID: indexer.config.ChainID, ContractAddress: indexer.config.Gateway, BlockNumber: blockHeight, BlockHash: block.Hash, TxHash: receipt.TxHash, TxIndex: uint32(receipt.TransactionIndex), LogIndex: uint32(dependencyLog.Index), PayloadDigest: payloadDigest}
		evidenceID, err := evidence.ComputeID(locator)
		if err != nil {
			return ConfirmedBlock{}, err
		}
		dependency.EvidenceID = evidenceID
		if err := dependency.Validate(trustview.NewTrustNode(trustview.NodeKey{ChainID: dependency.SourceChainID, Height: dependency.SourceHeight, BlockHash: dependency.SourceBlockHash}, dependency.SourceTrustRoot, evidence.ID{1}), payloadDigest); err != nil {
			return ConfirmedBlock{}, err
		}
		to, err := indexer.observations.ObserveExpectedTrustRoot(ctx, dependency.SourceChainID, dependency.SourceHeight, dependency.SourceBlockHash, dependency.SourceTrustRoot.Hash)
		if err != nil {
			if errors.Is(err, ErrDeterministicObservation) {
				return ConfirmedBlock{}, err
			}
			return ConfirmedBlock{}, transientPreparationError{fmt.Errorf("observe source TrustRoot: %w", err)}
		}
		from, err := indexer.observations.ObserveExpectedTrustRoot(ctx, indexer.config.ChainID, blockHeight, block.Hash, bundle.Resolution.HomeTrustRoot)
		if err != nil {
			if errors.Is(err, ErrDeterministicObservation) {
				return ConfirmedBlock{}, err
			}
			return ConfirmedBlock{}, transientPreparationError{fmt.Errorf("observe home TrustRoot: %w", err)}
		}
		if to.Key.ChainID != dependency.SourceChainID || to.Key.Height != dependency.SourceHeight || to.Key.BlockHash != dependency.SourceBlockHash || to.Root != dependency.SourceTrustRoot || from.Key.ChainID != indexer.config.ChainID || from.Key.Height != blockHeight || from.Key.BlockHash != block.Hash || from.Root.Hash != bundle.Resolution.HomeTrustRoot {
			return ConfirmedBlock{}, errors.New("TrustRoot observation content mismatch")
		}
		witness := trustview.NewMembershipWitness(evidenceID, bundle.Witness.LeafIndex, bundle.Witness.Siblings)
		edge, err := trustview.NewTrustEdge(from.ID, to.ID, evidenceID, dependency.LeafIndex, &witness.ID, indexer.config.PathStepCostGas)
		if err != nil {
			return ConfirmedBlock{}, err
		}
		now := time.Now().UTC()
		block.Dependencies = append(block.Dependencies, DependencyMaterialization{Dependency: dependency, Evidence: evidence.Record{ID: evidenceID, Locator: locator, State: evidence.Candidate, CreatedAt: now, UpdatedAt: now}, Witness: witness, From: from, To: to, Edge: edge})
	}
	for _, item := range logs {
		if item.Topics[0] == chainabi.TrustRootUpdatedTopic || item.Topics[0] == chainabi.DependencyRecordedTopic || item.Topics[0] == chainabi.RequestResolvedTopic {
			if _, ok := consumed[logCoordinate{TxHash: item.TxHash, TxIndex: item.TxIndex, LogIndex: item.Index}]; !ok {
				return ConfirmedBlock{}, errors.New("unbound verification update log")
			}
		}
	}
	return block, nil
}

type logCoordinate struct {
	TxHash            common.Hash
	TxIndex, LogIndex uint
}

func containsExactLog(logs []types.Log, want types.Log) bool {
	for _, item := range logs {
		if item.Address == want.Address && item.BlockNumber == want.BlockNumber && item.BlockHash == want.BlockHash && item.TxHash == want.TxHash && item.TxIndex == want.TxIndex && item.Index == want.Index && len(item.Topics) == len(want.Topics) && string(item.Data) == string(want.Data) {
			equal := true
			for index := range item.Topics {
				equal = equal && item.Topics[index] == want.Topics[index]
			}
			if equal {
				return true
			}
		}
	}
	return false
}

type transientPreparationError struct{ err error }

func (wrapped transientPreparationError) Error() string { return wrapped.err.Error() }
func (wrapped transientPreparationError) Unwrap() error { return wrapped.err }
