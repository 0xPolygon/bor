package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/log"
)

// The wit2 strike conditions are not all self-incriminating. "signer is not the
// scheduled producer" is a judgement about our own view of the chain, and the
// strike lands on whoever relayed the announcement rather than whoever signed
// it. So a trusted sentry faithfully forwarding a valid announcement can cross
// the threshold through no fault of its own.
//
// Jailing on that is disproportionate twice over: the peer is our own
// infrastructure, and the jail outlasts its stated period because nothing
// re-arms the dial scheduler when one lapses. These tests pin that a trusted
// peer is shed without being jailed, and that an untrusted one still is.

// strikeToThreshold drives a peer past wit2MisbehaviorStrikeLimit so the
// disconnect path runs. Returns how many calls it took.
func strikeToThreshold(h *handler, id string) int {
	for i := 1; ; i++ {
		if h.wit2PeerTracker.strike(id) {
			return i
		}
		if i > wit2MisbehaviorStrikeLimit*4 {
			panic("strike threshold never reached")
		}
	}
}

func TestWit2TrustedPeerShedWithoutJail(t *testing.T) {
	// No t.Parallel: this reads wit2StrikeTrustedShedMeter, which is a
	// package-global counter shared with every other test in this package.

	h := newTestHandler()
	defer h.close()

	const id = "trusted-sentry"

	h.handler.peerTrusted = func(string) bool { return true }

	before := wit2StrikeTrustedShedMeter.Snapshot().Count()

	// Reaching the threshold is what arms the disconnect; the strike ledger is
	// shared with the announce path, so drive it the same way production does.
	strikeToThreshold(h.handler, id)

	if !h.handler.shedWit2StrikeWithoutJail(id, log.New()) {
		t.Fatal("trusted peer was not shed without jail: the guard did not fire")
	}

	if got := wit2StrikeTrustedShedMeter.Snapshot().Count() - before; got != 1 {
		t.Fatalf("trusted-shed meter moved by %d, want 1", got)
	}
}

func TestWit2UntrustedPeerStillJailed(t *testing.T) {
	// No t.Parallel: this reads wit2StrikeTrustedShedMeter, which is a
	// package-global counter shared with every other test in this package.

	h := newTestHandler()
	defer h.close()

	const id = "stranger"

	h.handler.peerTrusted = func(string) bool { return false }

	before := wit2StrikeTrustedShedMeter.Snapshot().Count()

	strikeToThreshold(h.handler, id)

	if h.handler.shedWit2StrikeWithoutJail(id, log.New()) {
		t.Fatal("untrusted peer took the trusted path: the guard is too broad")
	}

	if got := wit2StrikeTrustedShedMeter.Snapshot().Count() - before; got != 0 {
		t.Fatalf("trusted-shed meter moved by %d for an untrusted peer, want 0", got)
	}
}

// A nil predicate must not be read as "everything is trusted", or a handler
// built outside newHandler would silently stop jailing anyone.
func TestWit2NilTrustPredicateDoesNotExemptEveryone(t *testing.T) {
	t.Parallel()

	h := newTestHandler()
	defer h.close()

	h.handler.peerTrusted = nil

	if h.handler.shedWit2StrikeWithoutJail("anyone", log.New()) {
		t.Fatal("nil trust predicate exempted a peer; it must fail closed")
	}
}

// newHandler must install the real resolver, or the guard is dead code in
// production while the tests above still pass against their stubs.
func TestWit2TrustPredicateWiredByDefault(t *testing.T) {
	t.Parallel()

	h := newTestHandler()
	defer h.close()

	if h.handler.peerTrusted == nil {
		t.Fatal("newHandler left peerTrusted nil; the trusted guard would never fire in production")
	}

	// An id with no live peer is not trusted.
	if h.handler.peerTrusted("no-such-peer") {
		t.Fatal("unknown peer id reported as trusted")
	}
}
