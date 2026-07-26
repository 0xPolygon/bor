// Package registryreader exposes the minimal read-only surface of the reserved
// blockspace registry that filtering modules (txpool, miner, block validator)
// need. It lives in a leaf package so core/, miner/, and core/txpool/ can
// import it without pulling in consensus/bor/contract → consensus/bor/statefull
// → core/, which would form an import cycle.
package registryreader

import (
	"fmt"
	"math/big"
	"slices"

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
	// FeeMode: 0 = free (zero in-protocol fee), 1 = routed (fee credited to the producer).
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
	// Root returns a value that changes whenever the reserved set or its limits
	// change, so a snapshot keyed on it can be reused until it moves.
	Root(state *state.StateDB, number uint64, hash common.Hash) (common.Hash, error)
	// WhitelistedAddresses returns every currently-active reserved address.
	WhitelistedAddresses(state *state.StateDB, number uint64, hash common.Hash) ([]common.Address, error)
	// TotalReservedGas returns the sum of active client quotas (reserved capacity).
	TotalReservedGas(state *state.StateDB, number uint64, hash common.Hash) (uint64, error)
}

// Client is the slim per-sender record a Snapshot stores: just what the hot
// classification and sequencing paths need. Activation state (active,
// effectiveFrom) is resolved at snapshot build time, so it never appears here.
type Client struct {
	// ID is the registry contract's incremental clientId.
	ID uint64
	// GasQuota is the client's per-block reserved gas allowance, charged
	// against declared transaction gas limits.
	GasQuota uint64
	// FeeMode: 0 = free (zero in-protocol fee), 1 = routed (fee credited to
	// the producer; reserved for a future mode, unused today).
	FeeMode uint8
}

// Snapshot is an immutable, pure-lookup view of the reserved set effective for
// one block, so the hot classification paths never do a per-transaction state
// read. Activation (active flag, effectiveFrom delay) is resolved once at
// build time: every stored entry is effective, which is why lookups take no
// block number. The txpool rebuilds it once per head; the execution path once
// per block (gated on the fork height). A nil *Snapshot classifies nothing
// (no registry / non-bor chain), so all methods are nil-safe.
type Snapshot struct {
	root      common.Hash
	capacity  uint64
	byAddress map[common.Address]Client
	clientIDs []uint64
	quotas    map[uint64]uint64
}

// BuildSnapshot reads the full active reserved set from the registry at the
// given block state and returns an immutable Snapshot classifying senders for
// block effectiveAt (clients with effectiveFrom > effectiveAt are excluded).
// Returns nil (no error) when no registry is configured.
func BuildSnapshot(r Reader, statedb *state.StateDB, number uint64, hash common.Hash, effectiveAt uint64) (*Snapshot, error) {
	if r == nil || !r.HasReservedRegistry() {
		return nil, nil
	}
	// Registry reads run through the EVM, which mutates the statedb it executes
	// against. On the execution path the caller passes the live block state, so
	// read against a throwaway copy to keep the build state-neutral — reading
	// against the live state would leak into the block and change the post-state.
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
	clients, err := resolveClients(r, statedb, number, hash, addrs, effectiveAt)
	if err != nil {
		return nil, err
	}
	if err := assertCapacityInvariant(clients, capacity); err != nil {
		return nil, err
	}
	return NewSnapshot(root, capacity, clients), nil
}

// resolveClients reads each whitelisted address's client record and keeps only
// those effective for effectiveAt (active and past their effectiveFrom delay).
func resolveClients(r Reader, statedb *state.StateDB, number uint64, hash common.Hash, addrs []common.Address, effectiveAt uint64) (map[common.Address]Client, error) {
	clients := make(map[common.Address]Client, len(addrs))
	for _, a := range addrs {
		c, err := r.ReservedClientForAddress(statedb, number, hash, a)
		if err != nil {
			return nil, err
		}
		if !c.Active || c.EffectiveFrom > effectiveAt {
			continue
		}
		if c.ClientID == nil || !c.ClientID.IsUint64() {
			return nil, fmt.Errorf("reserved registry returned invalid client id %v for %s", c.ClientID, a)
		}
		clients[a] = Client{ID: c.ClientID.Uint64(), GasQuota: c.GasQuota, FeeMode: c.FeeMode}
	}
	return clients, nil
}

// assertCapacityInvariant fails hard when the sum of the effective clients'
// quotas exceeds capacity. capacity is the contract's totalReservedGas (Σ active
// quotas) and the effective set is a subset of active, so this holds for any
// well-formed registry. Classification depends on it: it enforces per-client
// quota only, with no cross-client ceiling — a violation would let the
// per-client-only rule over-admit reserved gas beyond what the base fee is
// priced against, and signals a broken/incompatible registry.
func assertCapacityInvariant(clients map[common.Address]Client, capacity uint64) error {
	quotaByClient := make(map[uint64]uint64, len(clients))
	for _, c := range clients {
		quotaByClient[c.ID] = c.GasQuota
	}
	var sumQuotas uint64
	for _, q := range quotaByClient {
		sumQuotas += q
	}
	if sumQuotas > capacity {
		return fmt.Errorf("reserved registry invariant violated: Σ effective client quotas %d > capacity %d", sumQuotas, capacity)
	}
	return nil
}

// NewSnapshot constructs a Snapshot from an explicit, already-effective client
// set. Used by tests and by callers that source the reserved set outside the
// registry contract.
func NewSnapshot(root common.Hash, capacity uint64, clients map[common.Address]Client) *Snapshot {
	quotas := make(map[uint64]uint64, len(clients))
	for _, c := range clients {
		quotas[c.ID] = c.GasQuota
	}
	ids := make([]uint64, 0, len(quotas))
	for id := range quotas {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return &Snapshot{root: root, capacity: capacity, byAddress: clients, clientIDs: ids, quotas: quotas}
}

// Root is the registry root this snapshot was built at; callers reuse the
// snapshot while the live root is unchanged.
func (s *Snapshot) Root() common.Hash {
	if s == nil {
		return common.Hash{}
	}
	return s.root
}

// IsReserved reports whether account is a reserved sender in this snapshot's
// effective set.
func (s *Snapshot) IsReserved(account common.Address) bool {
	if s == nil {
		return false
	}
	_, ok := s.byAddress[account]
	return ok
}

// Lookup returns the client ID that owns account, or (_, false) if account is
// not in the effective reserved set.
func (s *Snapshot) Lookup(account common.Address) (uint64, bool) {
	if s == nil {
		return 0, false
	}
	c, ok := s.byAddress[account]
	return c.ID, ok
}

// Quota returns the per-block reserved gas allowance of clientID, or 0 for an
// unknown client.
func (s *Snapshot) Quota(clientID uint64) uint64 {
	if s == nil {
		return 0
	}
	return s.quotas[clientID]
}

// Clients returns the effective client IDs, sorted ascending.
func (s *Snapshot) Clients() []uint64 {
	if s == nil {
		return nil
	}
	return slices.Clone(s.clientIDs)
}

// FeeMode returns the fee mode of account's client (0 = free) or 0 if not reserved.
func (s *Snapshot) FeeMode(account common.Address) uint8 {
	if s == nil {
		return 0
	}
	return s.byAddress[account].FeeMode
}

// Capacity returns the reserved capacity (sum of active client quotas).
func (s *Snapshot) Capacity() uint64 {
	if s == nil {
		return 0
	}
	return s.capacity
}
