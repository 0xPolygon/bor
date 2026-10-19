package peerpolicy

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestPeerPolicyObserveWithClass(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.ObserveWithClass("peer", false, Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, "peer", 60, "throttle")
	tracker.ObserveWithClass("peer", true, Evidence{Reason: InvalidBlock})
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, "peer", 0, "none")
	tracker.ObserveWithClass("peer", false, Evidence{Reason: RateLimited})
	assertRisk(t, tracker, "peer", 20, "none")
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, "peer", 60, "throttle")
}

func TestPeerPolicyObserveWithClassClearsHistory(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	hashes := []common.Hash{{1}}
	for range 2 {
		tracker.ObserveWithClass("peer", false, Evidence{Family: BlockAnnouncements, Hashes: hashes})
		tracker.ObserveWithClass("peer", false, Evidence{Family: TransactionAnnouncements, Hashes: hashes})
		tracker.ObserveWithClass("peer", false, Evidence{Refused: true, Hashes: hashes})
	}
	tracker.ObserveWithClass("peer", true, Evidence{Reason: InvalidBlock})
	tracker.ObserveWithClass("peer", false, Evidence{Refused: true, Hashes: hashes})
	assertRisk(t, tracker, "peer", 0, "none")
	p, _ := tracker.peers.Peek("peer")
	if p.hashes.blocks.Len() != 0 || p.hashes.transactions.Len() != 0 || p.refusalSequence != 1 {
		t.Fatal("class change retained repetition evidence")
	}
	if entry, ok := p.hashes.refusedBodies.Get(hashes[0]); !ok || entry.count != 1 {
		t.Fatal("refused-body history was not reset")
	}
}

func TestPeerPolicyObserveWithClassBounds(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for _, id := range []string{"", strings.Repeat("x", 129)} {
		tracker.ObserveWithClass(id, true, Evidence{Reason: InvalidBlock})
	}
	for _, event := range []Evidence{{Reason: reasonCount}, {Family: familyCount}} {
		tracker.ObserveWithClass("peer", true, event)
	}
	if tracker.peers.Len() != 0 {
		t.Fatal("invalid evidence retained a peer")
	}
	id := strings.Repeat("x", 128)
	tracker.ObserveWithClass(id, false, Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, id, 60, "throttle")
}

func TestPeerPolicyConcurrentClasses(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 100 {
				id := fmt.Sprint(i % 32)
				tracker.ObserveWithClass(id, worker%2 == 0, Evidence{Reason: InvalidBlock})
				tracker.Observe(id, Evidence{Reason: RateLimited})
				tracker.peerRisks()
			}
		})
	}
	wg.Wait()
	for i := range 32 {
		id := fmt.Sprint(i)
		tracker.ObserveWithClass(id, true, Evidence{Reason: InvalidBlock})
		tracker.Observe(id, Evidence{Reason: InvalidBlock})
		assertRisk(t, tracker, id, 0, "none")
	}
}
