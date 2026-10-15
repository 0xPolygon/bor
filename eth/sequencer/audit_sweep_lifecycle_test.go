package sequencer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestAuditPassSweepsWithInvalidEndpoint(t *testing.T) {
	h := startExecHarness(t)
	c := newAuditTestConsumer(h)
	c.endpoint = "unknown-scheme://%%"
	db := h.chain.DB()
	if err := rawdb.WritePreconfAuditedThrough(db, 1); err != nil {
		t.Fatal(err)
	}
	c.persistServed(1, 3, common.Hash{0xab})
	c.runAuditPass(t.Context())
	wantServedMismatch(t, db, 1, h.chain.CurrentBlock().Number.Uint64())
	if got := auditedThrough(t, db); got != 1 {
		t.Fatalf("watermark = %d, want 1 while store is unavailable", got)
	}
}

type auditLookupHook struct {
	auditChain
	beforeLookup func()
}

func (c auditLookupHook) GetBlockByNumber(height uint64) *types.Block {
	c.beforeLookup()
	return c.auditChain.GetBlockByNumber(height)
}

func TestAuditSerializesCommitmentReplacement(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "sweep"}[sweep], func(t *testing.T) {
			testAuditCommitmentReplacement(t, sweep)
		})
	}
}

func testAuditCommitmentReplacement(t *testing.T, sweep bool) {
	t.Helper()
	h := startExecHarness(t)
	c := newAuditTestConsumer(h)
	db := h.chain.DB()
	chain, sealed := auditFixture(t, 7)
	sealed[7] = testHeader(8, common.Hash{})
	chain.blocks[7] = canonicalBlock(7, servedTxs(3))
	c.persistServed(7, 3, servedDigest(7, servedTxs(3)))
	if err := rawdb.WritePreconfAuditedThrough(db, 7); err != nil {
		t.Fatal(err)
	}
	var writer sync.WaitGroup
	started := make(chan struct{})
	hook := auditLookupHook{auditChain: chain, beforeLookup: func() {
		if c.servedMu.TryLock() {
			c.servedMu.Unlock()
			t.Error("audit read and delete are not serialized with serving")
			c.persistServed(7, 4, common.Hash{0xcd})
			return
		}
		writer.Add(1)
		go func() {
			defer writer.Done()
			close(started)
			c.persistServed(7, 4, common.Hash{0xcd})
		}()
		<-started
	}}
	a := &auditor{db: db, chain: hook, servedMu: &c.servedMu, fetch: fetchFrom(t, sealed)}
	if sweep {
		if _, err := a.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	} else if err := a.auditHeightInto(t.Context(), 7, new(auditSummary)); err != nil {
		t.Fatal(err)
	}
	writer.Wait()
	count, digest, present, err := rawdb.ReadPreconfServed(db, 7)
	if err != nil || !present || count != 4 || digest != (common.Hash{0xcd}) {
		t.Fatalf("replacement lost: count=%d digest=%x present=%v err=%v", count, digest, present, err)
	}
}

func TestAuditSweepObservesCancellationBetweenCommitments(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, _ := auditFixture(t, 12)
	seedServedMismatch(t, db, chain, 7)
	seedServedMismatch(t, db, chain, 8)
	if err := rawdb.WritePreconfAuditedThrough(db, 12); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := &auditor{db: db, chain: auditLookupHook{auditChain: chain, beforeLookup: cancel}}
	if _, err := a.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want context.Canceled", err)
	}
	if _, _, present, err := rawdb.ReadPreconfServed(db, 8); err != nil || !present {
		t.Fatalf("judged next commitment after cancellation: present=%v err=%v", present, err)
	}
}

func TestAuditRetentionSweepStopsOnCancel(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)
	seedServedMismatch(t, db, chain, 7)
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := &auditor{
		db: db, chain: auditLookupHook{auditChain: chain, beforeLookup: cancel},
		fetch: fetchFrom(t, sealed), oldest: oldestFrom(new(int), servedWindow(10)),
	}
	if _, err := a.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want context.Canceled", err)
	}
	if got := auditedThrough(t, db); got != 4 {
		t.Fatalf("watermark = %d, want 4 after canceled retention sweep", got)
	}
}

func TestAuditRetentionSweepStopsAtFinality(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)
	seedServedMismatch(t, db, chain, 9)
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatal(err)
	}
	a := &auditor{
		db: db, chain: chain, fetch: fetchFrom(t, sealed),
		oldest:    oldestFrom(new(int), servedWindow(15)),
		finalized: func() (uint64, bool) { return 8, true },
	}
	if _, err := a.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, present, err := rawdb.ReadPreconfServed(db, 9); err != nil || !present {
		t.Fatalf("judged above finality: present=%v err=%v", present, err)
	}
	if records := rawdb.ReadInvalidPreconfsInRange(db, 9, 9); len(records) != 0 {
		t.Fatalf("recorded unfinalized height: %v", records)
	}
}

func TestAuditRetentionSweepExcludesForwardRange(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WritePreconfServed(db, 10, 3, servedDigest(10, servedTxs(3))); err != nil {
		t.Fatal(err)
	}
	delete(sealed, 10)
	a := &auditor{
		db: db, chain: chain, fetch: fetchFrom(t, sealed),
		oldest: oldestFrom(new(int), servedWindow(10)),
	}
	summary, err := a.run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if summary.unknown != 1 {
		t.Fatalf("unknown = %d, want one visit to the unjudgeable forward height", summary.unknown)
	}
}

func TestAuditBlocksLiveCommitmentMutations(t *testing.T) {
	for _, persist := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical cleanup", true: "serving"}[persist], func(t *testing.T) {
			h := startExecHarness(t)
			c := newAuditTestConsumer(h)
			c.servedMu.Lock()
			started, done := make(chan struct{}), make(chan struct{})
			go func() {
				close(started)
				if persist {
					c.persistServed(7, 3, common.Hash{0xab})
				} else {
					c.markCanonicalHeadAudited()
				}
				close(done)
			}()
			<-started
			select {
			case <-done:
				t.Error("live commitment operation bypassed ongoing audit reconciliation")
			case <-time.After(20 * time.Millisecond):
			}
			c.servedMu.Unlock()
			<-done
		})
	}
}
