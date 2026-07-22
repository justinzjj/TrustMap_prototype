package coordinator

import "testing"

func TestRequestStateTransitionGraph(t *testing.T) {
	legal := [][2]RequestState{
		{Observed, EvidenceReady}, {EvidenceReady, Planned}, {Planned, ProofReady},
		{Observed, Rejected}, {Retryable, Replanned}, {Retryable, DirectFallback}, {Replanned, ProofReady},
		{Planned, Retryable}, {ProofReady, Retryable}, {DirectFallback, Retryable},
	}
	for _, edge := range legal {
		changed, err := ValidateTransition(edge[0], edge[1])
		if err != nil || !changed {
			t.Fatalf("%s -> %s = (%t, %v), want (true, nil)", edge[0], edge[1], changed, err)
		}
	}
	for _, state := range AllRequestStates() {
		changed, err := ValidateTransition(state, state)
		if err != nil || changed {
			t.Fatalf("%s -> itself = (%t, %v), want idempotent", state, changed, err)
		}
	}
	for _, edge := range [][2]RequestState{
		{Observed, Planned}, {EvidenceReady, Observed}, {ProofReady, RequestState("submitted")},
		{Retryable, ProofReady}, {DirectFallback, RequestState("confirmed")}, {Rejected, Observed},
	} {
		if changed, err := ValidateTransition(edge[0], edge[1]); err == nil || changed {
			t.Fatalf("%s -> %s accepted", edge[0], edge[1])
		}
	}
}

func TestPhase3DoesNotExposeSubmissionStates(t *testing.T) {
	for _, state := range []RequestState{"submitted", "confirmed"} {
		if err := state.Validate(); err == nil {
			t.Fatalf("Phase 4 state %q accepted by Phase 3", state)
		}
	}
}
