package replay

const ReplayVerifiedDependencyEdgeKind ReplayEdgeKind = "verified_dependency"

// ReplayVerifiedDependencyEdge is a replay-only logical dependency. It must
// not be confused with the evidence-bearing TrustEdge used by live mode.
type ReplayVerifiedDependencyEdge struct {
	From   ReplayBlockKey
	To     ReplayBlockKey
	Weight uint64
}
