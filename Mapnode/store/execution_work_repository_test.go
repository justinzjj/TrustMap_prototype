package store

import (
	"context"
	"testing"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
)

func TestExecutionWorkRepositorySelectsOldestAndResumesAttempt(t *testing.T) {
	db := openTestDB(t)
	request, planID, snapshotID := insertExecutionPlanFixture(t, db, planner.DirectPlan)
	requests := NewRequestRepository(db)
	request, _, _ = requests.Transition(context.Background(), request.ID, coordinator.Observed, coordinator.EvidenceReady, coordinator.TransitionMetadata{})
	request, _, _ = requests.Transition(context.Background(), request.ID, coordinator.EvidenceReady, coordinator.Planned, coordinator.TransitionMetadata{})
	repository := NewExecutionWorkRepository(db)
	selected, found, err := repository.NextRequest(context.Background())
	if err != nil || !found || selected.ID != request.ID {
		t.Fatalf("selected=%+v found=%v err=%v", selected, found, err)
	}
	if attempt, err := repository.NextAttempt(context.Background(), request.ID); err != nil || attempt != 0 {
		t.Fatalf("attempt=%d err=%v", attempt, err)
	}
	if err := repository.MarkRetryable(context.Background(), request, "stale home root"); err != nil {
		t.Fatal(err)
	}
	if attempt, err := repository.NextAttempt(context.Background(), request.ID); err != nil || attempt != 1 {
		t.Fatalf("retry attempt=%d err=%v", attempt, err)
	}

	submission := testSubmission(t, request, planID, snapshotID, nil, 3)
	if _, _, err := NewTransactionRepository(db).SavePrepared(context.Background(), submission); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.NextRequest(context.Background()); err != nil || found {
		t.Fatalf("active nonce did not serialize worker found=%v err=%v", found, err)
	}
}
