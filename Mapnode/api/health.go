package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/trustview"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type OperationalStatus struct {
	Ready    bool   `json:"ready"`
	Degraded bool   `json:"degraded"`
	Reason   string `json:"reason"`
}

type Source interface {
	OperationalStatus(context.Context) OperationalStatus
	RequestStatus(context.Context, domain.RequestID) (RequestStatus, bool, error)
	CurrentTrustView(context.Context) (trustview.TrustView, error)
}

type Handler struct{ source Source }

func NewHandler(source Source) http.Handler { return &Handler{source: source} }

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	switch request.URL.Path {
	case "/health/live":
		writeJSON(writer, http.StatusOK, map[string]string{"status": "live"})
		return
	case "/health/ready":
		status := handler.source.OperationalStatus(request.Context())
		if !status.Ready {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
		return
	case "/v1/status":
		status := handler.source.OperationalStatus(request.Context())
		code := http.StatusOK
		if !status.Ready {
			code = http.StatusServiceUnavailable
		}
		writeJSON(writer, code, status)
		return
	case "/v1/trustview":
		handler.serveTrustView(writer, request)
		return
	}
	const prefix = "/v1/requests/"
	if strings.HasPrefix(request.URL.Path, prefix) {
		raw := strings.TrimPrefix(request.URL.Path, prefix)
		if len(raw) != 66 || !strings.HasPrefix(raw, "0x") {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "request ID must be exactly 32 bytes"})
			return
		}
		decoded := common.FromHex(raw)
		if len(decoded) != 32 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "request ID must be exactly 32 bytes"})
			return
		}
		var id domain.RequestID
		copy(id[:], decoded)
		status, found, err := handler.source.RequestStatus(request.Context(), id)
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "request status unavailable"})
			return
		}
		if !found {
			writeJSON(writer, http.StatusNotFound, map[string]string{"error": "request not found"})
			return
		}
		writeJSON(writer, http.StatusOK, status)
		return
	}
	writeJSON(writer, http.StatusNotFound, map[string]string{"error": "not found"})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
