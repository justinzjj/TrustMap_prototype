package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

var (
	ErrCanonicalBlockMismatch = errors.New("canonical block hash mismatch")
	ErrBlockUnconfirmed       = errors.New("canonical block lacks required confirmations")
)

type canonicalHeaderRPC interface {
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

type CanonicalBlock struct {
	Height              domain.BlockHeight
	Hash                common.Hash
	ConfirmedHeadHeight domain.BlockHeight
	ConfirmedHeadHash   common.Hash
	Confirmations       uint64
}

type CanonicalBlockReader struct{ rpc canonicalHeaderRPC }

func NewCanonicalBlockReader(rpc canonicalHeaderRPC) *CanonicalBlockReader {
	return &CanonicalBlockReader{rpc: rpc}
}

func (reader *CanonicalBlockReader) Confirmed(ctx context.Context, height domain.BlockHeight, expectedHash common.Hash, confirmations uint64) (CanonicalBlock, error) {
	if reader == nil || reader.rpc == nil {
		return CanonicalBlock{}, errors.New("nil canonical block RPC")
	}
	if expectedHash == (common.Hash{}) || confirmations == 0 {
		return CanonicalBlock{}, errors.New("canonical block hash and confirmations are required")
	}
	header, err := reader.rpc.HeaderByNumber(ctx, height.BigInt())
	if err != nil {
		return CanonicalBlock{}, fmt.Errorf("read canonical block %s: %w", height.BigInt(), err)
	}
	if header == nil || header.Number == nil || header.Number.Cmp(height.BigInt()) != 0 || header.Hash() != expectedHash {
		return CanonicalBlock{}, ErrCanonicalBlockMismatch
	}
	head, err := reader.rpc.HeaderByNumber(ctx, nil)
	if err != nil {
		return CanonicalBlock{}, fmt.Errorf("read canonical head: %w", err)
	}
	if head == nil || head.Number == nil {
		return CanonicalBlock{}, errors.New("canonical head is missing its number")
	}
	requiredHead := new(big.Int).Add(height.BigInt(), new(big.Int).SetUint64(confirmations))
	if head.Number.Cmp(requiredHead) < 0 {
		return CanonicalBlock{}, ErrBlockUnconfirmed
	}
	headHeight, err := domain.NewBlockHeightFromBig(head.Number)
	if err != nil {
		return CanonicalBlock{}, fmt.Errorf("canonical head height: %w", err)
	}
	return CanonicalBlock{Height: height, Hash: expectedHash, ConfirmedHeadHeight: headHeight, ConfirmedHeadHash: head.Hash(), Confirmations: confirmations}, nil
}
