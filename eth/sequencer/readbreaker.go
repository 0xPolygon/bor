package sequencer

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/log"
)

// readFailuresToTrip is how many consecutive unanswered reads open the
// breaker. One is too eager — a single transient error would skip the
// pre-seal mirror check for a whole probe interval — and a larger number
// pays another full read budget per block before it helps.
const readFailuresToTrip = 2

// staleReadsToTrip is how many consecutive reads that reach the live tail at
// an unchanged head open the breaker. A gateway cut from its broker keeps
// answering from a window that no longer advances -- an answer, so the
// silence path never fires -- and the producer, reading the same held height
// every retry, refuses to seal until the fault clears. Treating a tail that
// does not move as one that does not answer keeps production off it.
const staleReadsToTrip = 6

// readProbeInterval is how often an open breaker lets one read through to
// look for the path again. It bounds the recovery delay and the steady-state
// cost of an outage together, and the two pull in opposite directions: the
// probe pays a full read budget when the path is still silent, so probing
// once a second cost most of a block at a one-second cadence and recovered
// only two thirds of the lost throughput on a devnet. Five seconds matches
// the write path's probe cap and puts the cost near a twentieth of that,
// for a recovery delay nobody watching block times would notice. Var for
// tests.
var readProbeInterval = 5 * time.Second

// readProbeTimeout bounds the one read an open breaker lets through. A probe
// asks a single question — does the path answer — so it gets a deadline
// sized for that rather than for returning a useful tail: on a silent path
// the full read budget is spent to learn nothing, and once per interval that
// was the whole of the throughput still missing on a devnet (12 probes of
// 1.25 s across a 60 s outage, a 25 % loss against a measured 27 %).
//
// A path too slow to answer inside it keeps the breaker open, which is the
// right answer anyway: a store that cannot reply in 150 ms has no business
// on the block path. Recovery is unaffected — a healthy store answers a
// Range in single-digit milliseconds.
var readProbeTimeout = 150 * time.Millisecond

// staleHoldMax bounds a staleness hold. A frozen tail is only evidence of a
// broken read path while someone is writing; when this node is the only
// producer, it holds its own publishes for as long as it cannot read, so
// nothing can ever advance the tail and the hold would last forever. Past
// this bound reads resume: a gateway that is still frozen re-trips within
// staleReadsToTrip cheap answered reads, while a sole writer gets the tail
// read it needs to resync and publish. Var for tests.
var staleHoldMax = 3 * readProbeInterval

// errReadPathDown is what a refused read returns. Callers already treat any
// read error as "the tail is unreadable" and take the safe branch, so a
// refusal needs no handling of its own — it only has to not look like
// NOT_FOUND, which is an answer.
var errReadPathDown = errors.New("store read path not answering")

// readBreaker keeps block production off a read path that is not answering.
//
// Every store read on the producer's critical path has a budget, and a
// gateway that accepts the connection without ever replying burns all of it,
// on every block: that is how a consumer-side outage turns into a block
// production slowdown. The reads are advisory — each one's caller already
// has a safe answer for an unreadable tail — so once the path has gone quiet
// the honest thing is to take that answer immediately instead of paying for
// it. One probe per interval finds the path again, which is also what closes
// the breaker: there is no other way to learn that a silent path is back.
type readBreaker struct {
	mu       sync.Mutex
	failures int
	open     bool
	// staleSince is when the current staleness hold opened.
	staleSince time.Time
	// stale marks the breaker open because the tail answers but does not
	// advance, as against open on silence. Only an advancing tail clears it;
	// a bare successful round trip does not, because a frozen tail answers.
	stale    bool
	freeze   int
	lastTail []byte
	// lastSeal is the highest sealed height read up to lastTail, 0 if none.
	lastSeal  uint64
	nextProbe time.Time
	// recoveringUntil suspends the frozen-tail rule; see recovered.
	recoveringUntil time.Time
}

// allow reports whether a read may go out, and whether it is the probe.
// Opening the breaker reserves the slot, so exactly one read per interval
// goes out, and it carries the probe's smaller deadline.
func (b *readBreaker) allow() (allowed, probing bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.open {
		return true, false
	}

	now := time.Now()
	if b.stale && now.Sub(b.staleSince) > staleHoldMax {
		b.open, b.stale, b.failures, b.freeze = false, false, 0, 0
		readBreakerClosed.Inc(1)
		log.Warn("Sequencer store tail still frozen past the hold bound; resuming reads", "held", now.Sub(b.staleSince))

		return true, false
	}

	if now.Before(b.nextProbe) {
		readsSkipped.Inc(1)

		return false, false
	}

	b.nextProbe = now.Add(readProbeInterval)

	return true, true
}

// readBudget is the deadline a read gets: a probe's, or the full one.
func readBudget(probing bool) time.Duration {
	if probing {
		return readProbeTimeout
	}

	return tailReadTimeout
}

// isOpen reports whether reads are being skipped. It does not reserve a
// probe slot, so asking cannot perturb the answer.
func (b *readBreaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.open
}

// answered closes the breaker: the path is alive, whatever it said.
func (b *readBreaker) answered() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stale {
		// A frozen tail still answers; only an advancing tail (observeLiveTail)
		// clears a staleness hold, never a bare successful round trip.
		return
	}

	if b.open {
		readBreakerClosed.Inc(1)
		log.Info("Sequencer store read path is answering again")
	}

	b.failures, b.open = 0, false
}

// silent records a read the path did not answer, and opens the breaker once
// they stop looking like one bad round trip.
func (b *readBreaker) silent() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failures++

	if b.open || b.failures < readFailuresToTrip {
		return
	}

	b.open, b.nextProbe = true, time.Now().Add(readProbeInterval)
	readBreakerOpened.Inc(1)
	log.Warn("Sequencer store read path is not answering; keeping it off the block path",
		"failures", b.failures, "probeEvery", readProbeInterval)
}

// observeLiveTail feeds the breaker the head a read reached at the live tail,
// with the highest sealed height the read saw (0 if none) and our chain head.
// A head that does not move across staleReadsToTrip live reads is a store
// answering with a frozen window -- treated exactly like a silent one, so a
// broker-partitioned gateway that keeps a green light cannot wedge block
// production. Advancement clears the hold; a bare answer cannot.
//
// A frozen tail sealed above our chain is not that wedge: our own sealed
// blocks are on the chain, so it is a dead producer's run, and the next
// producer recovers it. Counting it would close the read path on a node that
// idles before its takeover, and its first build would rebuild the run from
// the pool.
func (b *readBreaker) observeLiveTail(tail []byte, seal, head uint64) {
	if len(tail) == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// A read can reach the tail before it learns the seal under it, so a
	// later read of the same tail still updates the seal.
	if seal != 0 {
		b.lastSeal = seal
	}

	if !bytes.Equal(tail, b.lastTail) {
		b.lastTail = append(b.lastTail[:0], tail...)
		b.unfreezeLocked()

		return
	}

	if time.Now().Before(b.recoveringUntil) || b.lastSeal > head {
		b.freeze = 0

		return
	}

	b.freeze++
	if b.freeze < staleReadsToTrip || b.open {
		return
	}

	b.open, b.stale = true, true
	b.staleSince = time.Now()
	b.nextProbe = b.staleSince.Add(readProbeInterval)
	readBreakerOpened.Inc(1)
	log.Warn("Sequencer store tail is frozen (answering but not advancing); keeping it off the block path",
		"reads", b.freeze, "probeEvery", readProbeInterval)
}

// staleTail returns the frozen head behind a staleness hold, and whether one
// is in force.
func (b *readBreaker) staleTail() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.stale {
		return nil, false
	}

	return append([]byte(nil), b.lastTail...), true
}

// unfreeze lifts a staleness hold: the tail was seen to move.
func (b *readBreaker) unfreeze() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.unfreezeLocked()
}

func (b *readBreaker) unfreezeLocked() {
	b.freeze = 0
	if !b.stale {
		return
	}

	b.open, b.stale, b.failures = false, false, 0
	readBreakerClosed.Inc(1)
	log.Info("Sequencer store tail is advancing again")
}

// recovered suspends the frozen-tail rule while builds recover heights the
// store sealed ahead of us. A dead sealer is exactly a tail that stops
// moving, so the rule would otherwise cut a multi-block recovery off after a
// few heights — yet those reads are not wedging production, they drive it.
// Resetting the count is not enough: idle catch-up reads between two builds
// can reach the limit on their own.
func (b *readBreaker) recovered() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.freeze = 0
	b.recoveringUntil = time.Now().Add(recoverGrace)
}

// isSilent reports an error that means the read path did not answer, as
// against an answer we did not like. NOT_FOUND is an answer: it proves the
// path alive and is a normal result for a height the store never held.
func isSilent(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}

	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Unavailable, codes.Canceled:
		return true
	default:
		return false
	}
}

// admit gates one read on the breaker and reports whether it is the probe.
//
// A probe under a staleness hold first asks the one question that can lift
// it — does anything follow the frozen head? The caller's own read cannot
// be trusted to: it may be the first page of a walk from a position behind
// the tip, and the open breaker refuses every page after the probe, so the
// walk never reaches the live tail and the hold never sees it move. A node
// whose position had fallen behind stayed blind long after the store
// resumed advancing.
func (r *reader) admit(ctx context.Context) (bool, error) {
	allowed, probing := r.breaker.allow()
	if !allowed {
		return false, errReadPathDown
	}

	if tail, stale := r.breaker.staleTail(); probing && stale && !r.tailMoved(ctx, tail) {
		return false, errReadPathDown
	}

	return probing, nil
}

// tailMoved reports whether the store holds anything after tail, lifting
// the staleness hold when it does. A store that no longer knows the head
// at all (wiped, or past its head-index window) has moved too.
func (r *reader) tailMoved(ctx context.Context, tail []byte) bool {
	cctx, cancel := context.WithTimeout(ctx, readProbeTimeout)
	defer cancel()

	resp, err := r.client().Range(cctx, &pb.RangeRequest{
		After: &pb.RangeRequest_Head{Head: tail},
		Limit: 1,
	})
	r.note(err)

	switch {
	case status.Code(err) == codes.NotFound:
	case err != nil:
		return false
	case len(resp.GetEntries()) == 0 && bytes.Equal(resp.GetNext(), tail):
		return false
	}

	r.breaker.unfreeze()

	return true
}
