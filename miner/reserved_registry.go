package miner

import (
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

// reservedRegistry is the worker's local view of the reserved-blockspace
// registry. Production wiring leaves the worker's registry nil; tests inject a
// *MockRegistry via SetReservedRegistry. The contract-backed implementation owned by
// the registry-module work lands behind the same shape later — a swap at the
// setter site.
type reservedRegistry interface {
	// Lookup returns the client ID that owns addr, or (_, false) if addr is
	// not registered. Only active clients are visible, and a sender belongs
	// to at most one client — uniqueness is the implementation's invariant
	// (MockRegistry panics on construction; the registry contract enforces it).
	Lookup(addr common.Address) (clientID uint64, ok bool)
	// Quota returns the per-block reserved-region gas allowance for clientID,
	// measured against declared transaction gas limits (tx.Gas), not actual
	// gas used.
	Quota(clientID uint64) uint64
	// CeilingGas returns the global reserved-region gas cap across all
	// clients (the registry contract's ceilingGas). Zero means uncapped.
	CeilingGas() uint64
	// Clients returns every registered client ID.
	Clients() []uint64
	// Snapshot returns an immutable copy of the registry state effective for
	// the child block of parent. The contract-backed implementation resolves
	// parent state and the effectiveFrom activation delay internally; callers
	// pin one snapshot per build so every pass sees a consistent view.
	Snapshot(parent common.Hash) reservedRegistry
}

// ReservedRegistry is the exported alias callers use with
// Miner.SetReservedRegistry.
type ReservedRegistry = reservedRegistry

// ReservedClient describes one reserved-blockspace client for NewMockRegistry. Each whitelisted
// sender belongs to exactly one client.
type ReservedClient struct {
	ID       uint64
	Senders  []common.Address
	QuotaGas uint64
}

// MockRegistry is a throwaway in-memory reservedRegistry for tests and as a placeholder
// until the registry module lands. Immutable after construction; lock-free.
type MockRegistry struct {
	addrToClient  map[common.Address]uint64
	clientToQuota map[uint64]uint64
	clientIDs     []uint64
	ceiling       uint64
}

// NewMockRegistry builds a MockRegistry. Panics on a duplicate client ID or a sender claimed by
// two clients — both are test-setup bugs, not runtime conditions.
func NewMockRegistry(clients []ReservedClient) *MockRegistry {
	m := &MockRegistry{
		addrToClient:  make(map[common.Address]uint64),
		clientToQuota: make(map[uint64]uint64),
		clientIDs:     make([]uint64, 0, len(clients)),
	}
	for _, c := range clients {
		if _, dup := m.clientToQuota[c.ID]; dup {
			panic(fmt.Sprintf("miner.NewMockRegistry: duplicate client ID %d", c.ID))
		}
		m.clientToQuota[c.ID] = c.QuotaGas
		m.clientIDs = append(m.clientIDs, c.ID)
		for _, addr := range c.Senders {
			if existing, ok := m.addrToClient[addr]; ok {
				panic(fmt.Sprintf("miner.NewMockRegistry: address %s belongs to clients %d and %d", addr.Hex(), existing, c.ID))
			}
			m.addrToClient[addr] = c.ID
		}
	}
	sort.Slice(m.clientIDs, func(i, j int) bool { return m.clientIDs[i] < m.clientIDs[j] })
	return m
}

// WithCeiling sets the global reserved-region gas cap (zero = uncapped) and
// returns m for construction chaining. Set before handing the MockRegistry to a
// worker; MockRegistry is treated as immutable once in use.
func (m *MockRegistry) WithCeiling(gas uint64) *MockRegistry {
	m.ceiling = gas
	return m
}

func (m *MockRegistry) Lookup(addr common.Address) (uint64, bool) {
	cid, ok := m.addrToClient[addr]
	return cid, ok
}

func (m *MockRegistry) Quota(clientID uint64) uint64 { return m.clientToQuota[clientID] }

func (m *MockRegistry) CeilingGas() uint64 { return m.ceiling }

func (m *MockRegistry) Clients() []uint64 {
	out := make([]uint64, len(m.clientIDs))
	copy(out, m.clientIDs)
	return out
}

func (m *MockRegistry) Snapshot(common.Hash) reservedRegistry {
	// The MockRegistry has no chain state to resolve, so the parent hash is unused;
	// the copy below just guarantees snapshot isolation.
	snapshot := *m
	snapshot.addrToClient = make(map[common.Address]uint64, len(m.addrToClient))
	for k, v := range m.addrToClient {
		snapshot.addrToClient[k] = v
	}
	snapshot.clientToQuota = make(map[uint64]uint64, len(m.clientToQuota))
	for k, v := range m.clientToQuota {
		snapshot.clientToQuota[k] = v
	}
	snapshot.clientIDs = make([]uint64, len(m.clientIDs))
	copy(snapshot.clientIDs, m.clientIDs)
	return &snapshot
}
