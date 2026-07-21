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
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrOperationalDegraded    = errors.New("MapNode canonical cursor is degraded")
	ErrOperationalUnavailable = errors.New("MapNode operational gate is unavailable")
)

type App struct {
	Registry              *registry.Registry
	ChainCatalog          *chain.Registry
	LiveChains            *store.LiveChainRepository
	CanonicalCursors      *store.CanonicalCursorRepository
	TrustRootObservations *store.TrustRootObservationRepository
	Evidence              *store.EvidenceRepository
	Requests              *store.RequestRepository
	TrustView             *store.TrustViewRepository
	Plans                 *store.PlanRepository
	PathProofs            *store.PathProofRepository
	PathProofBuilder      *pathproof.Builder

	database      *store.DB
	pathTreeDepth uint8
	ready         atomic.Bool
	planner       *planner.Planner
	coordinator   *coordinator.Coordinator
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
	chainCatalog, err := chainCatalogFromConfig(config)
	if err != nil {
		return nil, err
	}
	database, err := store.Open(config.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("open MapNode SQLite store: %w", err)
	}
	application := &App{Registry: chainRegistry, ChainCatalog: chainCatalog, database: database, pathTreeDepth: manifest.MerkleDepth}
	application.LiveChains = store.NewLiveChainRepository(database)
	if err := application.LiveChains.Sync(ctx, chainCatalog); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("persist live chain catalog: %w", err)
	}
	homeChainID, err := domain.NewChainIDFromBig(mustDecimal(config.HomeChain.ChainID))
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	deploymentHeight, err := domain.NewBlockHeight(manifest.DeploymentBlock)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := application.LiveChains.BindDeployment(ctx, chain.GatewayDeployment{ChainID: homeChainID, Address: common.HexToAddress(manifest.Gateway), CodeHash: common.HexToHash(manifest.CodeHashes.Gateway)}, deploymentHeight); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("persist home Gateway deployment: %w", err)
	}
	application.CanonicalCursors = store.NewCanonicalCursorRepository(database)
	application.TrustRootObservations = store.NewTrustRootObservationRepository(database)
	application.Evidence = store.NewEvidenceRepository(database)
	application.Requests = store.NewRequestRepository(database)
	application.TrustView = store.NewTrustViewRepository(database)
	application.Plans = store.NewPlanRepository(database)
	application.PathProofs = store.NewPathProofRepository(database)
	application.planner = planner.New(application.TrustView, application.Plans, chainRegistry)
	application.PathProofBuilder = pathproof.NewBuilder(application.PathProofs, manifest.MerkleDepth)
	application.coordinator = coordinator.New(application.Requests, application.TrustView, application.planner, application.Plans, application.PathProofBuilder)
	application.ready.Store(true)
	return application, nil
}

func chainCatalogFromConfig(config bootstrap.Config) (*chain.Registry, error) {
	if len(config.Chains) == 0 {
		return nil, errors.New("validated all-chain catalog is required")
	}
	entries := make([]chain.Chain, 0, len(config.Chains))
	for _, item := range config.Chains {
		chainID, err := domain.NewChainIDFromBig(mustDecimal(item.ChainID))
		if err != nil {
			return nil, fmt.Errorf("live chain %q ID: %w", item.Name, err)
		}
		entries = append(entries, chain.Chain{Name: item.Name, ChainID: chainID, HTTPRPC: item.HTTPRPC, Confirmations: item.Confirmations, DeploymentManifest: item.DeploymentManifest, Home: item.Home})
	}
	catalog, err := chain.NewRegistry(entries)
	if err != nil {
		return nil, err
	}
	home, err := catalog.HomeChain()
	if err != nil {
		return nil, err
	}
	configuredHomeID, err := domain.NewChainIDFromBig(mustDecimal(config.HomeChain.ChainID))
	if err != nil || home.Name != config.HomeChain.Name || home.ChainID != configuredHomeID {
		return nil, errors.New("live chain catalog home does not match home_chain authority")
	}
	return catalog, nil
}

func mustDecimal(value string) *big.Int {
	number, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil
	}
	return number
}

func (application *App) Ready() bool {
	return application.operationalGate(context.Background()) == nil
}

func (application *App) Process(ctx context.Context, work coordinator.Work) (coordinator.Result, error) {
	if err := application.operationalGate(ctx); err != nil {
		return coordinator.Result{}, err
	}
	return application.coordinator.Process(ctx, work)
}

func (application *App) operationalGate(ctx context.Context) error {
	if application == nil || !application.ready.Load() || application.CanonicalCursors == nil {
		return ErrOperationalUnavailable
	}
	degraded, err := application.CanonicalCursors.HasDegradedCanonicalCursor(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOperationalUnavailable, err)
	}
	if degraded {
		return ErrOperationalDegraded
	}
	return nil
}

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
