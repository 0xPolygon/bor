package sequencer

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ethereum/go-ethereum/log"
)

// readFailuresToTrip is how many consecutive unanswered reads open the
// breaker. One is too eager — a single transient error would skip the
// pre-seal mirror check for a whole probe interval — and a larger number
// pays another full read budget per block before it helps.
const readFailuresToTrip = 2

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
	mu        sync.Mutex
	failures  int
	open      bool
	nextProbe time.Time
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
