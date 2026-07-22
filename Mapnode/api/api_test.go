package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/coordinator"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/executor"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/planner"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type sourceFake struct {
	ready    bool
	degraded bool
	reason   string
	request  RequestStatus
	view     trustview.TrustView
}

func (source sourceFake) OperationalStatus(context.Context) OperationalStatus {
	return OperationalStatus{Ready: source.ready, Degraded: source.degraded, Reason: source.reason}
}
func (source sourceFake) RequestStatus(_ context.Context, id domain.RequestID) (RequestStatus, bool, error) {
	if source.request.RequestID != id {
		return RequestStatus{}, false, nil
	}
	return source.request, true, nil
}
func (source sourceFake) CurrentTrustView(context.Context) (trustview.TrustView, error) {
	return source.view, nil
}

func TestHandlerExposesStrictReadOnlyHealthRequestAndTrustView(t *testing.T) {
	id := domain.RequestID(common.HexToHash("0x1234"))
	source := sourceFake{ready: true, request: RequestStatus{RequestID: id, PhaseState: coordinator.Planned, ExecutionState: executor.Submitted, Attempt: 2, PlanType: planner.PathPlan, HopCount: 2, FallbackReason: planner.NoPath, TxHash: common.HexToHash("0xab")}, view: trustview.TrustView{Revision: 4}}
	handler := NewHandler(source)
	for _, path := range []string{"/health/live", "/health/ready", "/v1/status", "/v1/requests/" + common.Hash(id).Hex(), "/v1/trustview"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		var decoded any
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("GET %s invalid JSON: %v", path, err)
		}
	}
	requestRecorder := httptest.NewRecorder()
	handler.ServeHTTP(requestRecorder, httptest.NewRequest(http.MethodGet, "/v1/requests/"+common.Hash(id).Hex(), nil))
	var requestBody map[string]any
	if err := json.Unmarshal(requestRecorder.Body.Bytes(), &requestBody); err != nil {
		t.Fatal(err)
	}
	if requestBody["hop_count"] != float64(2) || requestBody["fallback_reason"] != string(planner.NoPath) {
		t.Fatalf("request plan detail missing: %s", requestRecorder.Body.String())
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/status", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/requests/0x12", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("short request ID status=%d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/unknown", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown status=%d", recorder.Code)
	}
}

func TestReadyAndStatusFailClosedWhenDegraded(t *testing.T) {
	handler := NewHandler(sourceFake{degraded: true, reason: "executor failed"})
	for _, path := range []string{"/health/ready", "/v1/status"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d", path, recorder.Code)
		}
		if path == "/v1/status" && !json.Valid(recorder.Body.Bytes()) {
			t.Fatal("degraded status is not JSON")
		}
	}
}
