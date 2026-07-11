// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package legacypool

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/triedb"
)

// Tests that transactions can be added to strict lists and list contents and
// nonce boundaries are correctly maintained.
func TestStrictListAdd(t *testing.T) {
	t.Parallel()

	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 1024)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}
	// Insert the transactions in a random order
	list := newList(true)
	for _, v := range rand.Perm(len(txs)) {
		list.Add(txs[v], DefaultConfig.PriceBump)
	}
	// Verify internal state
	if len(list.txs.items) != len(txs) {
		t.Errorf("transaction count mismatch: have %d, want %d", len(list.txs.items), len(txs))
	}

	for i, tx := range txs {
		if list.txs.items[tx.Nonce()] != tx {
			t.Errorf("item %d: transaction mismatch: have %v, want %v", i, list.txs.items[tx.Nonce()], tx)
		}
	}
}

// TestListAddVeryExpensive tests adding txs which exceed 256 bits in cost. It is
// expected that the list does not panic.
func TestListAddVeryExpensive(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)
	for i := 0; i < 3; i++ {
		value := big.NewInt(100)
		gasprice, _ := new(big.Int).SetString("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 0)
		gaslimit := uint64(i)
		tx, _ := types.SignTx(types.NewTransaction(uint64(i), common.Address{}, value, gaslimit, gasprice, nil), types.HomesteadSigner{}, key)
		t.Logf("cost: %x bitlen: %d\n", tx.Cost(), tx.Cost().BitLen())
		list.Add(tx, DefaultConfig.PriceBump)
	}
}

func BenchmarkListAdd(b *testing.B) {
	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 100000)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}
	// Insert the transactions in a random order
	priceLimit := uint256.NewInt(DefaultConfig.PriceLimit)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list := newList(true)
		for _, v := range rand.Perm(len(txs)) {
			list.Add(txs[v], DefaultConfig.PriceBump)
			list.Filter(priceLimit, DefaultConfig.PriceBump)
		}
	}
}

func TestFilterTxConditionalKnownAccounts(t *testing.T) {
	t.Parallel()

	// Create an in memory state db to test against.
	memDb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memDb, &triedb.Config{Preimages: true})
	db := state.NewDatabase(tdb, nil)
	state, _ := state.New(common.Hash{}, db)

	header := &types.Header{
		Number: big.NewInt(0),
	}

	// Create a private key to sign transactions.
	key, _ := crypto.GenerateKey()

	// Create a list.
	list := newList(true)

	// Create a transaction with no defined tx options
	// and add to the list.
	tx := transaction(0, 1000, key)
	list.Add(tx, DefaultConfig.PriceBump)

	// There should be no drops at this point.
	// No state has been modified.
	drops := list.FilterTxConditional(state, header)

	count := len(drops)
	require.Equal(t, 0, count, "got %d filtered by TxOptions when there should not be any", count)

	// Create another transaction with a known account storage root tx option
	// and add to the list.
	tx2 := transaction(1, 1000, key)

	var options types.OptionsPIP15

	options.KnownAccounts = types.KnownAccounts{
		common.Address{19: 1}: &types.Value{
			Single: common.HexToRefHash("0xe734938daf39aae1fa4ee64dc3155d7c049f28b57a8ada8ad9e86832e0253bef"),
		},
	}

	state.AddBalance(common.Address{19: 1}, uint256.NewInt(1000), tracing.BalanceChangeTransfer)

	trie, _ := state.StorageTrie(common.Address{19: 1})
	fmt.Println("before", trie)

	state.SetState(common.Address{19: 1}, common.Hash{}, common.Hash{30: 1})

	state.Finalise(true)

	trie, _ = state.StorageTrie(common.Address{19: 1})
	fmt.Println("after", trie.Hash())

	tx2.PutOptions(&options)
	list.Add(tx2, DefaultConfig.PriceBump)

	// There should still be no drops as no state has been modified.
	drops = list.FilterTxConditional(state, header)

	count = len(drops)
	require.Equal(t, 0, count, "got %d filtered by TxOptions when there should not be any", count)

	// Set state that conflicts with tx2's policy
	state.SetState(common.Address{19: 1}, common.Hash{}, common.Hash{31: 1})

	state.Finalise(true)

	trie, _ = state.StorageTrie(common.Address{19: 1})
	fmt.Println("after2", trie.Hash())

	// tx2 should be the single transaction filtered out
	drops = list.FilterTxConditional(state, header)

	count = len(drops)
	require.Equal(t, 1, count, "got %d filtered by TxOptions when there should be a single one", count)

	require.Equal(t, tx2, drops[0], "Got %x, expected %x", drops[0].Hash(), tx2.Hash())
}

func TestFilterTxConditionalBlockNumber(t *testing.T) {
	t.Parallel()

	// Create an in memory state db to test against.
	memDb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memDb, &triedb.Config{Preimages: true})
	db := state.NewDatabase(tdb, nil)
	state, _ := state.New(common.Hash{}, db)

	header := &types.Header{
		Number: big.NewInt(100),
	}

	// Create a private key to sign transactions.
	key, _ := crypto.GenerateKey()

	// Create a list.
	list := newList(true)

	// Create a transaction with no defined tx options
	// and add to the list.
	tx := transaction(0, 1000, key)
	list.Add(tx, DefaultConfig.PriceBump)

	// There should be no drops at this point.
	// No state has been modified.
	drops := list.FilterTxConditional(state, header)

	count := len(drops)
	require.Equal(t, 0, count, "got %d filtered by TxOptions when there should not be any", count)

	// Create another transaction with a block number option and add to the list.
	tx2 := transaction(1, 1000, key)

	var options types.OptionsPIP15

	options.BlockNumberMin = big.NewInt(90)
	options.BlockNumberMax = big.NewInt(110)

	tx2.PutOptions(&options)
	list.Add(tx2, DefaultConfig.PriceBump)

	// There should still be no drops as no state has been modified.
	drops = list.FilterTxConditional(state, header)

	count = len(drops)
	require.Equal(t, 0, count, "got %d filtered by TxOptions when there should not be any", count)

	// Set block number that conflicts with tx2's policy
	header.Number = big.NewInt(120)

	// tx2 should be the single transaction filtered out
	drops = list.FilterTxConditional(state, header)

	count = len(drops)
	require.Equal(t, 1, count, "got %d filtered by TxOptions when there should be a single one", count)

	require.Equal(t, tx2, drops[0], "Got %x, expected %x", drops[0].Hash(), tx2.Hash())
}

func TestFilterTxConditionalTimestamp(t *testing.T) {
	t.Parallel()

	// Create an in memory state db to test against.
	memDb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memDb, &triedb.Config{Preimages: true})
	db := state.NewDatabase(tdb, nil)
	state, _ := state.New(common.Hash{}, db)

	header := &types.Header{
		Number: big.NewInt(0),
		Time:   100,
	}

	// Create a private key to sign transactions.
	key, _ := crypto.GenerateKey()

	// Create a list.
	list := newList(true)

	// Create a transaction with no defined tx options
	// and add to the list.
	tx := transaction(0, 1000, key)
	list.Add(tx, DefaultConfig.PriceBump)

	// There should be no drops at this point.
	// No state has been modified.
	drops := list.FilterTxConditional(state, header)

	count := len(drops)
	require.Equal(t, 0, count, "got %d filtered by TxOptions when there should not be any", count)

	// Create another transaction with a timestamp option and add to the list.
	tx2 := transaction(1, 1000, key)

	var options types.OptionsPIP15

	minTimestamp := uint64(90)
	maxTimestamp := uint64(110)

	options.TimestampMin = &minTimestamp
	options.TimestampMax = &maxTimestamp

	tx2.PutOptions(&options)
	list.Add(tx2, DefaultConfig.PriceBump)

	// There should still be no drops as no state has been modified.
	drops = list.FilterTxConditional(state, header)

	count = len(drops)
	require.Equal(t, 0, count, "got %d filtered by TxOptions when there should not be any", count)

	// Set timestamp that conflicts with tx2's policy
	header.Time = 120

	// tx2 should be the single transaction filtered out
	drops = list.FilterTxConditional(state, header)

	count = len(drops)
	require.Equal(t, 1, count, "got %d filtered by TxOptions when there should be a single one", count)

	require.Equal(t, tx2, drops[0], "Got %x, expected %x", drops[0].Hash(), tx2.Hash())
}

// TestPricedListReheapSnapshot validates the reheap snapshot mechanism:
//   - Reheap increments the reheaps counter
//   - Put/PutMany insert when the snapshot matches the current counter
//   - Put/PutMany skip when the snapshot is stale
func TestPricedListReheapSnapshot(t *testing.T) {
	t.Parallel()

	all := newLookup()
	priced := newPricedList(all)

	key, _ := crypto.GenerateKey()
	tx1 := pricedTransaction(0, 100000, big.NewInt(1), key)
	tx2 := pricedTransaction(1, 100000, big.NewInt(2), key)
	tx3 := pricedTransaction(2, 100000, big.NewInt(3), key)

	heapLen := func() int {
		priced.reheapMu.Lock()
		defer priced.reheapMu.Unlock()
		return priced.urgent.Len() + priced.floating.Len()
	}

	// Initial counter is 0.
	require.Equal(t, uint64(0), priced.reheaps.Load())

	// Put with matching snapshot inserts.
	priced.Put(tx1, 0)
	require.Equal(t, 1, heapLen())

	// PutMany with matching snapshot inserts.
	priced.PutMany(types.Transactions{tx2, tx3}, 0)
	require.Equal(t, 3, heapLen())

	// Reheap increments the counter.
	all.Add(tx1)
	priced.Reheap()
	require.Equal(t, uint64(1), priced.reheaps.Load())

	// After Reheap, only tx1 is in lookup so heap has 1 entry.
	require.Equal(t, 1, heapLen())

	// Put with stale snapshot (0) is a no-op.
	priced.Put(tx1, 0)
	require.Equal(t, 1, heapLen())

	// PutMany with stale snapshot (0) is a no-op.
	priced.PutMany(types.Transactions{tx2, tx3}, 0)
	require.Equal(t, 1, heapLen())

	// Put with current snapshot (1) inserts.
	priced.Put(tx2, 1)
	require.Equal(t, 2, heapLen())

	// PutMany with current snapshot (1) inserts.
	priced.PutMany(types.Transactions{tx3}, 1)
	require.Equal(t, 3, heapLen())
}

func BenchmarkListCapOneTx(b *testing.B) {
	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 32)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list := newList(true)
		// Insert the transactions in a random order
		for _, v := range rand.Perm(len(txs)) {
			list.Add(txs[v], DefaultConfig.PriceBump)
		}
		b.StartTimer()
		list.Cap(list.Len() - 1)
		b.StopTimer()
	}
}

// TestLazyFlattenDifferential drives a list through a random sequence of every
// mutation path and verifies after each step that the cached lazy view matches
// a fresh conversion of Flatten() — i.e. the generation counter invalidates the
// view on every content change and never spuriously retains a stale one.
// TestCutScannerDifferential pins cutScanner.pass to the reference
// per-transaction predicate (Transaction.EffectiveGasTipIntCmp plus the gas
// cap check) across the fee-boundary cases, including the feeCap<baseFee
// wrap-through that makes the effective tip degrade to the tip cap.
func TestCutScannerDifferential(t *testing.T) {
	t.Parallel()

	lazy := func(tx *types.Transaction) *txpool.LazyTransaction {
		return &txpool.LazyTransaction{
			Hash:      tx.Hash(),
			Tx:        tx,
			GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
			GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
			Gas:       tx.Gas(),
		}
	}
	reference := func(ltx *txpool.LazyTransaction, minTip, baseFee *uint256.Int, gasCap uint64) bool {
		if minTip != nil && ltx.Tx.EffectiveGasTipIntCmp(minTip, baseFee) < 0 {
			return false
		}
		return gasCap == 0 || ltx.Gas <= gasCap
	}

	u := func(v uint64) *uint256.Int { return uint256.NewInt(v) }
	huge := new(uint256.Int).Sub(new(uint256.Int).Lsh(u(1), 255), u(1))

	var lazies []*txpool.LazyTransaction
	// Dynamic-fee grid around the boundaries plus extreme values.
	values := []*uint256.Int{u(0), u(1), u(29), u(30), u(31), u(59), u(60), u(61), u(100), huge}
	for _, tip := range values {
		for _, cap_ := range values {
			if cap_.Lt(tip) {
				continue // pool validation enforces feeCap >= tipCap
			}
			lazies = append(lazies, lazy(types.NewTx(&types.DynamicFeeTx{
				GasTipCap: tip.ToBig(), GasFeeCap: cap_.ToBig(), Gas: 21000,
			})))
		}
	}
	// Legacy txs (gasPrice = tipCap = feeCap) and gas-cap boundary rows.
	for _, price := range values {
		lazies = append(lazies, lazy(types.NewTx(&types.LegacyTx{GasPrice: price.ToBig(), Gas: 21000})))
	}
	for _, gas := range []uint64{20999, 21000, 21001, 30_000_000} {
		lazies = append(lazies, lazy(types.NewTx(&types.DynamicFeeTx{
			GasTipCap: u(30).ToBig(), GasFeeCap: u(100).ToBig(), Gas: gas,
		})))
	}

	cuts := []struct {
		minTip, baseFee *uint256.Int
		gasCap          uint64
	}{
		{nil, nil, 0},
		{nil, u(30), 0},
		{u(0), u(30), 0},
		{u(30), nil, 0},
		{u(30), u(30), 0},
		{u(30), u(31), 0},
		{u(1), u(60), 0},
		{u(30), u(60), 21000},
		{u(30), u(60), 20999},
		{huge, u(30), 0},
		{u(30), huge, 0},    // most feeCaps < baseFee: wrap-through path
		{huge, huge, 21000}, // feeFloor overflows uint256
	}
	for _, c := range cuts {
		scanner := newCutScanner(c.minTip, c.baseFee, c.gasCap)
		for _, ltx := range lazies {
			want := reference(ltx, c.minTip, c.baseFee, c.gasCap)
			got := scanner.pass(ltx)
			require.Equal(t, want, got,
				"cut{tip=%v fee=%v cap=%d} tx{tip=%v cap=%v gas=%d}",
				c.minTip, c.baseFee, c.gasCap, ltx.GasTipCap, ltx.GasFeeCap, ltx.Gas)
		}
	}

	// Randomized sweep for anything the grid missed.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		tip, cap_ := rng.Uint64()%200, rng.Uint64()%200
		if cap_ < tip {
			tip, cap_ = cap_, tip
		}
		ltx := lazy(types.NewTx(&types.DynamicFeeTx{
			GasTipCap: u(tip).ToBig(), GasFeeCap: u(cap_).ToBig(), Gas: 21000 + rng.Uint64()%100,
		}))
		minTip, baseFee := u(rng.Uint64()%200), u(rng.Uint64()%200)
		gasCap := rng.Uint64() % 21200
		scanner := newCutScanner(minTip, baseFee, gasCap)
		require.Equal(t, reference(ltx, minTip, baseFee, gasCap), scanner.pass(ltx),
			"fuzz tx{tip=%d cap=%d gas=%d} cut{tip=%v fee=%v cap=%d}",
			tip, cap_, ltx.Gas, minTip, baseFee, gasCap)
	}
}

func TestLazyFlattenDifferential(t *testing.T) {
	t.Parallel()

	key, _ := crypto.GenerateKey()

	check := func(l *list, step string) {
		t.Helper()

		want := l.txs.Flatten()
		got, _ := l.LazyFlatten(nil, [2]*filteredCut{})
		require.Equal(t, len(want), len(got), "length mismatch after %s", step)

		for i := range want {
			require.Equal(t, want[i].Hash(), got[i].Hash, "hash mismatch at %d after %s", i, step)
			require.Equal(t, want[i].Nonce(), got[i].Tx.Nonce(), "nonce mismatch at %d after %s", i, step)
			require.Equal(t, want[i].Gas(), got[i].Gas, "gas mismatch at %d after %s", i, step)
		}
		// A second call with no mutation must return the identical shared slice.
		again, _ := l.LazyFlatten(nil, [2]*filteredCut{})
		if len(got) > 0 {
			require.Same(t, got[0], again[0], "unchanged view was rebuilt after %s", step)
		}
	}

	rng := rand.New(rand.NewSource(42))
	l := newList(false) // non-strict: mirrors SortedMap-level behavior for all ops

	nonce := uint64(0)
	for step := 0; step < 500; step++ {
		switch op := rng.Intn(6); op {
		case 0: // Add
			l.Add(transaction(nonce, 100000, key), DefaultConfig.PriceBump)
			nonce++
		case 1: // Forward
			if l.Len() > 0 {
				l.Forward(l.txs.Flatten()[0].Nonce() + uint64(rng.Intn(3)))
			}
		case 2: // Filter (drop random txs)
			l.txs.Filter(func(tx *types.Transaction) bool { return rng.Intn(4) == 0 })
		case 3: // Cap
			if l.Len() > 1 {
				l.Cap(l.Len() - 1)
			}
		case 4: // Remove
			if l.Len() > 0 {
				l.txs.Remove(l.txs.LastElement().Nonce())
			}
		case 5: // Ready
			if l.Len() > 0 {
				l.txs.Ready(l.txs.Flatten()[0].Nonce())
			}
		}
		check(l, "step")
	}
}
