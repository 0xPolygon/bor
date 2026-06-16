package miner

import (
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

// reservedRegistry is the worker's local view of the reserved-blockspace
// registry. Production wiring leaves the worker's registry nil; tests inject a
// *Mock via SetReservedRegistry. The contract-backed implementation owned by
// the registry-module work lands behind the same shape later — a swap at the
// setter site.
type reservedRegistry interface {
	// Lookup returns the client ID that owns addr, or (_, false) if addr is
	// not registered.
	Lookup(addr common.Address) (clientID uint64, ok bool)
	// Quota returns the per-block reserved-region gas allowance for clientID.
	Quota(clientID uint64) uint64
	// Clients returns every registered client ID.
	Clients() []uint64
	// Snapshot returns a copy of the current registry state.
	Snapshot() reservedRegistry
}

// ReservedRegistry is the exported alias callers use with
// Miner.SetReservedRegistry.
type ReservedRegistry = reservedRegistry

// Client describes one reserved-blockspace client for NewMock. Each whitelisted
// sender belongs to exactly one client.
type Client struct {
	ID       uint64
	Senders  []common.Address
	QuotaGas uint64
}

// Mock is a throwaway in-memory reservedRegistry for tests and as a placeholder
// until the registry module lands. Immutable after construction; lock-free.
type Mock struct {
	addrToClient  map[common.Address]uint64
	clientToQuota map[uint64]uint64
	clientIDs     []uint64
}

// NewMock builds a Mock. Panics on a duplicate client ID or a sender claimed by
// two clients — both are test-setup bugs, not runtime conditions.
func NewMock(clients []Client) *Mock {
	m := &Mock{
		addrToClient:  make(map[common.Address]uint64),
		clientToQuota: make(map[uint64]uint64),
		clientIDs:     make([]uint64, 0, len(clients)),
	}
	for _, c := range clients {
		if _, dup := m.clientToQuota[c.ID]; dup {
			panic(fmt.Sprintf("miner.NewMock: duplicate client ID %d", c.ID))
		}
		m.clientToQuota[c.ID] = c.QuotaGas
		m.clientIDs = append(m.clientIDs, c.ID)
		for _, addr := range c.Senders {
			if existing, ok := m.addrToClient[addr]; ok {
				panic(fmt.Sprintf("miner.NewMock: address %s belongs to clients %d and %d", addr.Hex(), existing, c.ID))
			}
			m.addrToClient[addr] = c.ID
		}
	}
	sort.Slice(m.clientIDs, func(i, j int) bool { return m.clientIDs[i] < m.clientIDs[j] })
	return m
}

func (m *Mock) Lookup(addr common.Address) (uint64, bool) {
	cid, ok := m.addrToClient[addr]
	return cid, ok
}

func (m *Mock) Quota(clientID uint64) uint64 { return m.clientToQuota[clientID] }

func (m *Mock) Clients() []uint64 {
	out := make([]uint64, len(m.clientIDs))
	copy(out, m.clientIDs)
	return out
}

func (m *Mock) Snapshot() reservedRegistry {
	// Copy `m` and return
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
