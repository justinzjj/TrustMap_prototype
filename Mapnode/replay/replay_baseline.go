package replay

import "sync"

type baselineKey struct {
	verifier string
	target   string
}

type ReplayBaselines struct {
	mu          sync.RWMutex
	initHeights map[string]uint64
	values      map[baselineKey]uint64
}

func NewReplayBaselines(initHeights map[string]uint64) *ReplayBaselines {
	copyOfInit := make(map[string]uint64, len(initHeights))
	for chain, height := range initHeights {
		copyOfInit[canonicalChain(chain)] = height
	}
	return &ReplayBaselines{initHeights: copyOfInit, values: make(map[baselineKey]uint64)}
}

func (baselines *ReplayBaselines) Get(verifierChain, targetChain string) uint64 {
	key := baselineKey{verifier: canonicalChain(verifierChain), target: canonicalChain(targetChain)}
	baselines.mu.RLock()
	value, ok := baselines.values[key]
	baselines.mu.RUnlock()
	if ok {
		return value
	}
	baselines.mu.Lock()
	defer baselines.mu.Unlock()
	if value, ok = baselines.values[key]; !ok {
		value = baselines.initHeights[key.target]
		baselines.values[key] = value
	}
	return value
}

func (baselines *ReplayBaselines) Advance(verifierChain, targetChain string, height uint64) uint64 {
	key := baselineKey{verifier: canonicalChain(verifierChain), target: canonicalChain(targetChain)}
	baselines.mu.Lock()
	defer baselines.mu.Unlock()
	current, ok := baselines.values[key]
	if !ok {
		current = baselines.initHeights[key.target]
	}
	if height > current {
		current = height
	}
	baselines.values[key] = current
	return current
}

type ReplayDirectEstimate struct {
	BaselineBefore       uint64
	DirectStart          uint64
	DirectBlocks         uint64
	CheckpointConfigured bool
	CheckpointPeriod     uint64
	CheckpointHeight     uint64
	CheckpointApplied    bool
	Cost                 uint64
}

func (baselines *ReplayBaselines) EstimateDirect(verifierChain, targetChain string, targetHeight uint64, profile CostProfile, policy CheckpointPolicy, useCheckpoint bool) (ReplayDirectEstimate, error) {
	baseline := baselines.Get(verifierChain, targetChain)
	estimate := ReplayDirectEstimate{BaselineBefore: baseline, DirectStart: baseline}
	if targetHeight >= baseline {
		if useCheckpoint {
			if period, ok := policy.Period(targetChain); ok {
				checkpoint := (targetHeight / period) * period
				estimate.CheckpointConfigured = true
				estimate.CheckpointPeriod = period
				estimate.CheckpointHeight = checkpoint
				if checkpoint > estimate.DirectStart {
					estimate.DirectStart = checkpoint
					estimate.CheckpointApplied = true
				}
			}
		}
		estimate.DirectBlocks = targetHeight - estimate.DirectStart
		cost, err := CheckedCost(estimate.DirectBlocks, profile.DirectStepCost)
		estimate.Cost = cost
		return estimate, err
	}
	estimate.DirectBlocks = baseline - targetHeight
	cost, err := CheckedCost(estimate.DirectBlocks, profile.PathStepCost)
	estimate.Cost = cost
	return estimate, err
}
