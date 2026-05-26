// Package registryreader exposes the minimal read-only surface of the reserved
// blockspace registry that filtering modules (txpool, miner, block validator)
// need. It lives in a leaf package so core/, miner/, and core/txpool/ can
// import it without pulling in consensus/bor/contract → consensus/bor/statefull
// → core/, which would form an import cycle.
package registryreader

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
)

// ClientLookup mirrors the slim "client for address" view returned by the
// registry contract. Defined here (not in consensus/bor/contract) so the
// interface is self-contained in this leaf package.
type ClientLookup struct {
	ClientID *big.Int
	GasQuota uint64
	Admin    common.Address
	Active   bool
}

// Reader is the read-only view of the reserved blockspace registry consumed by
// transaction filtering paths. Callers must nil-check the interface before
// invoking — chain/txpool/miner expose a nil Reader when the chain has no
// registry configured (non-bor engines, devnets without the contract).
type Reader interface {
	HasReservedRegistry() bool
	IsReservedAddress(state *state.StateDB, number uint64, hash common.Hash, account common.Address) (bool, error)
	ReservedClientForAddress(state *state.StateDB, number uint64, hash common.Hash, account common.Address) (ClientLookup, error)
}
