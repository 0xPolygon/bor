package legacypool

import (
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

func writeFilterFile(t *testing.T, path string, addrs ...common.Address) {
	t.Helper()

	lines := make([]string, len(addrs))
	for i, addr := range addrs {
		lines[i] = addr.Hex()
	}
	writeListFile(t, path, lines)
}

func writeListFile(t *testing.T, path string, lines []string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("failed to write list file: %v", err)
	}
}

func TestParseFilteredAddresses(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x1111111111111111111111111111111111111111")
	b := common.HexToAddress("0x2222222222222222222222222222222222222222")

	tests := []struct {
		name    string
		data    string
		want    []common.Address
		wantErr bool
	}{
		{"empty", "", nil, true},
		{"blank lines", "\n\n  \n", nil, true},
		{"single", a.Hex(), []common.Address{a}, false},
		{"crlf and padding", "  " + a.Hex() + "\r\n" + b.Hex() + "  \n", []common.Address{a, b}, false},
		{"lowercase", strings.ToLower(a.Hex()), []common.Address{a}, false},
		{"invalid entry", "not-an-address\n" + b.Hex(), nil, true},
		{"duplicates", a.Hex() + "\n" + a.Hex(), []common.Address{a}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFilteredAddresses([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d addresses, want %d", len(got), len(tt.want))
			}
			for _, addr := range tt.want {
				if _, ok := got[addr]; !ok {
					t.Fatalf("missing %s", addr)
				}
			}
		})
	}
}

func TestLoadFilteredAddresses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	present := filepath.Join(dir, "present.txt")
	writeFilterFile(t, present, addr)
	empty := filepath.Join(dir, "empty.txt")
	writeFilterFile(t, empty)
	invalid := filepath.Join(dir, "invalid.txt")
	writeListFile(t, invalid, []string{addr.Hex(), "garbage"})

	tests := []struct {
		name    string
		path    string
		want    int
		wantErr bool
	}{
		{"no path", "", 0, false},
		{"missing file", filepath.Join(dir, "missing.txt"), 0, true},
		{"directory", dir, 0, true},
		{"empty", empty, 0, true},
		{"invalid entry", invalid, 0, true},
		{"present", present, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadFilteredAddresses(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(got) != tt.want {
				t.Fatalf("got %d addresses, want %d", len(got), tt.want)
			}
		})
	}
}

// Not parallel: asserts on package-level metrics.
func TestReloadFilteredAddresses(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(params.Ether))
	for nonce := range uint64(3) {
		if err := pool.addRemoteSync(transaction(nonce, 100000, key)); err != nil {
			t.Fatalf("failed to add tx %d: %v", nonce, err)
		}
	}

	// Enough unrelated transactions that evicting three stays below the
	// priced list's reheap threshold, so the stale counter is observable.
	var others []common.Address
	for range 3 {
		otherKey, _ := crypto.GenerateKey()
		other := crypto.PubkeyToAddress(otherKey.PublicKey)
		others = append(others, other)
		testAddBalance(pool, other, big.NewInt(params.Ether))
		for nonce := range uint64(8) {
			if err := pool.addRemoteSync(transaction(nonce, 100000, otherKey)); err != nil {
				t.Fatalf("failed to add other tx: %v", err)
			}
		}
	}

	path := filepath.Join(t.TempDir(), "filtered.txt")
	r := &filterReloader{path: path}

	reloadsBefore := filteredReloadMeter.Snapshot().Count()
	evictsBefore := filteredEvictMeter.Snapshot().Count()
	stalesBefore := pool.priced.stales.Load()

	// A missing file leaves the empty list in place.
	pool.reloadFilteredAddresses(r)
	if len(pool.filteredAddrs) != 0 || r.sweepNeeded {
		t.Fatalf("missing file: filtered=%d sweepNeeded=%v", len(pool.filteredAddrs), r.sweepNeeded)
	}

	writeFilterFile(t, path, from)
	pool.reloadFilteredAddresses(r)

	if pending, queued := pool.ContentFrom(from); len(pending)+len(queued) != 0 {
		t.Fatalf("filtered sender still has %d pending, %d queued", len(pending), len(queued))
	}
	for _, other := range others {
		if pending, _ := pool.ContentFrom(other); len(pending) != 8 {
			t.Fatalf("other sender has %d pending, want 8", len(pending))
		}
	}
	if err := pool.addRemoteSync(transaction(0, 100000, key)); !errors.Is(err, ErrTxFiltered) {
		t.Fatalf("expected ErrTxFiltered after reload, got %v", err)
	}
	if !r.sweepNeeded {
		t.Fatal("expected a follow-up sweep after the list changed")
	}
	if got := pool.priced.stales.Load() - stalesBefore; got != 3 {
		t.Fatalf("priced stales grew by %d, want 3", got)
	}
	if got := filteredReloadMeter.Snapshot().Count() - reloadsBefore; got != 1 {
		t.Fatalf("reload meter grew by %d, want 1", got)
	}
	if got := filteredEvictMeter.Snapshot().Count() - evictsBefore; got != 3 {
		t.Fatalf("evict meter grew by %d, want 3", got)
	}
	if got := filteredAddrsGauge.Snapshot().Value(); got != 1 {
		t.Fatalf("addresses gauge = %d, want 1", got)
	}
	if got := filteredReloadedGauge.Snapshot().Value(); got == 0 {
		t.Fatal("reloaded gauge not set")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals: %v", err)
	}

	// Simulate an async add that passed validation before the swap and
	// inserted after the first sweep.
	pool.mu.Lock()
	pool.filteredAddrs = map[common.Address]struct{}{}
	pool.mu.Unlock()
	if err := pool.addRemoteSync(transaction(0, 100000, key)); err != nil {
		t.Fatalf("failed to add late tx: %v", err)
	}
	pool.mu.Lock()
	pool.filteredAddrs = map[common.Address]struct{}{from: {}}
	pool.mu.Unlock()

	pool.reloadFilteredAddresses(r)
	if pending, queued := pool.ContentFrom(from); len(pending)+len(queued) != 0 {
		t.Fatal("follow-up sweep did not evict the late transaction")
	}
	if r.sweepNeeded {
		t.Fatal("follow-up sweep should run once")
	}

	// Rewriting the same list with different bytes is not a change.
	if err := os.WriteFile(path, []byte(from.Hex()+"\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool.reloadFilteredAddresses(r)
	if r.sweepNeeded {
		t.Fatal("an equivalent list should not schedule a sweep")
	}
	if got := filteredReloadMeter.Snapshot().Count() - reloadsBefore; got != 1 {
		t.Fatalf("reload meter grew by %d after an equivalent list, want 1", got)
	}

	// A deleted file keeps the current list.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(from) {
		t.Fatal("deleting the file lifted the filter")
	}

	// An emptied or corrupted file keeps it too.
	writeFilterFile(t, path)
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(from) {
		t.Fatal("emptying the file lifted the filter")
	}
	writeListFile(t, path, []string{common.Address{0x1}.Hex(), "garbage"})
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(from) {
		t.Fatal("an invalid entry lifted the filter")
	}

	// Replacing the list adds the new entries at once, and lifts the old ones
	// once the next read agrees.
	writeFilterFile(t, path, common.Address{0x1})
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(from) || !pool.isFiltered(common.Address{0x1}) {
		t.Fatal("first read of a replaced list should add entries without lifting any")
	}
	pool.reloadFilteredAddresses(r)
	if pool.isFiltered(from) {
		t.Fatal("replaced list should lift the filter once read twice")
	}
	if got := filteredAddrsGauge.Snapshot().Value(); got != 1 {
		t.Fatalf("addresses gauge = %d, want 1", got)
	}
	if err := pool.addRemoteSync(transaction(0, 100000, key)); err != nil {
		t.Fatalf("tx should be accepted once unfiltered, got %v", err)
	}
}

// Not parallel: marks package-level metrics other tests assert on.
func TestReloadFilteredAddressesPartialWrite(t *testing.T) {
	pool, _ := setupPool()
	defer pool.Close()

	a, b := common.Address{0xa}, common.Address{0xb}
	path := filepath.Join(t.TempDir(), "filtered.txt")
	r := &filterReloader{path: path}
	writeFilterFile(t, path, a, b)
	pool.reloadFilteredAddresses(r)
	pool.reloadFilteredAddresses(r)

	// A read in the middle of an in-place rewrite sees only the first line.
	writeFilterFile(t, path, a)
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(b) {
		t.Fatal("a half-written file lifted an address")
	}

	writeFilterFile(t, path, a, b)
	pool.reloadFilteredAddresses(r)
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(a) || !pool.isFiltered(b) {
		t.Fatal("completed rewrite should keep both addresses")
	}

	// The next rewrite is read half-written again, two reads after the last.
	writeFilterFile(t, path, a)
	pool.reloadFilteredAddresses(r)
	if !pool.isFiltered(b) {
		t.Fatal("a repeated half-written read lifted an address")
	}
}

// Not parallel: marks package-level metrics other tests assert on.
func TestReloadFilteredAddressesSettles(t *testing.T) {
	a, b := common.Address{0xa}, common.Address{0xb}

	tests := []struct {
		name    string
		initial []byte
		writes  [][]byte
	}{
		{"revert before the next read", []byte(a.Hex()), [][]byte{
			[]byte(a.Hex() + "\n" + b.Hex()),
			[]byte(a.Hex()),
			[]byte(a.Hex()),
		}},
		{"same list in new bytes each read", []byte(a.Hex() + "\n" + b.Hex()), [][]byte{
			[]byte(a.Hex()),
			[]byte(a.Hex() + "\n"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, _ := setupPool()
			defer pool.Close()

			path := filepath.Join(t.TempDir(), "filtered.txt")
			r := &filterReloader{path: path}
			if err := os.WriteFile(path, tt.initial, 0o644); err != nil {
				t.Fatal(err)
			}
			pool.reloadFilteredAddresses(r)
			pool.reloadFilteredAddresses(r)

			for i, data := range tt.writes {
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
				pool.reloadFilteredAddresses(r)
				want := i < len(tt.writes)-1
				if got := pool.isFiltered(b); got != want {
					t.Fatalf("read %d: filtered(b) = %v, want %v", i, got, want)
				}
			}
			if !pool.isFiltered(a) {
				t.Fatal("a kept address was lifted")
			}
		})
	}
}

// Not parallel: marks package-level metrics other tests assert on.
func TestReloadFilteredAddressesKeepsPendingSweep(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(params.Ether))

	path := filepath.Join(t.TempDir(), "filtered.txt")
	r := &filterReloader{path: path}
	writeFilterFile(t, path, from)
	pool.reloadFilteredAddresses(r)
	if !r.sweepNeeded {
		t.Fatal("expected a follow-up sweep after the list changed")
	}

	pool.mu.Lock()
	pool.filteredAddrs = map[common.Address]struct{}{}
	pool.mu.Unlock()
	late := transaction(0, 100000, key)
	if err := pool.addRemoteSync(late); err != nil {
		t.Fatalf("failed to add late tx: %v", err)
	}
	pool.mu.Lock()
	pool.filteredAddrs = map[common.Address]struct{}{from: {}}
	pool.mu.Unlock()

	// The same list in new bytes before the follow-up tick.
	if err := os.WriteFile(path, []byte(from.Hex()+"\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool.reloadFilteredAddresses(r)
	if pool.Has(late.Hash()) {
		t.Fatal("rewriting the same list dropped the pending sweep")
	}
	if r.sweepNeeded {
		t.Fatal("pending sweep should run once")
	}
}

// Not parallel: asserts on a package-level gauge.
func TestFilteredAddressesGaugeAtStartup(t *testing.T) {
	config := testTxPoolConfig
	config.FilteredAddresses = map[common.Address]struct{}{{0x1}: {}, {0x2}: {}}

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	blockchain := newTestBlockChain(params.TestChainConfig, 10000000, statedb, new(event.Feed))
	New(config, blockchain)

	if got := filteredAddrsGauge.Snapshot().Value(); got != 2 {
		t.Fatalf("addresses gauge = %d, want 2", got)
	}
}

func TestReloadFilteredAddressesEvictsAuthorities(t *testing.T) {
	t.Parallel()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	blockchain := newTestBlockChain(params.MergedTestChainConfig, 1000000, statedb, new(event.Feed))
	pool := New(testTxPoolConfig, blockchain)
	pool.Init(testTxPoolConfig.PriceLimit, blockchain.CurrentBlock(), newReserver())
	defer pool.Close()

	senderKey, _ := crypto.GenerateKey()
	testAddBalance(pool, crypto.PubkeyToAddress(senderKey.PublicKey), big.NewInt(params.Ether))

	authKey, _ := crypto.GenerateKey()
	auth, err := types.SignSetCode(authKey, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(params.MergedTestChainConfig.ChainID),
		Address: common.Address{0x42},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx := pricedSetCodeTxWithAuth(0, 250000, uint256.NewInt(1000), uint256.NewInt(1), senderKey, []types.SetCodeAuthorization{auth})
	if err := pool.addRemoteSync(tx); err != nil {
		t.Fatalf("failed to add setcode tx: %v", err)
	}

	path := filepath.Join(t.TempDir(), "filtered.txt")
	writeFilterFile(t, path, crypto.PubkeyToAddress(authKey.PublicKey))
	pool.reloadFilteredAddresses(&filterReloader{path: path})

	if pool.Has(tx.Hash()) {
		t.Fatal("transaction carrying a filtered authority should be evicted")
	}
}

func TestEvictFilteredSenderAndAuthority(t *testing.T) {
	t.Parallel()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	blockchain := newTestBlockChain(params.MergedTestChainConfig, 1000000, statedb, new(event.Feed))
	pool := New(testTxPoolConfig, blockchain)
	pool.Init(testTxPoolConfig.PriceLimit, blockchain.CurrentBlock(), newReserver())
	defer pool.Close()

	senderKey, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	testAddBalance(pool, sender, big.NewInt(params.Ether))
	otherKey, _ := crypto.GenerateKey()
	testAddBalance(pool, crypto.PubkeyToAddress(otherKey.PublicKey), big.NewInt(params.Ether))

	authKey, _ := crypto.GenerateKey()
	auth, err := types.SignSetCode(authKey, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(params.MergedTestChainConfig.ChainID),
		Address: common.Address{0x42},
	})
	if err != nil {
		t.Fatal(err)
	}
	both := pricedSetCodeTxWithAuth(0, 250000, uint256.NewInt(1000), uint256.NewInt(1), senderKey, []types.SetCodeAuthorization{auth})
	other := pricedTransaction(0, 100000, big.NewInt(1000), otherKey)
	for _, tx := range []*types.Transaction{both, other} {
		if err := pool.addRemoteSync(tx); err != nil {
			t.Fatalf("failed to add tx: %v", err)
		}
	}

	pool.mu.Lock()
	pool.filteredAddrs = map[common.Address]struct{}{sender: {}, crypto.PubkeyToAddress(authKey.PublicKey): {}}
	evicted := pool.evictFiltered()
	pool.mu.Unlock()

	if evicted != 1 {
		t.Fatalf("evicted %d transactions, want 1", evicted)
	}
	if pool.Has(both.Hash()) || !pool.Has(other.Hash()) {
		t.Fatal("wrong transaction evicted")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals: %v", err)
	}
}

func TestFilterReloadLoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reload  time.Duration
		reloads bool
	}{
		{"enabled", 10 * time.Millisecond, true},
		{"disabled", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testFilterReloadLoop(t, tt.reload, tt.reloads)
		})
	}
}

func testFilterReloadLoop(t *testing.T, reload time.Duration, reloads bool) {
	path := filepath.Join(t.TempDir(), "filtered.txt")
	writeFilterFile(t, path, common.Address{0x1})

	config := testTxPoolConfig
	config.FilteredAddressesFile = path
	config.FilterReload = reload

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	blockchain := newTestBlockChain(params.TestChainConfig, 10000000, statedb, new(event.Feed))
	pool := New(config, blockchain)
	if err := pool.Init(config.PriceLimit, blockchain.CurrentBlock(), newReserver()); err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(params.Ether))
	tx := transaction(0, 100000, key)
	if err := pool.addRemoteSync(tx); err != nil {
		t.Fatalf("failed to add tx: %v", err)
	}

	writeFilterFile(t, path, from)

	if !reloads {
		time.Sleep(100 * time.Millisecond)
		if !pool.Has(tx.Hash()) {
			t.Fatal("transaction evicted with reload disabled")
		}
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for pool.Has(tx.Hash()) {
		if time.Now().After(deadline) {
			t.Fatal("reload loop did not evict the filtered transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
