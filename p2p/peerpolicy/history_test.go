package peerpolicy

import (
	"encoding/binary"
	"hash/maphash"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestPeerPolicyLargeRepetitionHistory(t *testing.T) {
	for _, family := range []Family{TransactionAnnouncements, BlockAnnouncements, BodyReplies} {
		clock := new(mclock.Simulated)
		tracker := New(clock)
		hashes := make([]common.Hash, 4096)
		for i := range hashes {
			binary.BigEndian.PutUint64(hashes[i][:8], uint64(i))
		}
		event := Evidence{Family: family, Hashes: hashes, Refused: family == BodyReplies}
		for range 3 {
			for start := 0; start < len(hashes); start += 64 {
				event.Hashes = hashes[start : start+64]
				tracker.Observe("peer", event)
			}
		}
		reason := AnnouncementRepetition
		if family == BodyReplies {
			reason = ServingRepetition
		}
		got := tracker.Snapshot("peer")
		if got.Risk != 20 || got.Windows[reason.String()] != 1 {
			t.Fatalf("family %v: %+v", family, got)
		}
		clock.Run(time.Minute)
		tracker.Observe("peer", event)
		assertRisk(t, tracker, "peer", 0, "none")
	}
}

func TestPeerPolicyDistinctHistory(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for i := 0; i < 10000; i++ {
		var hash common.Hash
		binary.BigEndian.PutUint64(hash[:8], uint64(i))
		tracker.Observe("peer", Evidence{Family: TransactionAnnouncements, Hashes: []common.Hash{hash}})
	}
	assertRisk(t, tracker, "peer", 0, "none")
}

func TestPeerPolicyExemptClass(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	tracker.SetExempt("peer", true)
	for _, reason := range []Reason{InvalidBlock, RateLimited, LegacyJail} {
		tracker.Observe("peer", Evidence{Reason: reason})
	}
	assertRisk(t, tracker, "peer", 0, "none")
	tracker.SetExempt("peer", false)
	tracker.Observe("peer", Evidence{Reason: RateLimited})
	assertRisk(t, tracker, "peer", 20, "none")
}

func TestPeerPolicyClassIdentityBounds(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for _, size := range []int{0, 128, 129} {
		id := string(make([]byte, size))
		tracker.SetExempt(id, true)
		_, exists := tracker.peers.Peek(id)
		if exists != (size == 128) {
			t.Fatalf("identity size %d retained=%v", size, exists)
		}
	}
}

func TestPeerPolicyHistoryExpiryBoundary(t *testing.T) {
	history := newHashHistory()
	low := common.Hash{}
	lowRank := maphash.Bytes(history.seed, low[:])
	var high common.Hash
	for i := uint64(1); ; i++ {
		binary.BigEndian.PutUint64(high[:8], i)
		rank := maphash.Bytes(history.seed, high[:])
		if rank%maxHashes != lowRank%maxHashes {
			continue
		}
		if rank < lowRank {
			low, high = high, low
		}
		break
	}
	history.Add(&low, announcement{tick: 10, count: 3})
	history.Add(&high, announcement{tick: 15, count: 1})
	if _, ok := history.Get(&high); ok {
		t.Fatal("live lower rank was replaced")
	}
	history.Add(&high, announcement{tick: 16, count: 1})
	if entry, ok := history.Get(&high); !ok || entry.count != 1 {
		t.Fatal("expired slot was not replaced at boundary")
	}
}

func TestPeerPolicyHistoryCapacity(t *testing.T) {
	history := newHashHistory()
	var occupied [maxHashes]bool
	for i := uint64(0); i < 1<<20 && history.Len() < maxHashes; i++ {
		var hash common.Hash
		binary.BigEndian.PutUint64(hash[:8], i)
		slot := maphash.Bytes(history.seed, hash[:]) % maxHashes
		if occupied[slot] {
			continue
		}
		occupied[slot] = true
		history.Add(&hash, announcement{count: 1})
	}
	if got := history.Len(); got != maxHashes {
		t.Fatalf("distinct slots: %d, want %d", got, maxHashes)
	}
}
