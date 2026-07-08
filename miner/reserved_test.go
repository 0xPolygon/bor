package miner

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

// Compile-time guard: Mock must satisfy the exported registry interface so the
// Miner.SetReservedRegistry wiring can't silently break.
var _ ReservedRegistry = (*Mock)(nil)

func TestOrderClients(t *testing.T) {
	t.Parallel()

	h1 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	h2 := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	cases := []struct {
		name    string
		parent  common.Hash
		ids     []uint64
		wantLen int
	}{
		{name: "empty", parent: h1, ids: nil, wantLen: 0},
		{name: "single", parent: h1, ids: []uint64{42}, wantLen: 1},
		{name: "three", parent: h1, ids: []uint64{1, 2, 3}, wantLen: 3},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := orderClients(tc.parent, tc.ids)
			require.Len(t, got, tc.wantLen)
			require.Equal(t, got, orderClients(tc.parent, tc.ids), "deterministic for fixed inputs")

			seen := make(map[uint64]struct{}, len(got))
			for _, id := range got {
				seen[id] = struct{}{}
			}
			for _, id := range tc.ids {
				_, ok := seen[id]
				require.Truef(t, ok, "id %d missing from output", id)
			}
		})
	}

	require.NotEqual(t,
		orderClients(h1, []uint64{1, 2, 3}),
		orderClients(h2, []uint64{1, 2, 3}),
		"different parent hashes should reorder >2 clients")
}

func TestNewMock(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0xAa")
	b := common.HexToAddress("0xBb")
	c := common.HexToAddress("0xCc")

	m := NewMock([]Client{
		{ID: 7, Senders: []common.Address{a, b}, QuotaGas: 10_000_000},
		{ID: 3, Senders: []common.Address{c}, QuotaGas: 5_000_000},
	})

	cid, ok := m.Lookup(a)
	require.True(t, ok)
	require.Equal(t, uint64(7), cid)

	_, ok = m.Lookup(common.HexToAddress("0xDd"))
	require.False(t, ok)

	require.Equal(t, uint64(10_000_000), m.Quota(7))
	require.Equal(t, uint64(0), m.Quota(99))
	require.Equal(t, []uint64{3, 7}, m.Clients(), "Clients sorted")
}

func TestNewMock_PanicsOnDuplicateSender(t *testing.T) {
	t.Parallel()
	defer func() { require.NotNil(t, recover(), "expected panic on duplicate sender") }()

	addr := common.HexToAddress("0x01")
	_ = NewMock([]Client{
		{ID: 1, Senders: []common.Address{addr}, QuotaGas: 1},
		{ID: 2, Senders: []common.Address{addr}, QuotaGas: 1},
	})
}

// TestMockSnapshot verifies Snapshot returns an independent deep copy: mutating
// the original after snapshotting must not be observable through the snapshot.
func TestMockSnapshot(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x0a")
	m := NewMock([]Client{{ID: 1, Senders: []common.Address{a}, QuotaGas: 100}}).WithCeiling(500)
	snap := m.Snapshot(common.Hash{})

	// Mutate the original's internal maps; the snapshot must be unaffected.
	m.addrToClient[a] = 999
	m.clientToQuota[1] = 0
	m.clientIDs[0] = 999
	m.ceiling = 0

	cid, ok := snap.Lookup(a)
	require.True(t, ok)
	require.Equal(t, uint64(1), cid)
	require.Equal(t, uint64(100), snap.Quota(1))
	require.Equal(t, []uint64{1}, snap.Clients())
	require.Equal(t, uint64(500), snap.CeilingGas())
}

func TestFilterReservedTxs(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x01")
	b := common.HexToAddress("0x02")
	c := common.HexToAddress("0x03")
	other := common.HexToAddress("0xFFFF")

	cases := []struct {
		name          string
		registry      *Mock
		input         map[common.Address][]*txpool.LazyTransaction
		wantClients   map[uint64][]common.Address
		wantRemaining []common.Address
	}{
		{
			name:          "no reserved senders present",
			registry:      NewMock([]Client{{ID: 1, Senders: []common.Address{common.HexToAddress("0x99")}, QuotaGas: 1}}),
			input:         map[common.Address][]*txpool.LazyTransaction{a: stubTxs(1)},
			wantRemaining: []common.Address{a},
		},
		{
			name: "filters reserved senders out",
			registry: NewMock([]Client{
				{ID: 7, Senders: []common.Address{a, b}, QuotaGas: 1_000_000},
				{ID: 3, Senders: []common.Address{c}, QuotaGas: 500_000},
			}),
			input: map[common.Address][]*txpool.LazyTransaction{
				a: stubTxs(2), b: stubTxs(1), c: stubTxs(3), other: stubTxs(1),
			},
			wantClients:   map[uint64][]common.Address{7: {a, b}, 3: {c}},
			wantRemaining: []common.Address{other},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			perClient := filterReservedTxs(tc.input, tc.registry)

			require.Len(t, perClient, len(tc.wantClients))
			for cid, senders := range tc.wantClients {
				got, ok := perClient[cid]
				require.Truef(t, ok, "client %d missing", cid)
				require.Len(t, got, len(senders))
				for _, s := range senders {
					require.Contains(t, got, s)
				}
			}

			require.Len(t, tc.input, len(tc.wantRemaining))
			for _, s := range tc.wantRemaining {
				require.Contains(t, tc.input, s)
			}
		})
	}
}

// TestReservedOrdering pins the reserved heap's two departures from the normal
// market ordering: it pops ascending effective tip (zero-/below-base-fee first)
// and it never drops a below-base-fee transaction. Per-sender nonce order is
// honoured.
func TestReservedOrdering(t *testing.T) {
	t.Parallel()

	baseFee := big.NewInt(100)
	a := common.HexToAddress("0x0a")
	b := common.HexToAddress("0x0b")

	// a: nonce0 fallback-fee (tip 50), nonce1 fallback-fee (tip 60).
	// b: nonce0 zero-fee (below base fee), nonce1 fallback-fee (tip 10).
	txs := map[common.Address][]*txpool.LazyTransaction{
		a: {feeTx(0, 150, 50), feeTx(1, 160, 60)},
		b: {feeTx(0, 0, 0), feeTx(1, 110, 10)},
	}

	h := newReservedTransactionsByNonce(nil, txs, baseFee, nil)

	// Nothing dropped: 4 txs across 2 senders must all be present.
	var order []common.Address
	for {
		ltx, _ := h.Peek()
		if ltx == nil {
			break
		}
		from, _ := h.PeekFrom()
		order = append(order, from)
		h.Shift()
	}
	require.Len(t, order, 4, "no below-base-fee tx should be dropped")

	// b nonce0 (tip 0) pops first (ascending). b nonce1 (tip 10) next. Then a's
	// nonce0 (tip 50), a nonce1 (tip 60). Per-sender nonce order holds.
	require.Equal(t, []common.Address{b, b, a, a}, order)
}

// TestSelectReservedTxs exercises the per-client quota selection: ascending-fee
// preference (zero-fee wins scarce quota over fallback-fee), per-sender nonce
// contiguity on quota breach, and gas-limit-based accounting.
func TestSelectReservedTxs(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x0a")
	b := common.HexToAddress("0x0b")

	cases := []struct {
		name         string
		baseFee      *big.Int
		pending      map[common.Address][]*txpool.LazyTransaction
		quota        uint64
		wantSelected map[common.Address]int
		wantUsed     uint64
		wantOverflow map[common.Address]int
	}{
		{
			name:         "all fit",
			baseFee:      nil,
			pending:      map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}},
			quota:        1000,
			wantSelected: map[common.Address]int{a: 2},
			wantUsed:     200,
			wantOverflow: map[common.Address]int{},
		},
		{
			name:         "breach diverts the sender's remaining nonces",
			baseFee:      nil,
			pending:      map[common.Address][]*txpool.LazyTransaction{a: {gasTx(600), gasTx(600), gasTx(600)}},
			quota:        1000,
			wantSelected: map[common.Address]int{a: 1},
			wantUsed:     600,
			wantOverflow: map[common.Address]int{a: 2},
		},
		{
			name:    "ascending-fee preference: zero-fee wins scarce quota",
			baseFee: big.NewInt(100),
			pending: map[common.Address][]*txpool.LazyTransaction{
				a: {feeGasTx(150, 50, 100)}, // fallback-fee, tip 50
				b: {feeGasTx(0, 0, 100)},    // zero-fee (below base fee)
			},
			quota:        100, // room for exactly one
			wantSelected: map[common.Address]int{b: 1},
			wantUsed:     100,
			wantOverflow: map[common.Address]int{a: 1},
		},
		{
			name:         "zero quota: everything overflows",
			baseFee:      nil,
			pending:      map[common.Address][]*txpool.LazyTransaction{a: {gasTx(1)}},
			quota:        0,
			wantSelected: map[common.Address]int{},
			wantUsed:     0,
			wantOverflow: map[common.Address]int{a: 1},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn := reservedCtor(tc.baseFee)

			selected, used, overflow := selectReservedTxs(tc.pending, tc.quota, fn)
			require.Equal(t, tc.wantUsed, used, "selected declared gas")

			gotSelected := drainBySender(selected)
			require.Len(t, gotSelected, len(tc.wantSelected))
			for addr, n := range tc.wantSelected {
				require.Equalf(t, n, gotSelected[addr], "selected[%s]", addr.Hex())
			}
			gotOverflow := 0
			for _, txs := range overflow {
				gotOverflow += len(txs)
			}
			wantOverflow := 0
			for addr, n := range tc.wantOverflow {
				require.Lenf(t, overflow[addr], n, "overflow[%s]", addr.Hex())
				wantOverflow += n
			}
			require.Equal(t, wantOverflow, gotOverflow)
		})
	}
}

// drainBySender pops a reserved ordering and counts transactions per sender.
func drainBySender(h *transactionsByPriceAndNonce) map[common.Address]int {
	out := make(map[common.Address]int)
	for {
		ltx, _ := h.Peek()
		if ltx == nil {
			break
		}
		from, _ := h.PeekFrom()
		out[from]++
		h.Shift()
	}
	return out
}

// waitForBlockWithTxs blocks until a mined block carrying at least minTxs
// transactions is observed, or fails after timeout.
func waitForBlockWithTxs(t *testing.T, sub *event.TypeMuxSubscription, minTxs int, timeout time.Duration) *types.Block {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-sub.Chan():
			blk := ev.Data.(core.NewMinedBlockEvent).Block
			if len(blk.Transactions()) >= minTxs {
				return blk
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a block with >= %d txs", minTxs)
			return nil
		}
	}
}

// stubTxs builds n minimal LazyTransaction handles. filterReservedTxs only
// inspects the map shape (keyed by sender), so the entries can be minimal.
func stubTxs(n int) []*txpool.LazyTransaction {
	out := make([]*txpool.LazyTransaction, n)
	for i := 0; i < n; i++ {
		out[i] = &txpool.LazyTransaction{Hash: common.Hash{byte(i)}}
	}
	return out
}

// feeTx builds a LazyTransaction with the given (positional) nonce, fee cap and
// tip cap for ordering tests. The slice position is the effective nonce; the
// nonce argument only seeds Hash/Time for determinism.
func feeTx(nonce, feeCap, tipCap uint64) *txpool.LazyTransaction {
	return &txpool.LazyTransaction{
		Hash:      common.Hash{byte(nonce)},
		Time:      time.Unix(0, int64(nonce)),
		GasFeeCap: uint256.NewInt(feeCap),
		GasTipCap: uint256.NewInt(tipCap),
		Gas:       21000,
	}
}

// gasTx builds a zero-fee LazyTransaction with the given gas limit, for quota
// selection tests that don't care about fee ordering.
func gasTx(gas uint64) *txpool.LazyTransaction {
	return &txpool.LazyTransaction{
		GasFeeCap: uint256.NewInt(0),
		GasTipCap: uint256.NewInt(0),
		Gas:       gas,
	}
}

// feeGasTx builds a LazyTransaction with explicit fee cap, tip cap and gas
// limit, for quota tests that exercise the ascending-fee preference.
func feeGasTx(feeCap, tipCap, gas uint64) *txpool.LazyTransaction {
	return &txpool.LazyTransaction{
		GasFeeCap: uint256.NewInt(feeCap),
		GasTipCap: uint256.NewInt(tipCap),
		Gas:       gas,
	}
}

// reservedCtor returns a reserved-ordering constructor bound to baseFee, for
// unit tests that exercise sequencing/selection without a full worker/env.
func reservedCtor(baseFee *big.Int) newTransactionsByPriceAndNonceFn {
	return func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce {
		return newReservedTransactionsByNonce(nil, txs, baseFee, nil)
	}
}

// TestExtractReservedTxs covers the worker-level reserved extraction: nil and
// empty registries are no-ops; a populated registry yields one ordered group
// per client and re-adds quota overflow to the pending map.
func TestExtractReservedTxs(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x0a")
	n := common.HexToAddress("0x0e")
	fn := reservedCtor(nil)

	t.Run("nil registry is a no-op", func(t *testing.T) {
		t.Parallel()
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100)}}
		require.Nil(t, extractReservedTxs(nil, common.Hash{}, pending, fn))
		require.Contains(t, pending, a, "pending untouched")
	})

	t.Run("empty registry is a no-op", func(t *testing.T) {
		t.Parallel()
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100)}}
		require.Nil(t, extractReservedTxs(NewMock(nil), common.Hash{}, pending, fn))
		require.Contains(t, pending, a, "pending untouched")
	})

	t.Run("client within quota; normal sender untouched", func(t *testing.T) {
		t.Parallel()
		registry := NewMock([]Client{{ID: 1, Senders: []common.Address{a}, QuotaGas: 1_000_000}})
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}, n: {gasTx(100)}}

		groups := extractReservedTxs(registry, common.Hash{}, pending, fn)
		require.Len(t, groups, 1)
		require.Equal(t, map[common.Address]int{a: 2}, drainBySender(groups[0]))

		require.NotContains(t, pending, a, "reserved sender removed from pending")
		require.Contains(t, pending, n, "normal sender retained")
	})

	t.Run("quota overflow re-added to pending", func(t *testing.T) {
		t.Parallel()
		registry := NewMock([]Client{{ID: 1, Senders: []common.Address{a}, QuotaGas: 100}})
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}}

		groups := extractReservedTxs(registry, common.Hash{}, pending, fn)
		require.Len(t, groups, 1)
		require.Equal(t, map[common.Address]int{a: 1}, drainBySender(groups[0]))
		require.Len(t, pending[a], 1, "overflow tx re-added to pending for the normal pass")
	})
}

// TestExtractReservedTxs_Ceiling exercises the global reserved cap: the summed
// declared gas selected across clients must not exceed the registry's
// ceilingGas, with the excess diverted to the normal pass; zero means uncapped.
func TestExtractReservedTxs_Ceiling(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x0a") // client 1's sender
	b := common.HexToAddress("0x0b") // client 2's sender
	senderOf := map[uint64]common.Address{1: a, 2: b}
	fn := reservedCtor(nil)

	newPending := func() map[common.Address][]*txpool.LazyTransaction {
		return map[common.Address][]*txpool.LazyTransaction{
			a: {gasTx(300), gasTx(300)},
			b: {gasTx(300), gasTx(300)},
		}
	}

	t.Run("ceiling caps the second client in visit order", func(t *testing.T) {
		t.Parallel()
		registry := NewMock([]Client{
			{ID: 1, Senders: []common.Address{a}, QuotaGas: 600},
			{ID: 2, Senders: []common.Address{b}, QuotaGas: 600},
		}).WithCeiling(800)
		pending := newPending()

		// The first-visited client fits its full 600; the second is left
		// min(600, 200) = 200, so neither of its 300-gas txs fits.
		order := orderClients(common.Hash{}, []uint64{1, 2})
		first, second := senderOf[order[0]], senderOf[order[1]]

		groups := extractReservedTxs(registry, common.Hash{}, pending, fn)
		require.Len(t, groups, 1, "second client fully diverted by the ceiling")
		require.Equal(t, map[common.Address]int{first: 2}, drainBySender(groups[0]))
		require.Len(t, pending[second], 2, "ceiling overflow re-added to pending")
	})

	t.Run("zero ceiling means uncapped", func(t *testing.T) {
		t.Parallel()
		registry := NewMock([]Client{
			{ID: 1, Senders: []common.Address{a}, QuotaGas: 600},
			{ID: 2, Senders: []common.Address{b}, QuotaGas: 600},
		})
		pending := newPending()

		groups := extractReservedTxs(registry, common.Hash{}, pending, fn)
		require.Len(t, groups, 2, "both clients fully served")
		require.Empty(t, pending, "nothing diverted")
	})
}

// TestSelectReservedTxs_Interrupt pins the interrupt behavior: when block
// building is already interrupted, the scan heap constructor yields an empty
// heap, so the client contributes nothing and nothing lands in overflow — the
// transactions simply stay in the pool for a later block.
func TestSelectReservedTxs_Interrupt(t *testing.T) {
	t.Parallel()

	interrupted := new(atomic.Bool)
	interrupted.Store(true)
	fn := func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce {
		return newReservedTransactionsByNonce(nil, txs, nil, interrupted)
	}

	a := common.HexToAddress("0x0a")
	pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}}

	selected, used, overflow := selectReservedTxs(pending, 1_000, fn)
	require.True(t, selected.Empty(), "interrupted scan selects nothing")
	require.Zero(t, used)
	require.Empty(t, overflow, "nothing diverted to the normal pass")
}

// TestClonePreservesReservedOrdering guards the sendPlan/prefetch path: cloning
// a reserved heap must keep the ascending, never-drop ordering so the plan the
// prefetcher sees matches what commitTransactions will execute.
func TestClonePreservesReservedOrdering(t *testing.T) {
	t.Parallel()

	baseFee := big.NewInt(100)
	a := common.HexToAddress("0x0a")
	b := common.HexToAddress("0x0b")
	txs := map[common.Address][]*txpool.LazyTransaction{
		a: {feeTx(0, 150, 50)}, // fallback-fee
		b: {feeTx(1, 0, 0)},    // zero-fee
	}

	clone := newReservedTransactionsByNonce(nil, txs, baseFee, nil).clone()
	require.True(t, clone.reserved, "clone keeps the reserved flag")
	require.True(t, clone.heads.ascending, "clone keeps ascending ordering")

	// Zero-fee still pops before fallback-fee on the clone.
	from, ok := clone.PeekFrom()
	require.True(t, ok)
	require.Equal(t, b, from)
}

// TestSequenceTxs validates the full grouping: priority first, then reserved
// clients, then normal; empty groups are omitted; reserved overflow lands in
// the normal group.
func TestSequenceTxs(t *testing.T) {
	t.Parallel()

	p := common.HexToAddress("0x0a") // priority
	r := common.HexToAddress("0x0b") // reserved
	n := common.HexToAddress("0x0c") // normal

	newEnv := func() *environment {
		return &environment{header: &types.Header{Number: big.NewInt(1), BaseFee: nil}}
	}

	t.Run("priority, reserved and normal groups in order", func(t *testing.T) {
		t.Parallel()
		w := &worker{prio: []common.Address{p}, reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 1_000_000}})}
		pending := map[common.Address][]*txpool.LazyTransaction{p: {gasTx(100)}, r: {gasTx(100)}, n: {gasTx(100)}}

		seq := w.sequenceTxs(newEnv(), w.reservedRegistrySnapshot(common.Hash{}), pending)
		require.Len(t, seq, 3)
		require.Equal(t, map[common.Address]int{p: 1}, drainBySender(seq[0]), "priority group first")
		require.Equal(t, map[common.Address]int{r: 1}, drainBySender(seq[1]), "reserved group next")
		require.Equal(t, map[common.Address]int{n: 1}, drainBySender(seq[2]), "normal group last")
	})

	t.Run("no prio, no registry: single normal group", func(t *testing.T) {
		t.Parallel()
		w := &worker{}
		pending := map[common.Address][]*txpool.LazyTransaction{n: {gasTx(100)}}

		seq := w.sequenceTxs(newEnv(), w.reservedRegistrySnapshot(common.Hash{}), pending)
		require.Len(t, seq, 1)
		require.Equal(t, map[common.Address]int{n: 1}, drainBySender(seq[0]))
	})

	t.Run("empty pending yields empty sequence", func(t *testing.T) {
		t.Parallel()
		w := &worker{prio: []common.Address{p}, reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 100}})}
		seq := w.sequenceTxs(newEnv(), w.reservedRegistrySnapshot(common.Hash{}), map[common.Address][]*txpool.LazyTransaction{})
		require.Empty(t, seq)
	})

	t.Run("reserved overflow joins the normal group", func(t *testing.T) {
		t.Parallel()
		w := &worker{reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 100}})}
		pending := map[common.Address][]*txpool.LazyTransaction{r: {gasTx(100), gasTx(100)}, n: {gasTx(100)}}

		seq := w.sequenceTxs(newEnv(), w.reservedRegistrySnapshot(common.Hash{}), pending)
		require.Len(t, seq, 2)
		require.Equal(t, map[common.Address]int{r: 1}, drainBySender(seq[0]), "reserved group: one tx fits quota")
		require.Equal(t, map[common.Address]int{n: 1, r: 1}, drainBySender(seq[1]), "normal group: normal sender + reserved overflow")
	})

	t.Run("multiple clients yield one group each in deterministic order", func(t *testing.T) {
		t.Parallel()
		r2 := common.HexToAddress("0x0d")
		senderOf := map[uint64]common.Address{1: r, 2: r2}
		w := &worker{reservedRegistry: NewMock([]Client{
			{ID: 1, Senders: []common.Address{r}, QuotaGas: 1_000_000},
			{ID: 2, Senders: []common.Address{r2}, QuotaGas: 1_000_000},
		})}
		pending := map[common.Address][]*txpool.LazyTransaction{r: {gasTx(100)}, r2: {gasTx(100)}, n: {gasTx(100)}}

		env := newEnv()
		order := orderClients(env.header.ParentHash, []uint64{1, 2})

		seq := w.sequenceTxs(env, w.reservedRegistrySnapshot(env.header.ParentHash), pending)
		require.Len(t, seq, 3)
		require.Equal(t, map[common.Address]int{senderOf[order[0]]: 1}, drainBySender(seq[0]), "first client in visit order")
		require.Equal(t, map[common.Address]int{senderOf[order[1]]: 1}, drainBySender(seq[1]), "second client in visit order")
		require.Equal(t, map[common.Address]int{n: 1}, drainBySender(seq[2]), "normal group last")
	})

	t.Run("below-base-fee overflow is dropped from the normal group", func(t *testing.T) {
		t.Parallel()
		// Quota fits one of the two zero-fee txs. The overflow tx re-enters
		// the normal pass, where standard EIP-1559 admission drops it
		// (GasFeeCap < BaseFee) — per spec it stays in the pool for a later
		// block instead of entering this one.
		w := &worker{reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 100}})}
		pending := map[common.Address][]*txpool.LazyTransaction{
			r: {feeGasTx(0, 0, 100), feeGasTx(0, 0, 100)},
			n: {feeGasTx(200, 100, 100)},
		}

		env := newEnv()
		env.header.BaseFee = big.NewInt(100)

		seq := w.sequenceTxs(env, w.reservedRegistrySnapshot(env.header.ParentHash), pending)
		require.Len(t, seq, 2)
		require.Equal(t, map[common.Address]int{r: 1}, drainBySender(seq[0]), "reserved group: one zero-fee tx within quota")
		require.Equal(t, map[common.Address]int{n: 1}, drainBySender(seq[1]), "normal group: zero-fee overflow not admitted")
	})

	t.Run("sender in both prio and registry is consumed by the priority pass", func(t *testing.T) {
		t.Parallel()
		// Pins the documented builder preference: the priority pass runs
		// first, so a prioritized registered sender bypasses reserved quota
		// accounting and pays normal fees. Operators should not prioritize
		// registered senders.
		w := &worker{prio: []common.Address{r}, reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 100}})}
		pending := map[common.Address][]*txpool.LazyTransaction{r: {gasTx(100), gasTx(100)}}

		seq := w.sequenceTxs(newEnv(), w.reservedRegistrySnapshot(common.Hash{}), pending)
		require.Len(t, seq, 1, "single priority group; no reserved group")
		require.False(t, seq[0].reserved, "priority group uses normal ordering")
		require.Equal(t, map[common.Address]int{r: 2}, drainBySender(seq[0]), "both txs taken despite quota of 100")
	})
}

// TestReservedBuild_NilRegistry confirms that with no registry wired (production
// default) the worker still builds a block including the submitted tx.
func TestReservedBuild_NilRegistry(t *testing.T) {
	chainConfig := *params.BorUnittestChainConfig
	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	w, b, _ := newTestWorker(t, DefaultTestConfig(), &chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)
	defer w.close()

	require.Nil(t, w.reservedRegistry, "registry defaults to nil")

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()
	w.start()

	require.NoError(t, b.txPool.Add([]*types.Transaction{b.newRandomTxWithNonce(false, 0)}, false)[0])

	// The worker may seal an empty pre-commit block first; wait for the one
	// carrying the transaction.
	waitForBlockWithTxs(t, sub, 1, 5*time.Second)
}

// TestReservedBuild_HappyPath registers the test bank as a reserved client and
// confirms its transactions are included end-to-end through the wired sequencing
// path. (Single funded sender, so positional reserved/normal distinction is
// covered by the unit tests above, not here.)
func TestReservedBuild_HappyPath(t *testing.T) {
	chainConfig := *params.BorUnittestChainConfig
	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	w, b, _ := newTestWorker(t, DefaultTestConfig(), &chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)
	defer w.close()

	w.setReservedRegistry(NewMock([]Client{
		{ID: 1, Senders: []common.Address{testBankAddress}, QuotaGas: 10_000_000},
	}))

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	errs := b.txPool.Add([]*types.Transaction{
		b.newRandomTxWithNonce(false, 0),
		b.newRandomTxWithNonce(false, 1),
	}, false)
	for _, err := range errs {
		require.NoError(t, err)
	}
	w.start()

	got := waitForBlockWithTxs(t, sub, 2, 15*time.Second)

	signer := types.LatestSigner(&chainConfig)
	for i, tx := range got.Transactions() {
		from, err := types.Sender(signer, tx)
		require.NoError(t, err)
		require.Equalf(t, testBankAddress, from, "tx %d should be from the reserved sender", i)
	}
}

// borUnittestCancunConfig returns a copy of BorUnittestChainConfig with Shanghai
// and Cancun active from genesis. BlockExtraData (and therefore the
// ReservedGasUsed header field) is only RLP-encoded into Header.Extra
// post-Cancun, which BorUnittestChainConfig doesn't reach.
func borUnittestCancunConfig() params.ChainConfig {
	chainConfig := *params.BorUnittestChainConfig
	chainConfig.ShanghaiBlock = big.NewInt(0)
	chainConfig.CancunBlock = big.NewInt(0)
	return chainConfig
}

// TestReservedBuild_HeaderGasUsed confirms the producer records the reserved
// pass's actual gas total in BlockExtraData.ReservedGasUsed. With every block
// transaction committed through the reserved group, the total must equal the
// header's GasUsed.
func TestReservedBuild_HeaderGasUsed(t *testing.T) {
	chainConfig := borUnittestCancunConfig()
	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	w, b, _ := newTestWorker(t, DefaultTestConfig(), &chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)
	defer w.close()

	w.setReservedRegistry(NewMock([]Client{
		{ID: 1, Senders: []common.Address{testBankAddress}, QuotaGas: 10_000_000},
	}))

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	errs := b.txPool.Add([]*types.Transaction{
		b.newRandomTxWithNonce(false, 0),
		b.newRandomTxWithNonce(false, 1),
	}, false)
	for _, err := range errs {
		require.NoError(t, err)
	}
	w.start()

	got := waitForBlockWithTxs(t, sub, 2, 15*time.Second)

	reserved := got.Header().GetReservedGasUsed(&chainConfig)
	require.NotNil(t, reserved, "reserved pass active: header must carry ReservedGasUsed")
	require.Equal(t, got.GasUsed(), *reserved, "all txs are reserved, so reserved gas equals block gas used")
}

// TestReservedBuild_HeaderAbsentWithoutRegistry pins wire compatibility: with no
// registry wired (production default), post-Cancun blocks must not carry the
// ReservedGasUsed field at all — their Extra encoding stays byte-identical to
// pre-reserved-blockspace blocks.
func TestReservedBuild_HeaderAbsentWithoutRegistry(t *testing.T) {
	chainConfig := borUnittestCancunConfig()
	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	w, b, _ := newTestWorker(t, DefaultTestConfig(), &chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)
	defer w.close()

	require.Nil(t, w.reservedRegistry, "registry defaults to nil")

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()
	w.start()

	require.NoError(t, b.txPool.Add([]*types.Transaction{b.newRandomTxWithNonce(false, 0)}, false)[0])

	got := waitForBlockWithTxs(t, sub, 1, 15*time.Second)
	require.Nil(t, got.Header().GetReservedGasUsed(&chainConfig), "no registry: field must be absent from the wire")
}

// TestReservedBuild_Positional proves the reserved-first builder preference
// end-to-end with two funded senders: the registered sender's lower-priced
// transaction must precede an unregistered sender's higher-priced one, which is
// the opposite of pure price ordering.
func TestReservedBuild_Positional(t *testing.T) {
	chainConfig := *params.BorUnittestChainConfig
	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	w, b, _ := newTestWorker(t, DefaultTestConfig(), &chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)
	defer w.close()

	w.setReservedRegistry(NewMock([]Client{
		{ID: 1, Senders: []common.Address{testUserAddress}, QuotaGas: 10_000_000},
	}))

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()
	w.start()

	// The shared genesis funds only the bank, so give the reserved sender a
	// balance first and wait for that transfer to mine.
	fund, err := types.SignTx(
		types.NewTransaction(0, testUserAddress, big.NewInt(100_000_000_000_000_000), params.TxGas, big.NewInt(30*params.InitialBaseFee), nil),
		types.HomesteadSigner{}, testBankKey)
	require.NoError(t, err)
	require.NoError(t, b.txPool.Add([]*types.Transaction{fund}, false)[0])
	waitForBlockWithTxs(t, sub, 1, 15*time.Second)

	// Pause sealing while both contenders are admitted — with one-second
	// blocks, sequential adds can otherwise split them across two blocks and
	// void the positional comparison.
	w.stop()

	// Reserved sender pays 26 gwei, normal sender 100 gwei: price ordering
	// would put the bank first, reserved sequencing must not.
	userTx, err := types.SignTx(
		types.NewTransaction(0, testBankAddress, big.NewInt(1000), params.TxGas, big.NewInt(26*params.InitialBaseFee), nil),
		types.HomesteadSigner{}, testUserKey)
	require.NoError(t, err)
	bankTx, err := types.SignTx(
		types.NewTransaction(1, testUserAddress, big.NewInt(1000), params.TxGas, big.NewInt(100*params.InitialBaseFee), nil),
		types.HomesteadSigner{}, testBankKey)
	require.NoError(t, err)

	// The pool resets asynchronously from chain-head events, so the user's
	// funding may not be visible to it yet — retry admission until it is.
	require.Eventually(t, func() bool {
		return b.txPool.Add([]*types.Transaction{userTx}, false)[0] == nil
	}, 10*time.Second, 100*time.Millisecond, "user tx not admitted after funding")
	require.NoError(t, b.txPool.Add([]*types.Transaction{bankTx}, false)[0])
	w.start()

	got := waitForBlockWithTxs(t, sub, 2, 15*time.Second)

	pos := make(map[common.Hash]int, len(got.Transactions()))
	for i, tx := range got.Transactions() {
		pos[tx.Hash()] = i
	}
	userPos, ok := pos[userTx.Hash()]
	require.True(t, ok, "reserved tx missing from block")
	bankPos, ok := pos[bankTx.Hash()]
	require.True(t, ok, "normal tx missing from block")
	require.Less(t, userPos, bankPos, "reserved sender's cheaper tx must precede the normal sender's pricier tx")
}

// TestReservedBuild_Overflow sets a quota that fits only one tx and confirms the
// second (quota overflow) tx still lands in the block via the normal group.
func TestReservedBuild_Overflow(t *testing.T) {
	chainConfig := *params.BorUnittestChainConfig
	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	w, b, _ := newTestWorker(t, DefaultTestConfig(), &chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)
	defer w.close()

	// Quota fits exactly one value-transfer (gas limit params.TxGas = 21000).
	w.setReservedRegistry(NewMock([]Client{
		{ID: 1, Senders: []common.Address{testBankAddress}, QuotaGas: params.TxGas},
	}))

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	errs := b.txPool.Add([]*types.Transaction{
		b.newRandomTxWithNonce(false, 0),
		b.newRandomTxWithNonce(false, 1),
	}, false)
	for _, err := range errs {
		require.NoError(t, err)
	}
	w.start()

	got := waitForBlockWithTxs(t, sub, 2, 15*time.Second)
	// One tx fit the reserved quota; the second overflowed to the normal group.
	// Both are included in the block.
	require.Len(t, got.Transactions(), 2, "both txs included (1 reserved + 1 normal overflow)")
}
