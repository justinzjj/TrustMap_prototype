package proof

import "errors"

const MaxTreeDepth uint8 = 32

var (
	ErrInvalidTreeDepth    = errors.New("tree depth must be between 1 and 32")
	ErrInvalidSiblingCount = errors.New("witness sibling count does not match tree depth")
	ErrLeafIndexOutOfRange = errors.New("witness leaf index is outside tree capacity")
	ErrEmptyPath           = errors.New("path must contain at least one hop")
	ErrPathLengthMismatch  = errors.New("block hash and witness counts do not match")
	ErrFinalRootMismatch   = errors.New("reconstructed root does not match expected home root")
	ErrTreeFull            = errors.New("incremental Merkle tree is full")
)
