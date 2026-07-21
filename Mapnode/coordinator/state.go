package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type RequestState string

const (
	Observed       RequestState = "observed"
	EvidenceReady  RequestState = "evidence_ready"
	Planned        RequestState = "planned"
	ProofReady     RequestState = "proof_ready"
	Rejected       RequestState = "rejected"
	Retryable      RequestState = "retryable"
	Replanned      RequestState = "replanned"
	DirectFallback RequestState = "direct_fallback"
)

var (
	ErrInvalidRequestState      = errors.New("invalid request state")
	ErrInvalidRequestTransition = errors.New("invalid request state transition")
)

// Uint256 is the lossless database representation of a Solidity uint256.
type Uint256 [32]byte

type Request struct {
	ID              domain.RequestID
	HomeChainID     domain.ChainID
	Gateway         common.Address
	Requester       common.Address
	Nonce           Uint256
	SourceChainID   domain.ChainID
	SourceHeight    domain.BlockHeight
	SourceBlockHash common.Hash
	State           RequestState
	Reason          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type TransitionMetadata struct {
	At     time.Time
	Reason string
}

// RequestRepository is consumer-owned. Concrete stores implement this interface
// without leaking SQL transactions or store-specific types into the coordinator.
type RequestRepository interface {
	Observe(context.Context, Request) (Request, bool, error)
	Load(context.Context, domain.RequestID) (Request, error)
	Transition(context.Context, domain.RequestID, RequestState, RequestState, TransitionMetadata) (Request, bool, error)
}

func AllRequestStates() []RequestState {
	return []RequestState{Observed, EvidenceReady, Planned, ProofReady, Rejected, Retryable, Replanned, DirectFallback}
}

func (state RequestState) Validate() error {
	switch state {
	case Observed, EvidenceReady, Planned, ProofReady, Rejected, Retryable, Replanned, DirectFallback:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidRequestState, state)
	}
}

func ValidateTransition(from, to RequestState) (bool, error) {
	if err := from.Validate(); err != nil {
		return false, err
	}
	if err := to.Validate(); err != nil {
		return false, err
	}
	if from == to {
		return false, nil
	}
	legal := (from == Observed && (to == EvidenceReady || to == Rejected)) ||
		(from == EvidenceReady && to == Planned) ||
		(from == Planned && to == ProofReady) ||
		(from == Retryable && (to == Replanned || to == DirectFallback)) ||
		(from == Replanned && to == ProofReady)
	if !legal {
		return false, fmt.Errorf("%w: %s -> %s", ErrInvalidRequestTransition, from, to)
	}
	return true, nil
}
