package replay

// ReplayBlock is a trace block coordinate. OriginalHeight is the compatibility
// identity; NormalizedHeight is a per-chain translated audit coordinate.
type ReplayBlock struct {
	Chain            string
	OriginalHeight   uint64
	NormalizedHeight uint64
}

// ReplayBlockKey is the graph identity used by ReplayTrustView. Normalized
// heights are deliberately excluded: legacy compatibility is defined over the
// original trace coordinates.
type ReplayBlockKey struct {
	Chain  string
	Height uint64
}

func (block ReplayBlock) Key() ReplayBlockKey {
	return ReplayBlockKey{Chain: canonicalChain(block.Chain), Height: block.OriginalHeight}
}
