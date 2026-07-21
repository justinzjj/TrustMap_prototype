package reorg

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrCanonicalMismatch  = errors.New("persisted canonical block hash mismatch")
	ErrCursorDegraded     = errors.New("canonical cursor is degraded")
	ErrNonMonotonicCursor = errors.New("canonical cursor must advance by one block")
)

type CursorState string

const (
	Healthy  CursorState = "healthy"
	Degraded CursorState = "degraded"
)

type CanonicalBlock struct {
	Height domain.BlockHeight
	Hash   common.Hash
}

type CanonicalCursor struct {
	ChainID        domain.ChainID
	Height         domain.BlockHeight
	Hash           common.Hash
	State          CursorState
	DegradedReason string
}

func NewCanonicalCursor(chainID domain.ChainID, height domain.BlockHeight, hash common.Hash) CanonicalCursor {
	return CanonicalCursor{ChainID: chainID, Height: height, Hash: hash, State: Healthy}
}

func (cursor CanonicalCursor) Advance(recheckedHash common.Hash, next CanonicalBlock) (CanonicalCursor, error) {
	if cursor.State == Degraded {
		return cursor, ErrCursorDegraded
	}
	if recheckedHash != cursor.Hash {
		return cursor, ErrCanonicalMismatch
	}
	want := new(big.Int).Add(cursor.Height.BigInt(), big.NewInt(1))
	if next.Height.BigInt().Cmp(want) != 0 || next.Hash == (common.Hash{}) {
		return cursor, ErrNonMonotonicCursor
	}
	cursor.Height, cursor.Hash = next.Height, next.Hash
	return cursor, nil
}

func (cursor CanonicalCursor) Degrade(reason string) CanonicalCursor {
	cursor.State = Degraded
	if reason == "" {
		reason = fmt.Sprintf("%v", ErrCanonicalMismatch)
	}
	cursor.DegradedReason = reason
	return cursor
}
