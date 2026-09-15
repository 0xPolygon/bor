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

// fakeCodePeer is a minimal snap.SyncPeer that answers bytecode requests from a
// fixed corpus (well-behaving: request order, omitting hashes it lacks) and
// no-ops every other request kind.
type fakeCodePeer struct {
	id     string
	syncer *snap.Syncer
	corpus map[common.Hash][]byte
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
	var out [][]byte
	for _, h := range hashes {
		if code, ok := p.corpus[h]; ok {
			out = append(out, code)
		}
	}
	go p.syncer.OnByteCodes(p, id, out)
	return nil
}

// TestRecoverMissingStatelessCode exercises the downloader self-heal hook:
// a *state.MissingCodeError is healed by fetching the blob from a snap peer and
// persisting it to the chain db; any other failure, or an unservable blob, is
// left untouched for the caller's safe failure path.
func TestRecoverMissingStatelessCode(t *testing.T) {
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	chaindb := rawdb.NewMemoryDatabase()
	syncer := snap.NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	peer := &fakeCodePeer{id: "p1", syncer: syncer, corpus: map[common.Hash][]byte{hash: code}}
	if err := syncer.Register(peer); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	d := &Downloader{stateDB: chaindb, SnapSyncer: syncer}
	ctx := context.Background()

	// A non-code failure must not be treated as recoverable, and must write nothing.
	if d.recoverMissingStatelessCode(ctx, errors.New("gas limit reached")) {
		t.Fatal("recovered a non-MissingCodeError")
	}

	// A missing code a peer can serve is fetched, verified and persisted.
	if !d.recoverMissingStatelessCode(ctx, &state.MissingCodeError{Hash: hash}) {
		t.Fatal("did not recover a servable missing code")
	}
	if got := rawdb.ReadCode(chaindb, hash); !bytes.Equal(got, code) {
		t.Fatalf("healed code not persisted to chain db: got %x want %x", got, code)
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

// silentCodePeer accepts requests but never answers them — it models a peer that
// stalls, so a fetch ends only via its timeout or context cancellation.
type silentCodePeer struct{ id string }

func (p *silentCodePeer) ID() string      { return p.id }
func (p *silentCodePeer) Log() log.Logger { return log.New("id", p.id) }

func (p *silentCodePeer) RequestAccountRange(id uint64, root, origin, limit common.Hash, bytes uint64) error {
	return nil
}

func (p *silentCodePeer) RequestStorageRanges(id uint64, root common.Hash, accounts []common.Hash, origin, limit []byte, bytes uint64) error {
	return nil
}

func (p *silentCodePeer) RequestTrieNodes(id uint64, root common.Hash, paths []snap.TrieNodePathSet, bytes uint64) error {
	return nil
}

// RequestByteCodes accepts the request and never delivers a response.
func (p *silentCodePeer) RequestByteCodes(id uint64, hashes []common.Hash, bytes uint64) error {
	return nil
}

// TestRecoverMissingStatelessCodeHonorsCancel proves the self-heal fetch aborts
// promptly when its context is cancelled rather than blocking for the full
// statelessCodeHealTimeout — the property that keeps Cancel()/Terminate() from
// stalling on an outstanding network fetch during the heal-and-retry loop.
func TestRecoverMissingStatelessCodeHonorsCancel(t *testing.T) {
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	syncer := snap.NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	if err := syncer.Register(&silentCodePeer{id: "silent"}); err != nil {
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

// healTestCode is the bytecode the heal-loop tests fetch; its hash is the missing
// code the scripted chain reports.
var healTestCode = []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}

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

func newHealDownloader(t *testing.T, chain BlockChain, servable bool) *Downloader {
	t.Helper()
	syncer := snap.NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	corpus := map[common.Hash][]byte{}
	if servable {
		corpus[crypto.Keccak256Hash(healTestCode)] = healTestCode
	}
	if err := syncer.Register(&fakeCodePeer{id: "p1", syncer: syncer, corpus: corpus}); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	return &Downloader{
		stateDB:    rawdb.NewMemoryDatabase(),
		SnapSyncer: syncer,
		blockchain: chain,
		quitCh:     make(chan struct{}),
		cancelCh:   make(chan struct{}),
	}
}

// TestInsertStatelessWithHeal covers the heal-and-retry loop: a healable miss is
// recovered and retried to success, an unservable miss and a non-code error both
// stop after one attempt, a clean import does not retry, and a miss that never
// clears is bounded by maxStatelessCodeHeals.
func TestInsertStatelessWithHeal(t *testing.T) {
	hash := crypto.Keccak256Hash(healTestCode)
	blocks := types.Blocks{types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})}
	wits := []*stateless.Witness{nil}

	// Servable miss: healed on the first retry.
	chain := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
	d := newHealDownloader(t, chain, true)
	if _, err := d.insertStatelessWithHeal(blocks, wits); err != nil {
		t.Fatalf("expected heal+success, got %v", err)
	}
	if chain.calls != 2 {
		t.Fatalf("expected initial + one retry = 2 inserts, got %d", chain.calls)
	}

	// Unservable miss: no retry, error returned.
	chainU := &fakeStatelessChain{results: []error{&state.MissingCodeError{Hash: hash}}}
	dU := newHealDownloader(t, chainU, false)
	if _, err := dU.insertStatelessWithHeal(blocks, wits); err == nil {
		t.Fatal("expected failure for an unservable missing code")
	}
	if chainU.calls != 1 {
		t.Fatalf("unservable code must not retry: got %d inserts", chainU.calls)
	}

	// Non-code error: surfaced immediately, no heal attempt.
	chainE := &fakeStatelessChain{results: []error{errors.New("boom")}}
	dE := newHealDownloader(t, chainE, true)
	if _, err := dE.insertStatelessWithHeal(blocks, wits); err == nil {
		t.Fatal("expected the non-code error to surface")
	}
	if chainE.calls != 1 {
		t.Fatalf("non-code error must not retry: got %d inserts", chainE.calls)
	}

	// Clean import: a single attempt, no heal.
	chainOK := &fakeStatelessChain{}
	dOK := newHealDownloader(t, chainOK, true)
	if _, err := dOK.insertStatelessWithHeal(blocks, wits); err != nil {
		t.Fatalf("clean import: %v", err)
	}
	if chainOK.calls != 1 {
		t.Fatalf("clean import must not retry: got %d inserts", chainOK.calls)
	}

	// A miss that never clears: retries are bounded by maxStatelessCodeHeals.
	seq := make([]error, maxStatelessCodeHeals+5)
	for i := range seq {
		seq[i] = &state.MissingCodeError{Hash: hash}
	}
	chainB := &fakeStatelessChain{results: seq}
	dB := newHealDownloader(t, chainB, true)
	if _, err := dB.insertStatelessWithHeal(blocks, wits); err == nil {
		t.Fatal("expected bounded failure when the miss never clears")
	}
	if chainB.calls != maxStatelessCodeHeals+1 {
		t.Fatalf("expected initial + %d retries = %d inserts, got %d", maxStatelessCodeHeals, maxStatelessCodeHeals+1, chainB.calls)
	}
}
