package evidence

import (
	"errors"
	"fmt"
	"time"
)

type State string

const (
	Candidate State = "candidate"
	Verified  State = "verified"
	Confirmed State = "confirmed"
	Active    State = "active"
	Invalid   State = "invalid"
)

var (
	ErrInvalidTransition     = errors.New("invalid evidence state transition")
	ErrInvalidState          = errors.New("invalid evidence state")
	ErrInvalidReasonRequired = errors.New("invalid evidence requires a reason")
)

type Record struct {
	ID            ID
	Locator       Locator
	State         State
	InvalidReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type TransitionMetadata struct {
	At     time.Time
	Reason string
}

func AllStates() []State {
	return []State{Candidate, Verified, Confirmed, Active, Invalid}
}

func (state State) Validate() error {
	switch state {
	case Candidate, Verified, Confirmed, Active, Invalid:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidState, state)
	}
}

func ValidateTransition(from, to State, reason string) (bool, error) {
	if err := from.Validate(); err != nil {
		return false, err
	}
	if err := to.Validate(); err != nil {
		return false, err
	}
	if from == to {
		return false, nil
	}
	legal := (from == Candidate && (to == Verified || to == Invalid)) ||
		(from == Verified && to == Confirmed) ||
		(from == Confirmed && to == Active)
	if !legal {
		return false, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	if to == Invalid && reason == "" {
		return false, ErrInvalidReasonRequired
	}
	return true, nil
}
