package trustview

import "github.com/ethereum/go-ethereum/common"

// TrustRoot is the paper's per-block commitment to the chain's recorded
// cross-chain trust dependencies.
type TrustRoot struct {
	Hash common.Hash
}
