package replay

type ReplayPathSegment struct {
	From   ReplayBlockKey
	To     ReplayBlockKey
	Kind   ReplayEdgeKind
	Weight uint64
}

type ReplayPath struct {
	Cost     uint64
	Nodes    []ReplayBlockKey
	Segments []ReplayPathSegment
}

func (path ReplayPath) NodeCount() uint64 { return uint64(len(path.Nodes)) }
func (path ReplayPath) HopCount() uint64 {
	if len(path.Nodes) == 0 {
		return 0
	}
	return uint64(len(path.Nodes) - 1)
}
