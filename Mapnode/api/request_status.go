package api

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/executor"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type RequestStatus struct {
	RequestID      domain.RequestID         `json:"-"`
	PhaseState     coordinator.RequestState `json:"phase_state"`
	ExecutionState executor.ExecutionState  `json:"execution_state,omitempty"`
	Reason         string                   `json:"reason"`
	Attempt        uint64                   `json:"attempt"`
	PlanType       planner.PlanType         `json:"plan_type,omitempty"`
	HopCount       int                      `json:"hop_count"`
	FallbackReason planner.FallbackReason   `json:"fallback_reason,omitempty"`
	TxHash         common.Hash              `json:"tx_hash,omitempty"`
}

func (status RequestStatus) MarshalJSON() ([]byte, error) {
	type wire struct {
		RequestID      string                   `json:"request_id"`
		PhaseState     coordinator.RequestState `json:"phase_state"`
		ExecutionState executor.ExecutionState  `json:"execution_state,omitempty"`
		Reason         string                   `json:"reason"`
		Attempt        uint64                   `json:"attempt"`
		PlanType       planner.PlanType         `json:"plan_type,omitempty"`
		HopCount       int                      `json:"hop_count"`
		FallbackReason planner.FallbackReason   `json:"fallback_reason,omitempty"`
		TxHash         string                   `json:"tx_hash,omitempty"`
	}
	tx := ""
	if status.TxHash != (common.Hash{}) {
		tx = status.TxHash.Hex()
	}
	return json.Marshal(wire{RequestID: common.Hash(status.RequestID).Hex(), PhaseState: status.PhaseState, ExecutionState: status.ExecutionState, Reason: status.Reason, Attempt: status.Attempt, PlanType: status.PlanType, HopCount: status.HopCount, FallbackReason: status.FallbackReason, TxHash: tx})
}
