package replay

// ReplayBlock is a trace block coordinate. OriginalHeight is the compatibility
// identity; NormalizedHeight is a per-chain translated audit coordinate.
type ReplayBlock struct {
	Chain            string
	OriginalHeight   uint64
	NormalizedHeight uint64
}
