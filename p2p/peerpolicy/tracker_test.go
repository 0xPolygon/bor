package peerpolicy

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

func TestPeerPolicyScoreAndExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.Observe("a", Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, "a", 60, "throttle")
	tracker.Observe("a", Evidence{Reason: InvalidWitnessBody})
	tracker.Observe("a", Evidence{Reason: LegacyJail})
	assertRisk(t, tracker, "a", 60, "throttle")
	clock.Run(windowWidth)
	tracker.Observe("a", Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, "a", 100, "jail")
	clock.Run(5 * windowWidth)
	assertRisk(t, tracker, "a", 60, "throttle")
	clock.Run(windowWidth)
	assertRisk(t, tracker, "a", 0, "none")
	tracker.Observe("a", Evidence{Reason: DownloaderFailure})
	assertRisk(t, tracker, "a", 0, "none")
}

func assertRisk(t *testing.T, tracker *Tracker, id string, risk uint64, action string) {
	t.Helper()
	got := tracker.Snapshot(id)
	if got.Mode != "observe" || got.Risk != risk || got.Action != action {
		t.Fatalf("snapshot = %+v; want risk %d and action %s", got, risk, action)
	}
}

func TestPeerPolicyTrafficBoundaries(t *testing.T) {
	for _, family := range []Family{BlockAnnouncements, TransactionAnnouncements, Transactions, Blocks, Requests} {
		t.Run(fmt.Sprint(family), func(t *testing.T) {
			for _, bytes := range []bool{false, true} {
				tracker := New(new(mclock.Simulated))
				event := Evidence{Family: family, Items: allowances[family].items}
				if bytes {
					event.Items = 0
					event.Bytes = allowances[family].bytes
				}
				tracker.Observe("a", event)
				assertRisk(t, tracker, "a", 0, "none")
				tracker.Observe("a", Evidence{Family: family, Items: 1, Bytes: 1})
				assertRisk(t, tracker, "a", 20, "none")
				tracker.Observe("a", Evidence{Family: family, Items: math.MaxUint64, Bytes: math.MaxUint64})
				assertRisk(t, tracker, "a", 20, "none")
				assertRisk(t, tracker, "b", 0, "none")
			}
		})
	}
}

func TestPeerPolicyRepetition(t *testing.T) {
	clock := new(mclock.Simulated)
	clock.Run(40 * time.Second)
	tracker := New(clock)
	event := Evidence{Family: TransactionAnnouncements, Items: 1, Hashes: []common.Hash{{1}}}
	tracker.Observe("a", event)
	tracker.Observe("a", event)
	for range 32 {
		tracker.Observe("a", event)
	}
	assertRisk(t, tracker, "a", 0, "none")
	tracker.Observe("a", event)
	assertRisk(t, tracker, "a", 20, "none")
	tracker.Observe("b", event)
	assertRisk(t, tracker, "b", 0, "none")
	clock.Run(60 * time.Second)
	for range 34 {
		tracker.Observe("a", event)
	}
	assertRisk(t, tracker, "a", 0, "none")
}

func TestPeerPolicyInvalidDeliveryOwnsPenalty(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.Observe("a", Evidence{Reason: InvalidEncoding, Family: Blocks, Items: math.MaxUint64})
	got := tracker.Snapshot("a")
	if got.Risk != 60 || got.Windows[BlockVolume.String()] != 0 || got.Windows[InvalidEncoding.String()] != 1 {
		t.Fatalf("invalid delivery was charged twice: %+v", got)
	}
	tracker.Observe("a", Evidence{Family: Blocks, Items: 1})
	if tracker.Snapshot("a").Windows[BlockVolume.String()] != 0 {
		t.Fatal("invalid delivery consumed scoring allowance")
	}
}

func TestPeerPolicyBoundedRecords(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for i := 0; i <= maxPeers; i++ {
		tracker.Observe(fmt.Sprint(i), Evidence{Reason: InvalidEncoding})
	}
	if tracker.peers.Len() != maxPeers {
		t.Fatal("peer bound exceeded")
	}
	assertRisk(t, tracker, "0", 0, "none")
	assertRisk(t, tracker, fmt.Sprint(maxPeers), 60, "throttle")
	for i := 0; i < maxHashes*2; i++ {
		var hash common.Hash
		binary.BigEndian.PutUint64(hash[:8], uint64(i))
		tracker.Observe("a", Evidence{Family: BlockAnnouncements, Hashes: []common.Hash{hash}})
	}
	p, _ := tracker.peers.Peek("a")
	if p.hashes.Len() != maxHashes {
		t.Fatal("hash bound exceeded")
	}
}

func TestPeerPolicyConcurrentObservation(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				tracker.Observe("a", Evidence{Reason: InvalidBlock})
				tracker.Snapshot("a")
			}
		})
	}
	wg.Wait()
	assertRisk(t, tracker, "a", 60, "throttle")
}

func TestPeerPolicyRejectsUnknownEvidence(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for _, e := range []Evidence{{Reason: reasonCount}, {Family: familyCount}} {
		tracker.Observe("a", e)
	}
	tracker.Observe("", Evidence{Reason: InvalidBlock})
	if tracker.peers.Len() != 0 {
		t.Fatal("invalid evidence retained")
	}
}

func BenchmarkPeerPolicyObserve(b *testing.B) {
	if !metrics.Enabled() {
		metrics.Enable()
	}
	tracker := New(new(mclock.Simulated))
	event := Evidence{Family: Blocks, Items: 1, Bytes: 1024}
	tracker.Observe("a", event)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tracker.Observe("a", event)
	}
}
