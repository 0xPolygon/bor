package miner

import (
	"math/big"
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
	m := NewMock([]Client{{ID: 1, Senders: []common.Address{a}, QuotaGas: 100}})
	snap := m.Snapshot()

	// Mutate the original's internal maps; the snapshot must be unaffected.
	m.addrToClient[a] = 999
	m.clientToQuota[1] = 0
	m.clientIDs[0] = 999

	cid, ok := snap.Lookup(a)
	require.True(t, ok)
	require.Equal(t, uint64(1), cid)
	require.Equal(t, uint64(100), snap.Quota(1))
	require.Equal(t, []uint64{1}, snap.Clients())
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
		wantOverflow map[common.Address]int
	}{
		{
			name:         "all fit",
			baseFee:      nil,
			pending:      map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}},
			quota:        1000,
			wantSelected: map[common.Address]int{a: 2},
			wantOverflow: map[common.Address]int{},
		},
		{
			name:         "breach diverts the sender's remaining nonces",
			baseFee:      nil,
			pending:      map[common.Address][]*txpool.LazyTransaction{a: {gasTx(600), gasTx(600), gasTx(600)}},
			quota:        1000,
			wantSelected: map[common.Address]int{a: 1},
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
			wantOverflow: map[common.Address]int{a: 1},
		},
		{
			name:         "zero quota: everything overflows",
			baseFee:      nil,
			pending:      map[common.Address][]*txpool.LazyTransaction{a: {gasTx(1)}},
			quota:        0,
			wantSelected: map[common.Address]int{},
			wantOverflow: map[common.Address]int{a: 1},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn := reservedCtor(tc.baseFee)

			selected, overflow := selectReservedTxs(tc.pending, tc.quota, fn)

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
		w := &worker{}
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100)}}
		require.Nil(t, w.extractReservedTxs(common.Hash{}, pending, fn))
		require.Contains(t, pending, a, "pending untouched")
	})

	t.Run("empty registry is a no-op", func(t *testing.T) {
		t.Parallel()
		w := &worker{reservedRegistry: NewMock(nil)}
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100)}}
		require.Nil(t, w.extractReservedTxs(common.Hash{}, pending, fn))
		require.Contains(t, pending, a, "pending untouched")
	})

	t.Run("client within quota; normal sender untouched", func(t *testing.T) {
		t.Parallel()
		w := &worker{reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{a}, QuotaGas: 1_000_000}})}
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}, n: {gasTx(100)}}

		groups := w.extractReservedTxs(common.Hash{}, pending, fn)
		require.Len(t, groups, 1)
		require.Equal(t, map[common.Address]int{a: 2}, drainBySender(groups[0]))

		require.NotContains(t, pending, a, "reserved sender removed from pending")
		require.Contains(t, pending, n, "normal sender retained")
	})

	t.Run("quota overflow re-added to pending", func(t *testing.T) {
		t.Parallel()
		w := &worker{reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{a}, QuotaGas: 100}})}
		pending := map[common.Address][]*txpool.LazyTransaction{a: {gasTx(100), gasTx(100)}}

		groups := w.extractReservedTxs(common.Hash{}, pending, fn)
		require.Len(t, groups, 1)
		require.Equal(t, map[common.Address]int{a: 1}, drainBySender(groups[0]))
		require.Len(t, pending[a], 1, "overflow tx re-added to pending for the normal pass")
	})
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

		seq := w.sequenceTxs(newEnv(), pending)
		require.Len(t, seq, 3)
		require.Equal(t, map[common.Address]int{p: 1}, drainBySender(seq[0]), "priority group first")
		require.Equal(t, map[common.Address]int{r: 1}, drainBySender(seq[1]), "reserved group next")
		require.Equal(t, map[common.Address]int{n: 1}, drainBySender(seq[2]), "normal group last")
	})

	t.Run("no prio, no registry: single normal group", func(t *testing.T) {
		t.Parallel()
		w := &worker{}
		pending := map[common.Address][]*txpool.LazyTransaction{n: {gasTx(100)}}

		seq := w.sequenceTxs(newEnv(), pending)
		require.Len(t, seq, 1)
		require.Equal(t, map[common.Address]int{n: 1}, drainBySender(seq[0]))
	})

	t.Run("empty pending yields empty sequence", func(t *testing.T) {
		t.Parallel()
		w := &worker{prio: []common.Address{p}, reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 100}})}
		seq := w.sequenceTxs(newEnv(), map[common.Address][]*txpool.LazyTransaction{})
		require.Empty(t, seq)
	})

	t.Run("reserved overflow joins the normal group", func(t *testing.T) {
		t.Parallel()
		w := &worker{reservedRegistry: NewMock([]Client{{ID: 1, Senders: []common.Address{r}, QuotaGas: 100}})}
		pending := map[common.Address][]*txpool.LazyTransaction{r: {gasTx(100), gasTx(100)}, n: {gasTx(100)}}

		seq := w.sequenceTxs(newEnv(), pending)
		require.Len(t, seq, 2)
		require.Equal(t, map[common.Address]int{r: 1}, drainBySender(seq[0]), "reserved group: one tx fits quota")
		require.Equal(t, map[common.Address]int{n: 1, r: 1}, drainBySender(seq[1]), "normal group: normal sender + reserved overflow")
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

	select {
	case ev := <-sub.Chan():
		require.NotEmpty(t, ev.Data.(core.NewMinedBlockEvent).Block.Transactions())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for block")
	}
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
