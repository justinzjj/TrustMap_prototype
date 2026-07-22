package api

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
)

type trustViewResponse struct {
	Revision uint64              `json:"revision"`
	Nodes    []trustNodeResponse `json:"nodes"`
	Edges    []trustEdgeResponse `json:"edges"`
}
type trustNodeResponse struct {
	NodeID    string `json:"node_id"`
	ChainID   string `json:"chain_id"`
	Height    string `json:"height"`
	BlockHash string `json:"block_hash"`
	TrustRoot string `json:"trust_root"`
}

type trustEdgeResponse struct {
	EdgeID       string `json:"edge_id"`
	FromNodeID   string `json:"from_node_id"`
	ToNodeID     string `json:"to_node_id"`
	EvidenceID   string `json:"evidence_id"`
	LeafIndex    uint32 `json:"leaf_index"`
	PathStepCost uint64 `json:"path_step_cost"`
}

func (handler *Handler) serveTrustView(writer http.ResponseWriter, request *http.Request) {
	view, err := handler.source.CurrentTrustView(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "TrustView unavailable"})
		return
	}
	response := trustViewResponse{Revision: view.Revision}
	for _, node := range view.Nodes {
		response.Nodes = append(response.Nodes, trustNodeResponse{
			NodeID:    common.Hash(node.ID).Hex(),
			ChainID:   node.Key.ChainID.BigInt().String(),
			Height:    node.Key.Height.BigInt().String(),
			BlockHash: node.Key.BlockHash.Hex(),
			TrustRoot: node.Root.Hash.Hex(),
		})
	}
	for _, edge := range view.Edges {
		response.Edges = append(response.Edges, trustEdgeResponse{
			EdgeID:       common.Hash(edge.ID).Hex(),
			FromNodeID:   common.Hash(edge.From).Hex(),
			ToNodeID:     common.Hash(edge.To).Hex(),
			EvidenceID:   common.Hash(edge.EvidenceID).Hex(),
			LeafIndex:    edge.LeafIndex,
			PathStepCost: edge.PathStepCost,
		})
	}
	writeJSON(writer, http.StatusOK, response)
}
