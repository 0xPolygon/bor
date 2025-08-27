package types

import "github.com/ethereum/go-ethereum/common"

// StateSyncData represents state received from Ethereum Blockchain
type StateSyncData struct {
	ID       uint64
	Contract common.Address
	Data     string
	TxHash   common.Hash // L1 TxHash
}

// wire shape for RLP (use []byte for Data to avoid string/encoding ambiguity)
type encStateSyncData struct {
	ID       uint64
	Contract common.Address
	Data     []byte
	TxHash   common.Hash
}
