// Copyright 2024 The go-ethereum Authors
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

package downloader

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/log"
)

// healTestCode is the bytecode the heal tests fetch; its hash is the missing
// code the scripted chain reports.
var healTestCode = []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}

// fakeCodePeer is a minimal snap.SyncPeer for the heal tests. It answers
// bytecode requests from corpus (well-behaving: request order, omitting hashes
// it lacks) — or never answers when silent, modelling a stalled peer so a fetch
// ends only via timeout or context cancellation. onRequest, if set, runs
// synchronously inside each RequestByteCodes before any reply, letting a test
// inject a downloader cancel/terminate exactly while a fetch is in flight.
type fakeCodePeer struct {
	id        string
	syncer    *snap.Syncer
	corpus    map[common.Hash][]byte
	silent    bool
	onRequest func()
	requests  atomic.Int32
}

func (p *fakeCodePeer) ID() string      { return p.id }
func (p *fakeCodePeer) Log() log.Logger { return log.New("id", p.id) }

func (p *fakeCodePeer) RequestAccountRange(id uint64, root, origin, limit common.Hash, bytes uint64) error {
	return nil
}

func (p *fakeCodePeer) RequestStorageRanges(id uint64, root common.Hash, accounts []common.Hash, origin, limit []byte, bytes uint64) error {
	return nil
}

func (p *fakeCodePeer) RequestTrieNodes(id uint64, root common.Hash, paths []snap.TrieNodePathSet, bytes uint64) error {
	return nil
}

func (p *fakeCodePeer) RequestByteCodes(id uint64, hashes []common.Hash, bytes uint64) error {
	p.requests.Add(1)
	if p.onRequest != nil {
		p.onRequest()
	}
	if p.silent {
		return nil
	}
	var out [][]byte
	for _, h := range hashes {
		if code, ok := p.corpus[h]; ok {
			out = append(out, code)
		}
	}
	go p.syncer.OnByteCodes(p, id, out)
	return nil
}

// newHealSyncer returns a snap syncer with a single registered fakeCodePeer
// that serves healTestCode when servable, and the peer itself.
func newHealSyncer(t *testing.T, servable bool) (*snap.Syncer, *fakeCodePeer) {
	t.Helper()
	syncer := snap.NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	corpus := map[common.Hash][]byte{}
	if servable {
		corpus[crypto.Keccak256Hash(healTestCode)] = healTestCode
	}
	peer := &fakeCodePeer{id: "p1", syncer: syncer, corpus: corpus}
	if err := syncer.Register(peer); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	return syncer, peer
}

// TestRecoverMissingStatelessCode exercises the downloader self-heal hook:
// a *state.MissingCodeError is healed by fetching the blob from a snap peer and
// persisting it to the chain db; any other failure, a missing snap syncer, or an
// unservable blob is left untouched for the caller's safe failure path.
func TestRecoverMissingStatelessCode(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)
	chaindb := rawdb.NewMemoryDatabase()
	syncer, _ := newHealSyncer(t, true)
	d := &Downloader{stateDB: chaindb, SnapSyncer: syncer}
	ctx := context.Background()

	// A non-code failure must not be treated as recoverable, and must write nothing.
	if d.recoverMissingStatelessCode(ctx, errors.New("gas limit reached")) {
		t.Fatal("recovered a non-MissingCodeError")
	}
	if rawdb.HasCode(chaindb, hash) {
		t.Fatal("a non-code failure wrote code")
	}

	// Without a snap syncer there is nobody to fetch from: not recoverable, no panic.
	noSyncer := &Downloader{stateDB: chaindb}
	if noSyncer.recoverMissingStatelessCode(ctx, &state.MissingCodeError{Hash: hash}) {
		t.Fatal("reported recovery without a snap syncer")
	}

	// A missing code a peer can serve is fetched, verified and persisted.
	if !d.recoverMissingStatelessCode(ctx, &state.MissingCodeError{Hash: hash}) {
		t.Fatal("did not recover a servable missing code")
	}
	if got := rawdb.ReadCode(chaindb, hash); !bytes.Equal(got, healTestCode) {
		t.Fatalf("healed code not persisted to chain db: got %x want %x", got, healTestCode)
	}

	// A missing code no peer serves is not recoverable (caller falls back to safe stop).
	unservable := crypto.Keccak256Hash([]byte("no peer has this"))
	if d.recoverMissingStatelessCode(ctx, &state.MissingCodeError{Hash: unservable}) {
		t.Fatal("reported recovery for an unservable code")
	}
	if rawdb.HasCode(chaindb, unservable) {
		t.Fatal("unservable code was somehow persisted")
	}
}

// TestRecoverMissingStatelessCodeHonorsCancel proves the self-heal fetch aborts
// promptly when its context is cancelled rather than blocking for the full
// statelessCodeHealTimeout — the property that keeps Cancel()/Terminate() from
// stalling on an outstanding network fetch during the heal-and-retry loop.
func TestRecoverMissingStatelessCodeHonorsCancel(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)

	syncer := snap.NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	if err := syncer.Register(&fakeCodePeer{id: "silent", syncer: syncer, silent: true}); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	d := &Downloader{stateDB: rawdb.NewMemoryDatabase(), SnapSyncer: syncer}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the fetch starts

	done := make(chan bool, 1)
	go func() { done <- d.recoverMissingStatelessCode(ctx, &state.MissingCodeError{Hash: hash}) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("reported recovery from a peer that never answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recoverMissingStatelessCode ignored context cancellation (blocked on the fetch)")
	}
}

// fakeStatelessChain is a minimal BlockChain that only implements
// InsertChainStateless, returning a scripted sequence of errors (nil once
// exhausted). Any other method call panics — the heal loop touches none.
type fakeStatelessChain struct {
	BlockChain
	results []error
	calls   int
}

func (f *fakeStatelessChain) InsertChainStateless(types.Blocks, []*stateless.Witness) (int, error) {
	i := f.calls
	f.calls++
	if i < len(f.results) {
		return 0, f.results[i]
	}
	return 0, nil
}

// newHealDownloader wires a downloader over chain with a single snap peer that
// serves healTestCode when servable, returning both.
func newHealDownloader(t *testing.T, chain BlockChain, servable bool) (*Downloader, *fakeCodePeer) {
	t.Helper()
	syncer, peer := newHealSyncer(t, servable)
	return &Downloader{
		stateDB:    rawdb.NewMemoryDatabase(),
		SnapSyncer: syncer,
		blockchain: chain,
		quitCh:     make(chan struct{}),
		cancelCh:   make(chan struct{}),
	}, peer
}

// TestInsertStatelessWithHeal covers the heal-and-retry loop: a healable miss is
// recovered and retried to success, an unservable miss and a non-code error both
// stop after one attempt, a clean import does not retry, and a miss that never
// clears is bounded by maxStatelessCodeHeals.
func TestInsertStatelessWithHeal(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)
	blocks := types.Blocks{types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})}
	wits := []*stateless.Witness{nil}

	// Servable miss: healed on the first retry, code persisted.
	chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
	d, peer := newHealDownloader(t, chain, true)
	if _, err := d.insertStatelessWithHeal(blocks, wits); err != nil {
		t.Fatalf("expected heal+success, got %v", err)
	}
	if chain.calls != 2 {
		t.Fatalf("expected initial + one retry = 2 inserts, got %d", chain.calls)
	}
	if peer.requests.Load() != 1 {
		t.Fatalf("expected one fetch for one miss, got %d", peer.requests.Load())
	}
	if got := rawdb.ReadCode(d.stateDB, hash); !bytes.Equal(got, healTestCode) {
		t.Fatalf("healed code not persisted: got %x", got)
	}

	// Unservable miss: no retry, the miss itself is returned.
	chainU := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
	dU, _ := newHealDownloader(t, chainU, false)
	var mce *state.MissingCodeError
	if _, err := dU.insertStatelessWithHeal(blocks, wits); !errors.As(err, &mce) {
		t.Fatalf("expected the missing-code error for an unservable code, got %v", err)
	}
	if chainU.calls != 1 {
		t.Fatalf("unservable code must not retry: got %d inserts", chainU.calls)
	}

	// Non-code error: surfaced immediately, no heal attempt.
	boom := errors.New("boom")
	chainE := &fakeStatelessChain{results: []error{boom}}
	dE, peerE := newHealDownloader(t, chainE, true)
	if _, err := dE.insertStatelessWithHeal(blocks, wits); !errors.Is(err, boom) {
		t.Fatalf("expected the non-code error to surface unchanged, got %v", err)
	}
	if chainE.calls != 1 || peerE.requests.Load() != 0 {
		t.Fatalf("non-code error must not retry or fetch: %d inserts, %d fetches", chainE.calls, peerE.requests.Load())
	}

	// Clean import: a single attempt, no heal.
	chainOK := &fakeStatelessChain{}
	dOK, peerOK := newHealDownloader(t, chainOK, true)
	if _, err := dOK.insertStatelessWithHeal(blocks, wits); err != nil {
		t.Fatalf("clean import: %v", err)
	}
	if chainOK.calls != 1 || peerOK.requests.Load() != 0 {
		t.Fatalf("clean import must not retry or fetch: %d inserts, %d fetches", chainOK.calls, peerOK.requests.Load())
	}

	// A miss that never clears: retries are bounded by maxStatelessCodeHeals.
	seq := make([]error, maxStatelessCodeHeals+5)
	for i := range seq {
		seq[i] = &state.MissingCodeError{Hash: hash}
	}
	chainB := &fakeStatelessChain{results: seq}
	dB, _ := newHealDownloader(t, chainB, true)
	if _, err := dB.insertStatelessWithHeal(blocks, wits); err == nil {
		t.Fatal("expected bounded failure when the miss never clears")
	}
	if chainB.calls != maxStatelessCodeHeals+1 {
		t.Fatalf("expected initial + %d retries = %d inserts, got %d", maxStatelessCodeHeals, maxStatelessCodeHeals+1, chainB.calls)
	}
}

// stopChannels enumerates the two downloader lifecycle signals the heal loop
// must honour; each test below runs once per signal.
func stopChannels(d *Downloader) map[string]chan struct{} {
	return map[string]chan struct{}{"quit": d.quitCh, "cancel": d.cancelCh}
}

// TestInsertStatelessWithHealStopsBeforeHeal proves a downloader that is already
// cancelled or terminated when an import fails does not start a heal at all: no
// peer is asked, the miss is returned as-is after the single insert.
func TestInsertStatelessWithHealStopsBeforeHeal(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)
	blocks := types.Blocks{types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})}
	wits := []*stateless.Witness{nil}

	for name := range stopChannels(&Downloader{quitCh: make(chan struct{}), cancelCh: make(chan struct{})}) {
		t.Run(name, func(t *testing.T) {
			chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
			d, peer := newHealDownloader(t, chain, true)
			close(stopChannels(d)[name])

			var mce *state.MissingCodeError
			if _, err := d.insertStatelessWithHeal(blocks, wits); !errors.As(err, &mce) {
				t.Fatalf("expected the missing-code error to be returned unhealed, got %v", err)
			}
			if chain.calls != 1 {
				t.Fatalf("a stopped downloader must not retry: got %d inserts", chain.calls)
			}
			if peer.requests.Load() != 0 {
				t.Fatalf("a stopped downloader must not fetch: got %d request(s)", peer.requests.Load())
			}
		})
	}
}

// TestInsertStatelessWithHealAbortsInFlightFetch proves a cancel/terminate that
// arrives while a heal fetch is waiting on a peer aborts it promptly — well
// inside the peer timeout — rather than blocking Cancel()/Terminate() until the
// network timeouts run out.
func TestInsertStatelessWithHealAbortsInFlightFetch(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)
	blocks := types.Blocks{types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})}
	wits := []*stateless.Witness{nil}

	for name := range stopChannels(&Downloader{quitCh: make(chan struct{}), cancelCh: make(chan struct{})}) {
		t.Run(name, func(t *testing.T) {
			chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
			d, peer := newHealDownloader(t, chain, true)
			// The peer stalls; the stop signal fires from inside the request,
			// i.e. exactly while the fetch is in flight.
			peer.silent = true
			var once sync.Once
			peer.onRequest = func() { once.Do(func() { close(stopChannels(d)[name]) }) }

			type result struct {
				err error
			}
			done := make(chan result, 1)
			go func() {
				_, err := d.insertStatelessWithHeal(blocks, wits)
				done <- result{err}
			}()
			select {
			case r := <-done:
				var mce *state.MissingCodeError
				if !errors.As(r.err, &mce) {
					t.Fatalf("expected the missing-code error after an aborted heal, got %v", r.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("heal did not abort on the stop signal (blocked on the in-flight fetch)")
			}
			if chain.calls != 1 {
				t.Fatalf("an aborted heal must not retry: got %d inserts", chain.calls)
			}
			if peer.requests.Load() != 1 {
				t.Fatalf("expected exactly the one in-flight request, got %d", peer.requests.Load())
			}
		})
	}
}

// TestImportBlockResultsStateless covers the import entry point around the heal
// loop: a clean or healed import succeeds, a failed import is reported as an
// invalid body, and a cancel/terminate during the heal surfaces as the matching
// lifecycle error instead of a body error.
func TestImportBlockResultsStateless(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)
	results := []*fetchResult{{Header: &types.Header{Number: big.NewInt(1)}}}

	t.Run("empty", func(t *testing.T) {
		chain := &fakeStatelessChain{}
		d, _ := newHealDownloader(t, chain, true)
		if err := d.importBlockResultsStateless(nil); err != nil {
			t.Fatalf("empty batch: %v", err)
		}
		if chain.calls != 0 {
			t.Fatalf("empty batch must not insert, got %d inserts", chain.calls)
		}
	})

	lifecycle := map[string]error{"quit": errTerminated, "cancel": errCanceled}
	for name, want := range lifecycle {
		t.Run("stopped-before-import/"+name, func(t *testing.T) {
			chain := &fakeStatelessChain{}
			d, _ := newHealDownloader(t, chain, true)
			close(stopChannels(d)[name])
			if err := d.importBlockResultsStateless(results); !errors.Is(err, want) {
				t.Fatalf("expected %v when already stopped, got %v", want, err)
			}
			if chain.calls != 0 {
				t.Fatalf("a stopped downloader must not insert, got %d inserts", chain.calls)
			}
		})
	}

	t.Run("clean", func(t *testing.T) {
		chain := &fakeStatelessChain{}
		d, _ := newHealDownloader(t, chain, true)
		var hooked int
		d.chainInsertHook = func(got []*fetchResult) {
			if len(got) != len(results) {
				t.Errorf("insert hook saw %d results, want %d", len(got), len(results))
			}
			hooked++
		}
		if err := d.importBlockResultsStateless(results); err != nil {
			t.Fatalf("clean import: %v", err)
		}
		if chain.calls != 1 {
			t.Fatalf("expected one insert, got %d", chain.calls)
		}
		if hooked != 1 {
			t.Fatalf("expected the insert hook to run once, got %d", hooked)
		}
	})

	t.Run("healed", func(t *testing.T) {
		chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
		d, _ := newHealDownloader(t, chain, true)
		if err := d.importBlockResultsStateless(results); err != nil {
			t.Fatalf("healed import: %v", err)
		}
		if chain.calls != 2 {
			t.Fatalf("expected initial + one retry = 2 inserts, got %d", chain.calls)
		}
		if got := rawdb.ReadCode(d.stateDB, hash); !bytes.Equal(got, healTestCode) {
			t.Fatalf("healed code not persisted: got %x", got)
		}
	})

	t.Run("failed", func(t *testing.T) {
		chain := &fakeStatelessChain{results: []error{errors.New("boom")}}
		d, _ := newHealDownloader(t, chain, true)
		if err := d.importBlockResultsStateless(results); !errors.Is(err, errInvalidBody) {
			t.Fatalf("expected errInvalidBody for a failed import, got %v", err)
		}
	})

	t.Run("unservable", func(t *testing.T) {
		chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
		d, _ := newHealDownloader(t, chain, false)
		if err := d.importBlockResultsStateless(results); !errors.Is(err, errInvalidBody) {
			t.Fatalf("expected errInvalidBody for an unhealable import, got %v", err)
		}
	})

	for name, want := range lifecycle {
		t.Run("stopped-during-heal/"+name, func(t *testing.T) {
			chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
			d, peer := newHealDownloader(t, chain, true)
			peer.silent = true
			var once sync.Once
			peer.onRequest = func() { once.Do(func() { close(stopChannels(d)[name]) }) }

			done := make(chan error, 1)
			go func() { done <- d.importBlockResultsStateless(results) }()
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("expected %v when stopped during the heal, got %v", want, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("import did not return after the stop signal")
			}
		})
	}
}
