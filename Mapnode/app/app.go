// Package app composes the durable in-process MapNode core. Phase 3 opens no
// live indexer, P2P stream, transaction executor, or operational API worker.
package app

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/bootstrap"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type App struct {
	Registry         *registry.Registry
	Evidence         *store.EvidenceRepository
	Requests         *store.RequestRepository
	TrustView        *store.TrustViewRepository
	Plans            *store.PlanRepository
	PathProofs       *store.PathProofRepository
	Planner          *planner.Planner
	PathProofBuilder *pathproof.Builder
	Coordinator      *coordinator.Coordinator

	database      *store.DB
	pathTreeDepth uint8
	ready         atomic.Bool
}

// Open performs live chain/code binding before opening SQLite, then maps the
// already-strict bootstrap profile/manifest into the one-home-chain Registry.
func Open(ctx context.Context, config bootstrap.Config, manifest bootstrap.DeploymentManifest) (*App, error) {
	if err := bootstrap.ValidateRuntime(ctx, config, manifest); err != nil {
		return nil, err
	}
	chainRegistry, err := registryFromDeployment(config, manifest)
	if err != nil {
		return nil, err
	}
	database, err := store.Open(config.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("open MapNode SQLite store: %w", err)
	}
	application := &App{Registry: chainRegistry, database: database, pathTreeDepth: manifest.MerkleDepth}
	application.Evidence = store.NewEvidenceRepository(database)
	application.Requests = store.NewRequestRepository(database)
	application.TrustView = store.NewTrustViewRepository(database)
	application.Plans = store.NewPlanRepository(database)
	application.PathProofs = store.NewPathProofRepository(database)
	application.Planner = planner.New(application.TrustView, application.Plans, chainRegistry)
	application.PathProofBuilder = pathproof.NewBuilder(application.PathProofs, manifest.MerkleDepth)
	application.Coordinator = coordinator.New(application.Requests, application.TrustView, application.Planner, application.Plans, application.PathProofBuilder)
	application.ready.Store(true)
	return application, nil
}

func (application *App) Ready() bool { return application != nil && application.ready.Load() }

func (application *App) PathTreeDepth() uint8 {
	if application == nil {
		return 0
	}
	return application.pathTreeDepth
}

func (application *App) Close() error {
	if application == nil {
		return nil
	}
	application.ready.Store(false)
	if application.database == nil {
		return nil
	}
	return application.database.Close()
}

func registryFromDeployment(config bootstrap.Config, manifest bootstrap.DeploymentManifest) (*registry.Registry, error) {
	if config.DirectVerifier.Profile == nil {
		return nil, errors.New("validated direct verifier profile is required")
	}
	if manifest.MerkleDepth == 0 || manifest.MerkleDepth > 32 {
		return nil, errors.New("validated deployment merkleDepth is required")
	}
	chainNumber, ok := new(big.Int).SetString(config.HomeChain.ChainID, 10)
	if !ok {
		return nil, errors.New("invalid home chain ID")
	}
	chainID, err := domain.NewChainIDFromBig(chainNumber)
	if err != nil {
		return nil, err
	}
	configured, err := configuredParameters(*config.DirectVerifier.Profile)
	if err != nil {
		return nil, err
	}
	deployed, err := deployedParameters(manifest)
	if err != nil {
		return nil, err
	}
	return registry.New([]registry.Chain{{
		Name: config.HomeChain.Name, ChainID: chainID, MapNodeID: config.Name,
		Home: true, SignerAuthority: true, TransactionAuthority: true,
		Profile: registry.DeploymentProfile{Configured: configured, Deployed: deployed, Fingerprint: registry.ComputeProfileFingerprint(configured)},
	}})
}

func configuredParameters(profile bootstrap.DirectVerifierProfile) (registry.DirectVerifierParameters, error) {
	signers := make([]common.Address, len(profile.AuthorizedSigners))
	for index, signer := range profile.AuthorizedSigners {
		if !common.IsHexAddress(signer.Address) {
			return registry.DirectVerifierParameters{}, fmt.Errorf("invalid configured signer %d", index)
		}
		signers[index] = common.HexToAddress(signer.Address)
	}
	return registry.DirectVerifierParameters{ID: profile.ProfileID, AuthorizedSigners: signers, SignatureChecks: profile.SignatureChecks, HashRounds: profile.HashRounds, MeasuredDirectCostGas: optionalCost(profile.MeasuredDirectCostGas)}, nil
}

func deployedParameters(manifest bootstrap.DeploymentManifest) (registry.DirectVerifierParameters, error) {
	signers := make([]common.Address, len(manifest.AuthorizedSigners))
	for index, signer := range manifest.AuthorizedSigners {
		if !common.IsHexAddress(signer) {
			return registry.DirectVerifierParameters{}, fmt.Errorf("invalid deployed signer %d", index)
		}
		signers[index] = common.HexToAddress(signer)
	}
	return registry.DirectVerifierParameters{ID: manifest.ProfileID, AuthorizedSigners: signers, SignatureChecks: manifest.SignatureChecks, HashRounds: manifest.HashRounds, MeasuredDirectCostGas: optionalCost(manifest.MeasuredDirectCostGas)}, nil
}

func optionalCost(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}
