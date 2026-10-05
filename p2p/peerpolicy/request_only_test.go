package peerpolicy

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestPeerPolicyRequestOnlyWithinAllowance(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	for range 12 {
		tracker.Observe("reader", Evidence{Family: Requests, Items: allowances[Requests].items, Bytes: allowances[Requests].bytes})
		tracker.Observe("reader", Evidence{Family: BodyReplies, Items: allowances[BodyReplies].items, Bytes: allowances[BodyReplies].bytes})
		assertRisk(t, tracker, "reader", 0, "none")
		clock.Run(windowWidth)
	}
	clock.Run(time.Minute)
	assertRisk(t, tracker, "reader", 0, "none")
}

func TestPeerPolicyRequestOnlyScoreAndExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	body := Evidence{Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{1}}, ObjectBytes: []uint64{16 << 20}}
	for i := uint64(1); i <= 5; i++ {
		for range 5 {
			tracker.Observe("reader", body)
		}
		tracker.Observe("reader", Evidence{Family: Requests, Items: allowances[Requests].items + 1})
		tracker.Observe("reader", Evidence{Family: BodyReplies, Bytes: allowances[BodyReplies].bytes + 1})
		tracker.Observe("reader", Evidence{Family: TransactionAnnouncements, Items: 1, Hashes: []common.Hash{{2}}})
		got := tracker.Snapshot("reader")
		if got.Risk != i*20 || got.Windows[ServingRepetition.String()] != i || got.Windows[RequestVolume.String()] != i || got.Windows[ServingVolume.String()] != i {
			t.Fatalf("interval %d: %+v", i, got)
		}
		if i < 5 {
			clock.Run(windowWidth)
		}
	}
	assertRisk(t, tracker, "reader", 100, "jail")
	assertRisk(t, tracker, "another-peer", 0, "none")
	clock.Run(2 * windowWidth)
	assertRisk(t, tracker, "reader", 80, "throttle")
	clock.Run(4 * windowWidth)
	assertRisk(t, tracker, "reader", 0, "none")
}

func TestPeerPolicyRepetitionHistoryByFamily(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	body := Evidence{Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{1}}, ObjectBytes: []uint64{16 << 20}}
	block := Evidence{Family: BlockAnnouncements, Items: 1, Hashes: []common.Hash{{2}}}
	for i := 0; i < 35; i++ {
		if i < 5 {
			tracker.Observe("peer", body)
		}
		tracker.Observe("peer", block)
		churn := Evidence{Family: TransactionAnnouncements, Items: maxHashes, Hashes: make([]common.Hash, maxHashes)}
		for j := range churn.Hashes {
			binary.BigEndian.PutUint64(churn.Hashes[j][:8], uint64(i*maxHashes+j))
		}
		tracker.Observe("peer", churn)
	}
	got := tracker.Snapshot("peer")
	if got.Risk != 20 || got.Windows[ServingRepetition.String()] != 1 || got.Windows[AnnouncementRepetition.String()] != 1 {
		t.Fatalf("unrelated traffic evicted repetition evidence: %+v", got)
	}
	record, ok := tracker.peers.Peek("peer")
	if !ok {
		t.Fatal("missing peer")
	}
	for _, family := range []Family{BlockAnnouncements, TransactionAnnouncements, BodyReplies} {
		if record.hashes[family].Len() > maxHashes {
			t.Fatalf("family %d history exceeds bound", family)
		}
	}
}
