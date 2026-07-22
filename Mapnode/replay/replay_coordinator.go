package replay

import "fmt"

type ReplayDecisionKind string

const (
	ReplayDecisionDirect   ReplayDecisionKind = "direct"
	ReplayDecisionTrustMap ReplayDecisionKind = "trustmap"
)

type ReplayDecision struct {
	EventID              string
	Sequence             uint64
	Setting              Setting
	Decision             ReplayDecisionKind
	PlanningEnabled      bool
	BaselineBefore       uint64
	BaselineAfter        uint64
	DirectStart          uint64
	DirectBlocks         uint64
	CheckpointConfigured bool
	CheckpointPeriod     uint64
	CheckpointHeight     uint64
	CheckpointApplied    bool
	DirectCost           uint64
	PathFound            bool
	PathCost             uint64
	TrustMapTotalCost    uint64
	ChosenCost           uint64
	Path                 ReplayPath
	GraphNodes           uint64
	GraphEdges           uint64
	CrossEdgesAdded      uint64
}

type ReplayCoordinator struct {
	setting   Setting
	profile   CostProfile
	policy    CheckpointPolicy
	view      *ReplayTrustView
	baselines *ReplayBaselines
	planner   ReplayPlanner
}

func NewReplayCoordinator(setting Setting, initHeights map[string]uint64, profile CostProfile, policy CheckpointPolicy) (*ReplayCoordinator, error) {
	if setting != SettingB0 && setting != SettingB1 && setting != SettingB2 && setting != SettingB3 {
		return nil, fmt.Errorf("unknown replay setting %q", setting)
	}
	view, err := NewReplayTrustView(profile)
	if err != nil {
		return nil, err
	}
	return &ReplayCoordinator{
		setting: setting, profile: profile, policy: policy, view: view,
		baselines: NewReplayBaselines(initHeights), planner: ReplayPlanner{Profile: profile},
	}, nil
}

func NewReplayCoordinators(settings []Setting, initHeights map[string]uint64, profile CostProfile, policy CheckpointPolicy) (map[Setting]*ReplayCoordinator, error) {
	coordinators := make(map[Setting]*ReplayCoordinator, len(settings))
	for _, setting := range settings {
		if _, exists := coordinators[setting]; exists {
			return nil, fmt.Errorf("duplicate replay setting %s", setting)
		}
		coordinator, err := NewReplayCoordinator(setting, initHeights, profile, policy)
		if err != nil {
			return nil, err
		}
		coordinators[setting] = coordinator
	}
	return coordinators, nil
}

func (coordinator *ReplayCoordinator) Process(event ReplayEvent) (ReplayDecision, error) {
	start, err := coordinator.view.Activate(event.Destination)
	if err != nil {
		return ReplayDecision{}, fmt.Errorf("activate replay start: %w", err)
	}
	goal, err := coordinator.view.Activate(event.Source)
	if err != nil {
		return ReplayDecision{}, fmt.Errorf("activate replay goal: %w", err)
	}
	estimate, err := coordinator.baselines.EstimateDirect(
		event.Destination.Chain, event.Source.Chain, event.Source.OriginalHeight,
		coordinator.profile, coordinator.policy, coordinator.setting.UsesCheckpoint(),
	)
	if err != nil {
		return ReplayDecision{}, fmt.Errorf("estimate direct replay cost: %w", err)
	}
	result := ReplayDecision{
		EventID: event.ID, Sequence: event.Sequence, Setting: coordinator.setting,
		Decision: ReplayDecisionDirect, PlanningEnabled: coordinator.setting.UsesTrustMap(),
		BaselineBefore: estimate.BaselineBefore, DirectStart: estimate.DirectStart, DirectBlocks: estimate.DirectBlocks,
		CheckpointConfigured: estimate.CheckpointConfigured, CheckpointPeriod: estimate.CheckpointPeriod,
		CheckpointHeight: estimate.CheckpointHeight, CheckpointApplied: estimate.CheckpointApplied,
		DirectCost: estimate.Cost, ChosenCost: estimate.Cost,
	}
	if result.PlanningEnabled {
		choice, planErr := coordinator.planner.Plan(coordinator.view, start, goal, estimate.Cost)
		if planErr != nil {
			return ReplayDecision{}, fmt.Errorf("plan replay TrustMap path: %w", planErr)
		}
		result.PathFound = choice.PathFound
		result.Path = choice.Path
		if choice.PathFound {
			result.PathCost = choice.Path.Cost
			if total, addErr := CheckedAdd(choice.Path.Cost, coordinator.profile.TrustRootUpdateCost); addErr == nil {
				result.TrustMapTotalCost = total
			} else {
				return ReplayDecision{}, addErr
			}
		}
		if choice.UseTrustMap {
			result.Decision = ReplayDecisionTrustMap
			result.ChosenCost = choice.TotalCost
		}
	}
	result.BaselineAfter = coordinator.baselines.Advance(event.Destination.Chain, event.Source.Chain, event.Source.OriginalHeight)
	if _, err := coordinator.view.AddVerifiedDependency(event.Destination, event.Source); err != nil {
		return ReplayDecision{}, fmt.Errorf("add replay dependency after decision: %w", err)
	}
	result.CrossEdgesAdded = coordinator.view.CrossEdgesAdded()
	result.GraphNodes = coordinator.view.NodeCount()
	result.GraphEdges = coordinator.view.EdgeCount()
	return result, nil
}

// Restore applies only the state transition committed for an event. It does
// not invoke the planner, so resuming a run cannot silently select a new path.
func (coordinator *ReplayCoordinator) Restore(event ReplayEvent, decision ReplayDecision) error {
	if decision.EventID != event.ID || decision.Sequence != event.Sequence || decision.Setting != coordinator.setting {
		return fmt.Errorf("restore replay event identity mismatch at sequence %d", event.Sequence)
	}
	if _, err := coordinator.view.Activate(event.Destination); err != nil {
		return fmt.Errorf("restore replay start: %w", err)
	}
	if _, err := coordinator.view.Activate(event.Source); err != nil {
		return fmt.Errorf("restore replay goal: %w", err)
	}
	baseline := coordinator.baselines.Get(event.Destination.Chain, event.Source.Chain)
	if baseline != decision.BaselineBefore {
		return fmt.Errorf("restore replay baseline before mismatch at sequence %d: got %d want %d", event.Sequence, baseline, decision.BaselineBefore)
	}
	after := coordinator.baselines.Advance(event.Destination.Chain, event.Source.Chain, event.Source.OriginalHeight)
	if after != decision.BaselineAfter {
		return fmt.Errorf("restore replay baseline after mismatch at sequence %d: got %d want %d", event.Sequence, after, decision.BaselineAfter)
	}
	if _, err := coordinator.view.AddVerifiedDependency(event.Destination, event.Source); err != nil {
		return fmt.Errorf("restore replay dependency: %w", err)
	}
	if coordinator.view.NodeCount() != decision.GraphNodes || coordinator.view.EdgeCount() != decision.GraphEdges || coordinator.view.CrossEdgesAdded() != decision.CrossEdgesAdded {
		return fmt.Errorf("restore replay graph counter mismatch at sequence %d", event.Sequence)
	}
	return nil
}

func (coordinator *ReplayCoordinator) TrustView() *ReplayTrustView { return coordinator.view }
func (coordinator *ReplayCoordinator) Baselines() *ReplayBaselines { return coordinator.baselines }
