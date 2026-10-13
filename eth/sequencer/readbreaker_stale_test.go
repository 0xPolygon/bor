package sequencer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
)

// A gateway cut from its broker keeps answering from a window that no longer
// advances (run 07, "The Frozen Tail"). The silence path never fires -- the
// reads succeed -- so a non-advancing live tail has to open the breaker on
// its own, and only a moving tail may close it.
func TestReadBreakerTripsOnFrozenTail(t *testing.T) {
	var b readBreaker
	frozen := []byte{0xaa, 0xbb, 0xcc}
	for i := 0; i <= staleReadsToTrip; i++ {
		b.observeLiveTail(frozen)
	}
	if !b.isOpen() {
		t.Fatalf("a tail frozen across %d live reads did not open the breaker", staleReadsToTrip)
	}

	b.answered()
	if !b.isOpen() {
		t.Fatal("a successful-but-frozen read cleared the staleness hold")
	}

	b.observeLiveTail([]byte{0x11, 0x22, 0x33})
	if b.isOpen() {
		t.Fatal("an advancing tail did not clear the staleness hold")
	}
}

// An advancing tail must never open the breaker, however many reads.
func TestReadBreakerIgnoresAdvancingTail(t *testing.T) {
	var b readBreaker
	for i := 0; i < staleReadsToTrip*3; i++ {
		b.observeLiveTail([]byte{byte(i), 0x01})
	}
	if b.isOpen() {
		t.Fatal("an advancing tail must never open the staleness breaker")
	}
}

// A dead sealer leaves the tail frozen by definition; a recovery advancing
// one height per block below it must not be cut off by the frozen-tail rule.
func TestReadBreakerRecoveryKeepsFrozenTailClosed(t *testing.T) {
	var b readBreaker
	frozen := []byte{0xaa, 0xbb, 0xcc}

	b.recovered()
	for i := 0; i < staleReadsToTrip*3; i++ {
		b.observeLiveTail(frozen)
	}
	if b.isOpen() {
		t.Fatal("recovery progress must keep a frozen tail from opening the breaker")
	}

	b.mu.Lock()
	b.recoveringUntil = time.Now().Add(-time.Millisecond)
	b.mu.Unlock()

	for i := 0; i < staleReadsToTrip; i++ {
		b.observeLiveTail(frozen)
	}
	if !b.isOpen() {
		t.Fatal("a frozen tail after recovery has ended must still open the breaker")
	}
}

// A staleness hold must be liftable by a node whose read position is behind
// the tip: the probe asks the store directly whether the frozen head moved,
// instead of relying on a multi-page walk the open breaker would cut short.
func TestStaleHoldProbeLiftsOnAdvance(t *testing.T) {
	for _, advanced := range []bool{true, false} {
		t.Run(fmt.Sprintf("advanced=%v", advanced), func(t *testing.T) {
			h := startHarness(t)
			p := newTestPublisher(t, h, nil)
			publishBlock(t, p, 1, common.Hash{0xef}, 1)
			waitHead(t, h, p, 5*time.Second)

			r := classifierPublisher(t, h, nil).read

			r.breaker.mu.Lock()
			r.breaker.open, r.breaker.stale = true, true
			r.breaker.staleSince = time.Now()
			r.breaker.lastTail = h.store.Head().Bytes()
			r.breaker.nextProbe = time.Now().Add(-time.Second)
			r.breaker.mu.Unlock()

			if advanced {
				appendForeignOpen(t, h, 2, common.Hash{0x02})
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			// A read from the floor, as a node far behind the tip would make.
			_, err := r.rangeOnce(ctx, &pb.RangeRequest{Limit: 1})

			if advanced {
				if err != nil || r.breaker.isOpen() {
					t.Fatalf("an advanced tail must lift the hold and serve the read: err=%v open=%v", err, r.breaker.isOpen())
				}

				return
			}

			if !errors.Is(err, errReadPathDown) || !r.breaker.isOpen() {
				t.Fatalf("a still-frozen tail must keep the hold: err=%v open=%v", err, r.breaker.isOpen())
			}
		})
	}
}

// A staleness hold must not outlive its bound: a sole producer holding its
// own publishes can never make the tail move, so only time can lift it.
func TestStaleHoldIsBounded(t *testing.T) {
	var b readBreaker
	frozen := []byte{0xaa, 0xbb, 0xcc}
	for i := 0; i <= staleReadsToTrip; i++ {
		b.observeLiveTail(frozen)
	}

	if allowed, _ := b.allow(); allowed {
		t.Fatal("a fresh staleness hold must refuse reads between probes")
	}

	b.mu.Lock()
	b.staleSince = time.Now().Add(-staleHoldMax - time.Second)
	b.mu.Unlock()

	if allowed, probing := b.allow(); !allowed || probing {
		t.Fatalf("a hold past its bound must resume ordinary reads: allowed=%v probing=%v", allowed, probing)
	}

	if b.isOpen() {
		t.Fatal("a hold past its bound must close the breaker")
	}

	for i := 0; i <= staleReadsToTrip; i++ {
		b.observeLiveTail(frozen)
	}

	if !b.isOpen() {
		t.Fatal("a tail still frozen after the hold lifted must re-trip")
	}
}
