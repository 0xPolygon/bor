package sequencer

import "testing"

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
