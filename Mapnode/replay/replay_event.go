package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// ReplayEvent is one prepared cross-chain verification event.
type ReplayEvent struct {
	ID          string
	OriginalRow uint64
	Sequence    uint64
	Source      ReplayBlock
	Destination ReplayBlock
	SourceTime  time.Time
	BridgeName  string
	TxCount     int64
	VolumeUSD   float64
}

// ComputeEventID binds a prepared event to its source trace and original
// coordinates. Derived normalized heights and optional metadata are excluded.
func ComputeEventID(traceDigest string, event ReplayEvent) string {
	material := fmt.Sprintf(
		"trustmap/replay-event/v1\n%s\n%d\n%d\n%s\n%d\n%s\n%d\n%s",
		traceDigest,
		event.OriginalRow,
		event.Sequence,
		event.Source.Chain,
		event.Source.OriginalHeight,
		event.Destination.Chain,
		event.Destination.OriginalHeight,
		event.SourceTime.UTC().Format(time.RFC3339Nano),
	)
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}
