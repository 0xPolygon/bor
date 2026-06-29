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
	// FeeMode: 0 = free (zero in-protocol fee), 1 = routed (fee credited to the
	// producer). See the reserved-blockspace spec §7.
	FeeMode uint8
	// EffectiveFrom: block from which the client's reserved status applies.
	// Callers gate on Active && EffectiveFrom <= number.
	EffectiveFrom uint64
}

// Reader is the read-only view of the reserved blockspace registry consumed by
// transaction filtering paths. Callers must nil-check the interface before
// invoking — chain/txpool/miner expose a nil Reader when the chain has no
// registry configured (non-bor engines, devnets without the contract).
type Reader interface {
	HasReservedRegistry() bool
	IsReservedAddress(state *state.StateDB, number uint64, hash common.Hash, account common.Address) (bool, error)
	ReservedClientForAddress(state *state.StateDB, number uint64, hash common.Hash, account common.Address) (ClientLookup, error)
	// Root returns the registry's configVersion-derived root. It changes whenever
	// the reserved set or its limits change, so a snapshot keyed on it can be
	// reused until it moves (spec §4.5). The root()-keyed cross-block cache is a
	// tracked optimization (POS-3574) not yet wired on the execution path; see
	// BuildSnapshot.
	Root(state *state.StateDB, number uint64, hash common.Hash) (common.Hash, error)
	// WhitelistedAddresses returns every currently-active reserved address.
	WhitelistedAddresses(state *state.StateDB, number uint64, hash common.Hash) ([]common.Address, error)
	// TotalReservedGas returns the sum of active client quotas (reserved capacity).
	TotalReservedGas(state *state.StateDB, number uint64, hash common.Hash) (uint64, error)
}

// Snapshot is an immutable, pure-lookup view of the reserved set as of one
// block's state, so the hot classification paths — txpool admission, the EVM
// fee-skip stand-in, base-fee capacity — never do a per-transaction state read
// (spec §4.5). The txpool rebuilds it once per head (on reset); the execution
// path rebuilds it once per block (gated on the fork height). Cross-block reuse
// keyed on Root() is a tracked optimization (POS-3574), not yet implemented. A
// nil *Snapshot classifies nothing (no registry / non-bor chain), so all methods
// are nil-safe.
type Snapshot struct {
	root     common.Hash
	capacity uint64
	clients  map[common.Address]ClientLookup
}

// BuildSnapshot reads the full active reserved set from the registry at the
// given block state and returns an immutable Snapshot. Returns nil (no error)
// when no registry is configured. Each call does one Root() read plus a
// whitelist scan and a per-address lookup; a Root()-keyed cache that skips the
// rebuild while the root is unchanged is a tracked optimization (POS-3574).
func BuildSnapshot(r Reader, statedb *state.StateDB, number uint64, hash common.Hash) (*Snapshot, error) {
	if r == nil || !r.HasReservedRegistry() {
		return nil, nil
	}
	// The registry reads run through the EVM (ApplyMessage), which mutates the
	// statedb it executes against — gas accounting, sender nonce, touched/created
	// accounts, revert-journal residue. On the block execution path the caller
	// passes the live execution state, so reading against it would leak those
	// mutations into the block and diverge the state root across producer and
	// validator (a consensus split). Read against a throwaway copy so a snapshot
	// build is always state-neutral.
	if statedb != nil {
		statedb = statedb.Copy()
	}
	root, err := r.Root(statedb, number, hash)
	if err != nil {
		return nil, err
	}
	addrs, err := r.WhitelistedAddresses(statedb, number, hash)
	if err != nil {
		return nil, err
	}
	capacity, err := r.TotalReservedGas(statedb, number, hash)
	if err != nil {
		return nil, err
	}
	clients := make(map[common.Address]ClientLookup, len(addrs))
	for _, a := range addrs {
		c, err := r.ReservedClientForAddress(statedb, number, hash, a)
		if err != nil {
			return nil, err
		}
		clients[a] = c
	}
	return &Snapshot{root: root, capacity: capacity, clients: clients}, nil
}

// NewSnapshot constructs a Snapshot from an explicit client set. Used by tests
// and by callers that source the reserved set outside the registry contract.
func NewSnapshot(root common.Hash, capacity uint64, clients map[common.Address]ClientLookup) *Snapshot {
	return &Snapshot{root: root, capacity: capacity, clients: clients}
}

// Root is the registry root this snapshot was built at; callers reuse the
// snapshot while the live root is unchanged.
func (s *Snapshot) Root() common.Hash {
	if s == nil {
		return common.Hash{}
	}
	return s.root
}

// IsReserved reports whether account is an active reserved sender effective at
// the given block number (active && effectiveFrom <= number).
func (s *Snapshot) IsReserved(account common.Address, number uint64) bool {
	if s == nil {
		return false
	}
	c, ok := s.clients[account]
	return ok && c.Active && c.EffectiveFrom <= number
}

// FeeMode returns the fee mode of account's client (0 = free) or 0 if not reserved.
func (s *Snapshot) FeeMode(account common.Address) uint8 {
	if s == nil {
		return 0
	}
	return s.clients[account].FeeMode
}

// Capacity returns the reserved capacity (sum of active client quotas).
func (s *Snapshot) Capacity() uint64 {
	if s == nil {
		return 0
	}
	return s.capacity
}
