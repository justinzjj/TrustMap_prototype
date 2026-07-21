package indexer

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chainabi"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	internalproof "github.com/justinzjj/TrustMap_prototype/internal/proof"
)

var ErrInvalidVerificationReceipt = errors.New("invalid Gateway verification receipt")

type VerificationReceiptInput struct {
	Gateway     common.Address
	BlockHash   common.Hash
	BlockNumber uint64
	ParentHash  common.Hash
	MerkleDepth uint8
	Request     coordinator.Request
	Receipt     *types.Receipt
}

type MembershipWitnessMaterial struct {
	LeafIndex uint32
	Siblings  []common.Hash
}

type VerificationReceiptBundle struct {
	Success    chainabi.VerificationSucceeded
	Resolution chainabi.RequestResolved
	Dependency *chainabi.DependencyRecorded
	Witness    *MembershipWitnessMaterial
	Logs       []types.Log
}

func VerifyVerificationReceipt(input VerificationReceiptInput) (VerificationReceiptBundle, error) {
	if input.Gateway == (common.Address{}) || input.BlockHash == (common.Hash{}) || input.ParentHash == (common.Hash{}) || input.Receipt == nil {
		return VerificationReceiptBundle{}, invalidReceipt("missing receipt context")
	}
	if input.MerkleDepth == 0 || input.MerkleDepth > internalproof.MaxTreeDepth {
		return VerificationReceiptBundle{}, invalidReceipt("invalid deployment Merkle depth")
	}
	receipt := input.Receipt
	if receipt.Status != types.ReceiptStatusSuccessful || receipt.TxHash == (common.Hash{}) || receipt.BlockHash != input.BlockHash || receipt.BlockNumber == nil || receipt.BlockNumber.Cmp(new(big.Int).SetUint64(input.BlockNumber)) != 0 || uint64(receipt.TransactionIndex) > uint64(^uint32(0)) {
		return VerificationReceiptBundle{}, invalidReceipt("failed receipt or mismatched block coordinate")
	}

	var bundle VerificationReceiptBundle
	var successLog, resolutionLog *types.Log
	var updates []struct {
		log   *types.Log
		event chainabi.TrustRootUpdated
	}
	var dependencyLog *types.Log
	previousIndex := uint(0)
	for position, item := range receipt.Logs {
		if item == nil {
			return VerificationReceiptBundle{}, invalidReceipt("nil receipt log")
		}
		if item.BlockHash != input.BlockHash || item.BlockNumber != input.BlockNumber || item.TxHash != receipt.TxHash || item.TxIndex != receipt.TransactionIndex {
			return VerificationReceiptBundle{}, invalidReceipt("receipt log coordinate mismatch")
		}
		if position > 0 && item.Index <= previousIndex {
			return VerificationReceiptBundle{}, invalidReceipt("receipt logs are not strictly ordered")
		}
		previousIndex = item.Index
		if item.Address != input.Gateway {
			continue
		}
		if len(item.Topics) == 0 {
			return VerificationReceiptBundle{}, invalidReceipt("Gateway log has no topic")
		}
		switch item.Topics[0] {
		case chainabi.DirectVerificationSucceededTopic, chainabi.PathVerificationSucceededTopic:
			if successLog != nil {
				return VerificationReceiptBundle{}, invalidReceipt("multiple verification success events")
			}
			event, err := chainabi.ParseVerificationSucceeded(*item)
			if err != nil {
				return VerificationReceiptBundle{}, invalidReceipt(err.Error())
			}
			bundle.Success, successLog = event, item
		case chainabi.TrustRootUpdatedTopic:
			event, err := chainabi.ParseTrustRootUpdated(*item)
			if err != nil {
				return VerificationReceiptBundle{}, invalidReceipt(err.Error())
			}
			updates = append(updates, struct {
				log   *types.Log
				event chainabi.TrustRootUpdated
			}{item, event})
		case chainabi.DependencyRecordedTopic:
			if dependencyLog != nil {
				return VerificationReceiptBundle{}, invalidReceipt("multiple DependencyRecorded events")
			}
			event, err := chainabi.ParseDependencyRecorded(*item)
			if err != nil {
				return VerificationReceiptBundle{}, invalidReceipt(err.Error())
			}
			bundle.Dependency, dependencyLog = &event, item
		case chainabi.RequestResolvedTopic:
			if resolutionLog != nil {
				return VerificationReceiptBundle{}, invalidReceipt("multiple RequestResolved events")
			}
			event, err := chainabi.ParseRequestResolved(*item)
			if err != nil {
				return VerificationReceiptBundle{}, invalidReceipt(err.Error())
			}
			bundle.Resolution, resolutionLog = event, item
		case chainabi.VerificationRequestedTopic:
			return VerificationReceiptBundle{}, invalidReceipt("verification receipt unexpectedly contains VerificationRequested")
		default:
			return VerificationReceiptBundle{}, invalidReceipt("unknown Gateway receipt topic")
		}
		bundle.Logs = append(bundle.Logs, *item)
	}

	if successLog == nil || resolutionLog == nil || successLog.Index >= resolutionLog.Index {
		return VerificationReceiptBundle{}, invalidReceipt("missing or misordered success/RequestResolved events")
	}
	if err := verifySuccessAndResolution(input.Request, bundle.Success, bundle.Resolution); err != nil {
		return VerificationReceiptBundle{}, err
	}
	if !bundle.Resolution.NewDependency {
		if len(updates) != 0 || bundle.Dependency != nil {
			return VerificationReceiptBundle{}, invalidReceipt("existing dependency resolution emitted update evidence")
		}
		return bundle, nil
	}
	if bundle.Dependency == nil || dependencyLog == nil || len(updates) != 2 {
		return VerificationReceiptBundle{}, invalidReceipt("new dependency receipt is incomplete")
	}
	anchor, dependencyUpdate := updates[0], updates[1]
	if !(successLog.Index < anchor.log.Index && anchor.log.Index < dependencyUpdate.log.Index && dependencyUpdate.log.Index < dependencyLog.Index && dependencyLog.Index < resolutionLog.Index) {
		return VerificationReceiptBundle{}, invalidReceipt("new dependency receipt events are misordered")
	}
	dependency := *bundle.Dependency
	if dependency.RequestID != input.Request.ID || dependency.SourceChainID != input.Request.SourceChainID || dependency.SourceHeight != input.Request.SourceHeight || dependency.SourceBlockHash != input.Request.SourceBlockHash || dependency.SourceTrustRoot != bundle.Success.SourceTrustRoot || dependency.DependencyKey != bundle.Success.DependencyKey || dependency.DependencyKey != bundle.Resolution.DependencyKey {
		return VerificationReceiptBundle{}, invalidReceipt("DependencyRecorded does not bind the request and success event")
	}
	wantDependencyKey := trustview.ComputeDependencyKey(dependency.SourceChainID, dependency.SourceHeight, dependency.SourceBlockHash, trustview.TrustRoot{Hash: dependency.SourceTrustRoot})
	if dependency.DependencyKey != wantDependencyKey {
		return VerificationReceiptBundle{}, invalidReceipt("dependency key mismatch")
	}
	if anchor.event.RequestID != input.Request.ID || anchor.event.LeafIndex != 0 || !anchor.event.IsAnchor {
		return VerificationReceiptBundle{}, invalidReceipt("invalid anchor update identity")
	}
	wantAnchorLeaf := domain.LeafHash(anchor.event.OldTrustRoot, input.ParentHash)
	if anchor.event.Leaf != wantAnchorLeaf {
		return VerificationReceiptBundle{}, invalidReceipt("anchor leaf does not bind prior root and parent block")
	}
	dependencyLeaf := domain.LeafHash(dependency.SourceTrustRoot, dependency.SourceBlockHash)
	if dependencyUpdate.event.RequestID != input.Request.ID || dependencyUpdate.event.LeafIndex != 1 || dependencyUpdate.event.IsAnchor || dependency.LeafIndex != 1 || dependencyUpdate.event.Leaf != dependencyLeaf || dependencyUpdate.event.OldTrustRoot != anchor.event.NewTrustRoot || bundle.Resolution.HomeTrustRoot != dependencyUpdate.event.NewTrustRoot {
		return VerificationReceiptBundle{}, invalidReceipt("invalid dependency update root chain")
	}

	tree, err := internalproof.NewIncrementalTree(input.MerkleDepth)
	if err != nil {
		return VerificationReceiptBundle{}, invalidReceipt(err.Error())
	}
	if err := tree.Append(wantAnchorLeaf); err != nil || tree.Root() != anchor.event.NewTrustRoot {
		return VerificationReceiptBundle{}, invalidReceipt("anchor update root mismatch")
	}
	if err := tree.Append(dependencyLeaf); err != nil || tree.Root() != dependencyUpdate.event.NewTrustRoot {
		return VerificationReceiptBundle{}, invalidReceipt("dependency update root mismatch")
	}
	zeros, err := internalproof.ZeroHashes(input.MerkleDepth)
	if err != nil {
		return VerificationReceiptBundle{}, invalidReceipt(err.Error())
	}
	siblings := make([]common.Hash, input.MerkleDepth)
	siblings[0] = wantAnchorLeaf
	for level := 1; level < int(input.MerkleDepth); level++ {
		siblings[level] = zeros[level]
	}
	root, err := internalproof.RootFromWitness(dependencyLeaf, dependency.LeafIndex, siblings, input.MerkleDepth)
	if err != nil || root != bundle.Resolution.HomeTrustRoot {
		return VerificationReceiptBundle{}, invalidReceipt("reconstructed membership witness does not close")
	}
	bundle.Witness = &MembershipWitnessMaterial{LeafIndex: dependency.LeafIndex, Siblings: siblings}
	return bundle, nil
}

func verifySuccessAndResolution(request coordinator.Request, success chainabi.VerificationSucceeded, resolved chainabi.RequestResolved) error {
	if success.RequestID != request.ID || success.SourceChainID != request.SourceChainID || success.SourceHeight != request.SourceHeight || success.SourceBlockHash != request.SourceBlockHash || success.DependencyKey != resolved.DependencyKey || resolved.RequestID != request.ID || resolved.Requester != request.Requester {
		return invalidReceipt("success and resolution do not bind the observed request")
	}
	want := trustview.ComputeDependencyKey(success.SourceChainID, success.SourceHeight, success.SourceBlockHash, trustview.TrustRoot{Hash: success.SourceTrustRoot})
	if success.DependencyKey != want {
		return invalidReceipt("success dependency key mismatch")
	}
	return nil
}

func invalidReceipt(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidVerificationReceipt, reason)
}

func computeDependencyKey(chainID domain.ChainID, height domain.BlockHeight, blockHash, root common.Hash) common.Hash {
	return trustview.ComputeDependencyKey(chainID, height, blockHash, trustview.TrustRoot{Hash: root})
}
