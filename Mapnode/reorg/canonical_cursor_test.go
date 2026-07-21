package reorg_test

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/reorg"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

func TestCanonicalCursorFailsClosedAfterMismatch(t *testing.T) {
	chainID, _ := domain.NewChainID(10001)
	height, _ := domain.NewBlockHeight(7)
	nextHeight, _ := domain.NewBlockHeight(8)
	cursor := reorg.NewCanonicalCursor(chainID, height, common.HexToHash("0x7"))
	if _, err := cursor.Advance(common.HexToHash("0x8"), reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x9")}); !errors.Is(err, reorg.ErrCanonicalMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	degraded := cursor.Degrade("persisted block hash no longer canonical")
	if degraded.State != reorg.Degraded || degraded.DegradedReason == "" {
		t.Fatalf("degraded cursor = %+v", degraded)
	}
	if _, err := degraded.Advance(degraded.Hash, reorg.CanonicalBlock{Height: nextHeight, Hash: common.HexToHash("0x9")}); !errors.Is(err, reorg.ErrCursorDegraded) {
		t.Fatalf("degraded advance error = %v", err)
	}
}
