// Package app composes the durable MapNode core, confirmation-only Gateway
// indexer, and dependency-evidence GossipSub. Transaction execution and the
// inspection API remain deferred to later Phase 4 tasks.
package app

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/bootstrap"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/evidence"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/indexer"
	tmp2p "github.com/justinzjj/TrustMap_prototype/Mapnode/p2p"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	pathproof "github.com/justinzjj/TrustMap_prototype/Mapnode/proof"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/registry"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/store"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
)

var (
	ErrOperationalDegraded    = errors.New("MapNode canonical cursor is degraded")
	ErrOperationalUnavailable = errors.New("MapNode operational gate is unavailable")
)

type App struct {
	Registry                 *registry.Registry
	ChainCatalog             *chain.Registry
	LiveChains               *store.LiveChainRepository
	TrustRootObservations    *store.TrustRootObservationRepository
	Evidence                 *store.EvidenceRepository
	Requests                 *store.RequestRepository
	TrustView                *store.TrustViewRepository
	Plans                    *store.PlanRepository
	PathProofs               *store.PathProofRepository
	PathProofBuilder         *pathproof.Builder
	ConfirmedEventIndexer    *indexer.ConfirmedEventIndexer
	EvidenceInbox            *store.EvidenceInboxRepository
	RemoteDependencies       *store.RemoteDependencyRepository
	DependencyEvidenceGossip *tmp2p.DependencyEvidenceGossip

	database          *store.DB
	pathTreeDepth     uint8
	ready             atomic.Bool
	operationalMu     sync.RWMutex
	indexerValidated  bool
	indexerFailed     bool
	p2pFailed         bool
	p2pHost           host.Host
	p2pCancel         context.CancelFunc
	p2pRequired       bool
	p2pBootstrapReady atomic.Bool
	staticBootstrap   *tmp2p.StaticBootstrapper
	evidenceOutbox    *store.EvidenceOutboxRepository
	canonicalCursors  *store.CanonicalCursorRepository
	planner           *planner.Planner
	coordinator       requestProcessor
	indexerRepository indexerStore
	observerMu        sync.Mutex
	observers         map[domain.ChainID]observerBinding
}

type indexerStore interface {
	Configure(context.Context, store.IndexerConfig) error
	ApplyConfirmedBlock(context.Context, indexer.ConfirmedBlock) (bool, error)
	Degrade(context.Context, domain.ChainID, string) error
	HasDegraded(context.Context) (bool, error)
}

type observerBinding struct {
	client     *ethclient.Client
	reader     *chain.TrustRootReader
	deployment chain.GatewayDeployment
	block      domain.BlockHeight
}

type requestProcessor interface {
	Process(context.Context, coordinator.Work) (coordinator.Result, error)
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
	application.canonicalCursors = store.NewCanonicalCursorRepository(database)
	indexerRepository := store.NewIndexerRepository(database)
	application.indexerRepository = indexerRepository
	application.TrustRootObservations = store.NewTrustRootObservationRepository(database)
	application.Evidence = store.NewEvidenceRepository(database)
	application.Requests = store.NewRequestRepository(database)
	application.TrustView = store.NewTrustViewRepository(database)
	application.Plans = store.NewPlanRepository(database)
	application.PathProofs = store.NewPathProofRepository(database)
	application.planner = planner.New(application.TrustView, application.Plans, chainRegistry)
	application.PathProofBuilder = pathproof.NewBuilder(application.PathProofs, manifest.MerkleDepth)
	application.coordinator = coordinator.New(application.Requests, application.TrustView, application.planner, application.Plans, application.PathProofBuilder)
	pathStepCost := bootstrap.DefaultPathStepCostGas
	if manifest.PathStepCostGas != nil {
		pathStepCost = *manifest.PathStepCostGas
	}
	if err := application.indexerRepository.Configure(ctx, store.IndexerConfig{ChainID: homeChainID, Gateway: common.HexToAddress(manifest.Gateway), GatewayCodeHash: common.HexToHash(manifest.CodeHashes.Gateway), DeploymentBlock: deploymentHeight, MerkleDepth: manifest.MerkleDepth, PathStepCostGas: pathStepCost}); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("persist confirmed indexer configuration: %w", err)
	}
	homeRPC, err := ethclient.DialContext(ctx, config.HomeChain.HTTPRPC)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("dial home HTTP RPC for confirmed indexer: %w", err)
	}
	application.observers = make(map[domain.ChainID]observerBinding)
	confirmedIndexer, err := indexer.NewConfirmedEventIndexer(indexer.ConfirmedEventIndexerConfig{ChainID: homeChainID, Gateway: common.HexToAddress(manifest.Gateway), GatewayCodeHash: common.HexToHash(manifest.CodeHashes.Gateway), DeploymentBlock: deploymentHeight, Confirmations: config.HomeChain.Confirmations, MaxBlockRange: 256, MerkleDepth: manifest.MerkleDepth, PathStepCostGas: pathStepCost}, homeRPC, application, application)
	if err != nil {
		homeRPC.Close()
		_ = database.Close()
		return nil, err
	}
	application.observers[homeChainID] = observerBinding{client: homeRPC}
	application.ConfirmedEventIndexer = confirmedIndexer
	if config.P2P.Enabled {
		privateKey, peerID, err := tmp2p.LoadPersistentEd25519Identity(config.P2P.PrivateKeyFile)
		if err != nil {
			homeRPC.Close()
			_ = database.Close()
			return nil, err
		}
		bootstrapPeers, err := tmp2p.LoadBootstrapPeers(config.P2P.BootstrapFile, peerID)
		if err != nil {
			homeRPC.Close()
			_ = database.Close()
			return nil, err
		}
		p2pHost, err := libp2p.New(libp2p.Identity(privateKey), libp2p.ListenAddrStrings(config.P2P.Listen))
		if err != nil {
			homeRPC.Close()
			_ = database.Close()
			return nil, fmt.Errorf("create persistent libp2p host: %w", err)
		}
		application.p2pHost = p2pHost
		application.p2pRequired = true
		application.EvidenceInbox = store.NewEvidenceInboxRepository(database)
		application.evidenceOutbox = store.NewEvidenceOutboxRepository(database)
		application.RemoteDependencies = store.NewRemoteDependencyRepository(database)
		p2pContext, p2pCancel := context.WithCancel(context.Background())
		gossip, err := tmp2p.NewDependencyEvidenceGossip(p2pContext, p2pHost, application.EvidenceInbox, application.evidenceOutbox, tmp2p.GossipConfig{})
		if err != nil {
			p2pCancel()
			_ = p2pHost.Close()
			homeRPC.Close()
			_ = database.Close()
			return nil, err
		}
		bootstrapper, err := tmp2p.NewStaticBootstrapper(p2pHost, bootstrapPeers, tmp2p.StaticBootstrapConfig{}, application.p2pBootstrapReady.Store)
		if err != nil {
			_ = gossip.Close()
			p2pCancel()
			_ = p2pHost.Close()
			homeRPC.Close()
			_ = database.Close()
			return nil, err
		}
		if len(bootstrapPeers) == 0 {
			application.p2pBootstrapReady.Store(true)
		}
		application.p2pCancel = p2pCancel
		application.DependencyEvidenceGossip = gossip
		application.staticBootstrap = bootstrapper
		indexerRepository.ConfigureEvidenceOutbox(application.evidenceOutbox, peerID)
	}
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
	if application == nil {
		return false
	}
	application.operationalMu.RLock()
	defer application.operationalMu.RUnlock()
	return application.operationalGate(context.Background()) == nil
}

func (application *App) Process(ctx context.Context, work coordinator.Work) (coordinator.Result, error) {
	if application == nil {
		return coordinator.Result{}, ErrOperationalUnavailable
	}
	application.operationalMu.RLock()
	defer application.operationalMu.RUnlock()
	if err := application.operationalGate(ctx); err != nil {
		return coordinator.Result{}, err
	}
	return application.coordinator.Process(ctx, work)
}

func (application *App) operationalGate(ctx context.Context) error {
	if err := application.indexerWorkerGate(ctx); err != nil {
		return err
	}
	if !application.indexerValidated {
		return ErrOperationalUnavailable
	}
	return nil
}

func (application *App) indexerWorkerGate(ctx context.Context) error {
	if !application.ready.Load() || application.canonicalCursors == nil || application.coordinator == nil {
		return ErrOperationalUnavailable
	}
	if application.indexerFailed {
		return ErrOperationalDegraded
	}
	if application.p2pFailed {
		return ErrOperationalDegraded
	}
	if application.p2pRequired && !application.p2pBootstrapReady.Load() {
		return ErrOperationalUnavailable
	}
	degraded, err := application.canonicalCursors.HasDegradedCanonicalCursor(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOperationalUnavailable, err)
	}
	if degraded {
		return ErrOperationalDegraded
	}
	if application.indexerRepository != nil {
		degraded, err := application.indexerRepository.HasDegraded(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrOperationalUnavailable, err)
		}
		if degraded {
			return ErrOperationalDegraded
		}
	}
	return nil
}

func (application *App) InitializeCanonicalCursor(ctx context.Context, cursor reorg.CanonicalCursor) (reorg.CanonicalCursor, bool, error) {
	if application == nil {
		return reorg.CanonicalCursor{}, false, ErrOperationalUnavailable
	}
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if !application.ready.Load() || application.canonicalCursors == nil {
		return reorg.CanonicalCursor{}, false, ErrOperationalUnavailable
	}
	return application.canonicalCursors.Initialize(ctx, cursor)
}

func (application *App) AdvanceCanonicalCursor(ctx context.Context, chainID domain.ChainID, recheckedHash common.Hash, next reorg.CanonicalBlock) (reorg.CanonicalCursor, bool, error) {
	if application == nil {
		return reorg.CanonicalCursor{}, false, ErrOperationalUnavailable
	}
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if !application.ready.Load() || application.canonicalCursors == nil {
		return reorg.CanonicalCursor{}, false, ErrOperationalUnavailable
	}
	return application.canonicalCursors.Advance(ctx, chainID, recheckedHash, next)
}

func (application *App) LoadCanonicalCursor(ctx context.Context, chainID domain.ChainID) (reorg.CanonicalCursor, error) {
	if application == nil {
		return reorg.CanonicalCursor{}, ErrOperationalUnavailable
	}
	application.operationalMu.RLock()
	defer application.operationalMu.RUnlock()
	if !application.ready.Load() || application.canonicalCursors == nil {
		return reorg.CanonicalCursor{}, ErrOperationalUnavailable
	}
	return application.canonicalCursors.Load(ctx, chainID)
}

func (application *App) PathTreeDepth() uint8 {
	if application == nil {
		return 0
	}
	return application.pathTreeDepth
}

func (application *App) StepConfirmedIndexer(ctx context.Context) (indexer.StepResult, error) {
	if application == nil || application.ConfirmedEventIndexer == nil {
		return indexer.StepResult{}, ErrOperationalUnavailable
	}
	return application.ConfirmedEventIndexer.Step(ctx)
}

func (application *App) RunConfirmedIndexer(ctx context.Context) error {
	if application == nil || application.ConfirmedEventIndexer == nil {
		return ErrOperationalUnavailable
	}
	return application.ConfirmedEventIndexer.Run(ctx)
}

func (application *App) LoadIndexerCursor(ctx context.Context, chainID domain.ChainID) (reorg.CanonicalCursor, bool, error) {
	application.operationalMu.RLock()
	defer application.operationalMu.RUnlock()
	if !application.ready.Load() || application.canonicalCursors == nil {
		return reorg.CanonicalCursor{}, false, ErrOperationalUnavailable
	}
	cursor, err := application.canonicalCursors.Load(ctx, chainID)
	if errors.Is(err, store.ErrRecordNotFound) {
		return reorg.CanonicalCursor{}, false, nil
	}
	return cursor, err == nil, err
}

func (application *App) InitializeIndexerCursor(ctx context.Context, cursor reorg.CanonicalCursor) error {
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if !application.ready.Load() || application.canonicalCursors == nil {
		return ErrOperationalUnavailable
	}
	_, _, err := application.canonicalCursors.Initialize(ctx, cursor)
	if errors.Is(err, store.ErrRecordConflict) {
		return indexer.DeterministicStoreError(err)
	}
	return err
}

func (application *App) MarkIndexerValidated(ctx context.Context, expected reorg.CanonicalCursor) error {
	if application == nil {
		return ErrOperationalUnavailable
	}
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if err := application.indexerWorkerGate(ctx); err != nil {
		return err
	}
	persisted, err := application.canonicalCursors.Load(ctx, expected.ChainID)
	if errors.Is(err, store.ErrRecordNotFound) {
		return indexer.DeterministicStoreError(errors.New("validated indexer cursor disappeared before readiness transition"))
	}
	if err != nil {
		return err
	}
	if persisted != expected || persisted.State != reorg.Healthy {
		return indexer.DeterministicStoreError(errors.New("validated indexer cursor changed before readiness transition"))
	}
	application.indexerValidated = true
	return nil
}

func (application *App) LoadIndexerRequest(ctx context.Context, id domain.RequestID) (coordinator.Request, bool, error) {
	application.operationalMu.RLock()
	defer application.operationalMu.RUnlock()
	if !application.ready.Load() || application.Requests == nil {
		return coordinator.Request{}, false, ErrOperationalUnavailable
	}
	request, err := application.Requests.Load(ctx, id)
	if errors.Is(err, store.ErrRecordNotFound) {
		return coordinator.Request{}, false, nil
	}
	return request, err == nil, err
}

func (application *App) ApplyIndexerBlock(ctx context.Context, block indexer.ConfirmedBlock) (bool, error) {
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if err := application.indexerWorkerGate(ctx); err != nil {
		return false, err
	}
	if application.indexerRepository == nil {
		return false, ErrOperationalUnavailable
	}
	changed, err := application.indexerRepository.ApplyConfirmedBlock(ctx, block)
	if err != nil && deterministicApplyError(err) {
		return false, indexer.DeterministicStoreError(err)
	}
	return changed, err
}

func deterministicApplyError(err error) bool {
	return errors.Is(err, store.ErrRecordConflict) || errors.Is(err, store.ErrConcurrentUpdate) || errors.Is(err, store.ErrInactiveEvidence) || errors.Is(err, store.ErrGraphConflict) || errors.Is(err, store.ErrEvidenceBinding) || errors.Is(err, reorg.ErrCanonicalMismatch) || errors.Is(err, reorg.ErrNonMonotonicCursor) || errors.Is(err, reorg.ErrCursorDegraded) || errors.Is(err, trustview.ErrInvalidTrustEdge) || errors.Is(err, trustview.ErrInvalidTrustNode) || errors.Is(err, trustview.ErrTrustRootConflict)
}

func (application *App) DegradeIndexerCursor(ctx context.Context, chainID domain.ChainID, reason string) error {
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if !application.ready.Load() || application.canonicalCursors == nil {
		return ErrOperationalUnavailable
	}
	application.indexerFailed = true
	if application.indexerRepository == nil {
		return ErrOperationalUnavailable
	}
	if err := application.indexerRepository.Degrade(ctx, chainID, reason); err != nil {
		return err
	}
	if _, err := application.canonicalCursors.Load(ctx, chainID); err == nil {
		_, err = application.canonicalCursors.Degrade(ctx, chainID, reason)
		return err
	} else if !errors.Is(err, store.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (application *App) ObserveExpectedTrustRoot(ctx context.Context, chainID domain.ChainID, height domain.BlockHeight, blockHash, expectedRoot common.Hash) (trustview.TrustNode, error) {
	entry, ok := application.ChainCatalog.Chain(chainID)
	if !ok {
		return trustview.TrustNode{}, indexer.DeterministicObservationError(chain.ErrChainNotFound)
	}
	binding, err := application.observer(entry)
	if err != nil {
		if deterministicObservationError(err) {
			return trustview.TrustNode{}, indexer.DeterministicObservationError(err)
		}
		return trustview.TrustNode{}, err
	}
	observation, err := binding.reader.Observe(ctx, height, blockHash, entry.Confirmations)
	if err != nil {
		if deterministicObservationError(err) {
			return trustview.TrustNode{}, indexer.DeterministicObservationError(err)
		}
		return trustview.TrustNode{}, err
	}
	if observation.TrustRoot.Hash != expectedRoot {
		return trustview.TrustNode{}, indexer.DeterministicObservationError(trustview.ErrTrustRootConflict)
	}
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	if err := application.indexerWorkerGate(ctx); err != nil {
		return trustview.TrustNode{}, err
	}
	if err := application.LiveChains.BindDeployment(ctx, binding.deployment, binding.block); err != nil {
		if errors.Is(err, store.ErrRecordConflict) {
			return trustview.TrustNode{}, indexer.DeterministicObservationError(err)
		}
		return trustview.TrustNode{}, err
	}
	_, node, _, err := application.TrustRootObservations.Save(ctx, observation)
	if errors.Is(err, store.ErrRecordConflict) || errors.Is(err, store.ErrGraphConflict) {
		return trustview.TrustNode{}, indexer.DeterministicObservationError(err)
	}
	return node, err
}

func deterministicObservationError(err error) bool {
	return errors.Is(err, chain.ErrCanonicalBlockMismatch) || errors.Is(err, chain.ErrRemoteChainIDMismatch) || errors.Is(err, chain.ErrGatewayCodeMismatch) || errors.Is(err, chain.ErrGatewayABIMismatch) || errors.Is(err, chain.ErrMalformedRPCResponse) || errors.Is(err, chain.ErrInvalidGatewayManifest) || errors.Is(err, chain.ErrChainNotFound)
}

func (application *App) observer(entry chain.Chain) (observerBinding, error) {
	application.observerMu.Lock()
	defer application.observerMu.Unlock()
	binding := application.observers[entry.ChainID]
	if binding.reader != nil {
		return binding, nil
	}
	client := binding.client
	if client == nil {
		var err error
		client, err = ethclient.Dial(entry.HTTPRPC)
		if err != nil {
			return observerBinding{}, fmt.Errorf("dial TrustRoot observation RPC: %w", err)
		}
	}
	reader, deployment, block, err := chain.NewTrustRootReaderForChain(entry, client)
	if err != nil {
		if binding.client == nil {
			client.Close()
		}
		return observerBinding{}, err
	}
	binding = observerBinding{client: client, reader: reader, deployment: deployment, block: block}
	application.observers[entry.ChainID] = binding
	return binding, nil
}

type remoteObservationAdapter struct{ application *App }

func (adapter remoteObservationAdapter) ObserveExpectedTrustRoot(ctx context.Context, chainID domain.ChainID, height domain.BlockHeight, blockHash, expectedRoot common.Hash) (evidence.ValidatedTrustRootObservation, error) {
	node, err := adapter.application.ObserveExpectedTrustRoot(ctx, chainID, height, blockHash, expectedRoot)
	if err != nil {
		if deterministicObservationError(err) || errors.Is(err, indexer.ErrDeterministicObservation) {
			return evidence.ValidatedTrustRootObservation{}, fmt.Errorf("%w: %v", evidence.ErrInvalidTrustRootObservation, err)
		}
		return evidence.ValidatedTrustRootObservation{}, err
	}
	return evidence.ValidatedTrustRootObservation{ChainID: node.Key.ChainID, Height: node.Key.Height, BlockHash: node.Key.BlockHash, TrustRoot: node.Root.Hash, EvidenceID: node.EvidenceID}, nil
}

func (application *App) remoteValidator(entry chain.Chain) (*evidence.RemoteDependencyValidator, error) {
	binding, err := application.observer(entry)
	if err != nil {
		return nil, err
	}
	config, err := chain.LoadGatewayDeploymentConfig(entry)
	if err != nil {
		return nil, err
	}
	return evidence.NewRemoteDependencyValidator(evidence.RemoteDependencyConfig{ChainID: entry.ChainID, Gateway: config.Deployment.Address, GatewayCodeHash: config.Deployment.CodeHash, DeploymentBlock: config.DeploymentBlock.BigInt().Uint64(), Confirmations: entry.Confirmations, MerkleDepth: config.MerkleDepth, PathStepCostGas: config.PathStepCostGas}, binding.client, remoteObservationAdapter{application})
}

func (application *App) StepRemoteDependencyEvidence(ctx context.Context) (int, error) {
	if application == nil || application.EvidenceInbox == nil || application.RemoteDependencies == nil {
		return 0, ErrOperationalUnavailable
	}
	items, err := application.EvidenceInbox.Pending(ctx, 64, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	validated := 0
	for _, item := range items {
		entry, ok := application.ChainCatalog.Chain(item.Envelope.RecordingChainID)
		if !ok {
			if err := application.EvidenceInbox.MarkInvalid(ctx, item.Envelope.MessageID, "recording chain is absent from trusted catalog"); err != nil {
				return validated, err
			}
			continue
		}
		validator, err := application.remoteValidator(entry)
		if err != nil {
			if deterministicObservationError(err) || errors.Is(err, chain.ErrInvalidGatewayManifest) {
				if markErr := application.EvidenceInbox.MarkInvalid(ctx, item.Envelope.MessageID, err.Error()); markErr != nil {
					return validated, markErr
				}
			} else {
				if markErr := application.EvidenceInbox.MarkRetryable(ctx, item.Envelope.MessageID, err.Error(), time.Now().UTC().Add(time.Second)); markErr != nil {
					return validated, markErr
				}
			}
			continue
		}
		result, err := validator.Validate(ctx, item.Envelope.Locator())
		if errors.Is(err, evidence.ErrRemoteDependencyInvalid) {
			if markErr := application.EvidenceInbox.MarkInvalid(ctx, item.Envelope.MessageID, err.Error()); markErr != nil {
				return validated, markErr
			}
			continue
		}
		if errors.Is(err, evidence.ErrRemoteDependencyRetryable) {
			if markErr := application.EvidenceInbox.MarkRetryable(ctx, item.Envelope.MessageID, err.Error(), time.Now().UTC().Add(time.Second)); markErr != nil {
				return validated, markErr
			}
			continue
		}
		if err != nil {
			return validated, err
		}
		application.operationalMu.Lock()
		if gateErr := application.indexerWorkerGate(ctx); gateErr != nil {
			application.operationalMu.Unlock()
			return validated, gateErr
		}
		_, err = application.RemoteDependencies.Activate(ctx, item.Envelope.MessageID, result)
		application.operationalMu.Unlock()
		if err != nil {
			return validated, err
		}
		validated++
	}
	return validated, nil
}

func (application *App) RunRemoteDependencyEvidence(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := application.StepRemoteDependencyEvidence(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (application *App) RunDependencyEvidenceWorkers(ctx context.Context) error {
	if application == nil || application.DependencyEvidenceGossip == nil {
		return ErrOperationalUnavailable
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerCount := 2
	if application.staticBootstrap != nil {
		workerCount++
	}
	done := make(chan error, workerCount)
	go func() { done <- application.DependencyEvidenceGossip.Run(workerCtx) }()
	go func() { done <- application.RunRemoteDependencyEvidence(workerCtx) }()
	if application.staticBootstrap != nil {
		go func() { done <- application.staticBootstrap.Run(workerCtx) }()
	}
	err := <-done
	cancel()
	for range workerCount - 1 {
		otherErr := <-done
		if errors.Is(err, context.Canceled) && !errors.Is(otherErr, context.Canceled) {
			err = otherErr
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	application.operationalMu.Lock()
	application.p2pFailed = true
	application.operationalMu.Unlock()
	return err
}

func (application *App) Close() error {
	if application == nil {
		return nil
	}
	application.operationalMu.Lock()
	defer application.operationalMu.Unlock()
	application.ready.Store(false)
	if application.p2pCancel != nil {
		application.p2pCancel()
	}
	if application.DependencyEvidenceGossip != nil {
		_ = application.DependencyEvidenceGossip.Close()
	}
	if application.p2pHost != nil {
		_ = application.p2pHost.Close()
	}
	application.observerMu.Lock()
	closed := make(map[*ethclient.Client]struct{})
	for _, binding := range application.observers {
		if binding.client != nil {
			if _, ok := closed[binding.client]; !ok {
				binding.client.Close()
				closed[binding.client] = struct{}{}
			}
		}
	}
	application.observerMu.Unlock()
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
