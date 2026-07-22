package replay

type ReplayEdgeKind string

const (
	ReplayIntraChainUpEdge   ReplayEdgeKind = "intra_chain_up"
	ReplayIntraChainDownEdge ReplayEdgeKind = "intra_chain_down"
)

type ReplayEdge struct {
	Kind   ReplayEdgeKind
	Weight uint64
}

// ReplayIntraChainEdge makes ancestry edges explicit in replay audit output.
type ReplayIntraChainEdge struct {
	From   ReplayBlockKey
	To     ReplayBlockKey
	Kind   ReplayEdgeKind
	Weight uint64
}
