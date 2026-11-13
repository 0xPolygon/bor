package peerpolicy

import (
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"slices"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
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

// oversizedHashes is far past maxSampledHashes yet still fits one ~10 MiB packet.
const oversizedHashes = 260000

func distinctHashes(count int) []common.Hash {
	hashes := make([]common.Hash, count)
	for i := range hashes {
		binary.BigEndian.PutUint64(hashes[i][:8], uint64(i))
	}

	return hashes
}

func sampledSet(history *hashHistory, hashes []common.Hash) (map[common.Hash]bool, int, int) {
	start, step := history.sample(hashes)
	sampled := make(map[common.Hash]bool)

	for i := start; i < len(hashes); i += step {
		sampled[hashes[i]] = true
	}

	return sampled, start, step
}

func assertSampled(t *testing.T, history *hashHistory, hashes []common.Hash) {
	t.Helper()

	sampled, start, step := sampledSet(history, hashes)
	if len(sampled) > maxSampledHashes {
		t.Fatalf("sampled %d hashes from %d (start %d, step %d), want at most %d",
			len(sampled), len(hashes), start, step, maxSampledHashes)
	}

	for _, slot := range history.slots {
		if slot.used && !sampled[slot.hash] {
			t.Fatalf("history retained hash %x outside the sample (start %d, step %d)", slot.hash, start, step)
		}
	}

	if history.Len() == 0 || history.Len() > maxHashes {
		t.Fatalf("history holds %d entries, want 1..%d", history.Len(), maxHashes)
	}
}

func announcementHistory(p *peerRecord, family Family) *hashHistory {
	if family == BlockAnnouncements {
		return &p.hashes.blocks
	}

	return &p.hashes.transactions
}

func TestPeerPolicyOversizedAnnouncementSamplingPerEntrypoint(t *testing.T) {
	hashes := distinctHashes(oversizedHashes)

	for _, family := range []Family{BlockAnnouncements, TransactionAnnouncements} {
		for _, entrypoint := range []string{"Observe", "ObserveWithClass", "ObserveSession"} {
			t.Run(fmt.Sprintf("%s/%v", entrypoint, family), func(t *testing.T) {
				tracker := New(new(mclock.Simulated))
				event := Evidence{Family: family, Hashes: hashes, Items: uint64(len(hashes))}

				switch entrypoint {
				case "Observe":
					tracker.Observe("peer", event)
				case "ObserveWithClass":
					tracker.ObserveWithClass("peer", false, event)
				case "ObserveSession":
					started := tracker.Connected("peer")
					if !tracker.ObserveSession("peer", false, started, event) {
						t.Fatal("session observation was rejected")
					}
				}

				p, ok := tracker.peers.Peek("peer")
				if !ok {
					t.Fatal("no record was created")
				}

				assertSampled(t, announcementHistory(p, family), hashes)
			})
		}
	}
}

func TestPeerPolicyOversizedRefusalSampling(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	hashes := distinctHashes(oversizedHashes)
	tracker.Observe("peer", Evidence{Refused: true, Family: BodyReplies, Hashes: hashes})

	p, ok := tracker.peers.Peek("peer")
	if !ok {
		t.Fatal("no record was created")
	}

	assertSampled(t, &p.hashes.refusedBodies, hashes)
}

func TestPeerPolicySampleBounds(t *testing.T) {
	history := newHashHistory()

	for _, count := range []int{0, 1, 64, maxSampledHashes - 1, maxSampledHashes, maxSampledHashes + 1, 2 * maxSampledHashes, 65536, oversizedHashes} {
		hashes := distinctHashes(count)
		start, step := history.sample(hashes)

		if count <= maxSampledHashes {
			if start != 0 || step != 1 {
				t.Fatalf("count %d: start %d step %d, want every hash visited", count, start, step)
			}

			continue
		}

		if start < 0 || start >= step {
			t.Fatalf("count %d: start %d outside [0,%d)", count, start, step)
		}

		visits := (count-1-start)/step + 1
		if visits > maxSampledHashes {
			t.Fatalf("count %d: %d visits, want at most %d", count, visits, maxSampledHashes)
		}
		if count%maxSampledHashes == 0 && visits != maxSampledHashes {
			t.Fatalf("count %d: sampled only %d hashes despite an evenly divisible packet", count, visits)
		}
		// A sample that stops early would leave the tail of a packet unobservable.
		if last := start + (visits-1)*step; last < count-step {
			t.Fatalf("count %d: last visited index %d leaves more than one step unvisited", count, last)
		}
	}
}

func TestPeerPolicyOversizedAnnouncementTotalsUnchanged(t *testing.T) {
	metrics.Enable()

	tracker := New(new(mclock.Simulated))
	hashes := distinctHashes(oversizedHashes)
	bytes := uint64(len(hashes)) * 40

	items := tracker.metrics.items[TransactionAnnouncements].Snapshot().Count()
	traffic := tracker.metrics.bytes[TransactionAnnouncements].Snapshot().Count()

	tracker.Observe("peer", Evidence{
		Family: TransactionAnnouncements, Hashes: hashes, Items: uint64(len(hashes)), Bytes: bytes,
	})

	if got := tracker.metrics.items[TransactionAnnouncements].Snapshot().Count() - items; got != int64(len(hashes)) {
		t.Fatalf("item metric advanced by %d, want the full packet %d", got, len(hashes))
	}

	if got := tracker.metrics.bytes[TransactionAnnouncements].Snapshot().Count() - traffic; got != int64(bytes) {
		t.Fatalf("byte metric advanced by %d, want the full packet %d", got, bytes)
	}

	got := tracker.Snapshot("peer")
	if got.Risk != 20 || got.Windows[AnnouncementVolume.String()] != 1 {
		t.Fatalf("oversized packet lost its volume finding: %+v", got)
	}
}

func TestPeerPolicyAnnouncementRepetitionAtSampleBoundary(t *testing.T) {
	for _, count := range []int{maxSampledHashes, maxSampledHashes + 1} {
		tracker := New(new(mclock.Simulated))
		hashes := distinctHashes(count)
		event := Evidence{Family: TransactionAnnouncements, Hashes: hashes}

		for range 3 {
			tracker.Observe("peer", event)
		}

		p, _ := tracker.peers.Peek("peer")
		_, start, step := sampledSet(&p.hashes.transactions, hashes)

		if count <= maxSampledHashes {
			if start != 0 || step != 1 {
				t.Fatalf("count %d: start %d step %d, want every hash visited", count, start, step)
			}
		} else if step != 2 {
			t.Fatalf("count %d: step %d, want the sample to engage", count, step)
		}

		if got := tracker.Snapshot("peer"); got.Windows[AnnouncementRepetition.String()] != 1 {
			t.Fatalf("count %d: lost the repetition finding: %+v", count, got)
		}
	}
}

func TestPeerPolicyOversizedDistinctAnnouncementIsNotRepetition(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	hashes := distinctHashes(oversizedHashes)
	tracker.Observe("peer", Evidence{Family: TransactionAnnouncements, Hashes: hashes})

	if got := tracker.Snapshot("peer"); got.Risk != 0 || got.Windows[AnnouncementRepetition.String()] != 0 {
		t.Fatalf("sampling invented a repeat: %+v", got)
	}
}

func TestPeerPolicyProtectedPeerSkipsOversizedAnnouncement(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.SetExempt("peer", true)
	hashes := distinctHashes(oversizedHashes)
	tracker.ObserveWithClass("peer", true, Evidence{
		Family: TransactionAnnouncements, Hashes: hashes, Items: uint64(len(hashes)),
	})

	p, _ := tracker.peers.Peek("peer")
	if p.hashes.transactions.Len() != 0 {
		t.Fatalf("protected peer touched the history: %d entries", p.hashes.transactions.Len())
	}

	assertRisk(t, tracker, "peer", 0, "none")
}

func TestPeerPolicyAnnouncementBufferOwnership(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	hashes := distinctHashes(oversizedHashes)
	want := slices.Clone(hashes)
	started := tracker.Connected("peer")

	for _, event := range []Evidence{
		{Family: BlockAnnouncements, Hashes: hashes, Items: uint64(len(hashes))},
		{Family: TransactionAnnouncements, Hashes: hashes, Items: uint64(len(hashes))},
		{Refused: true, Family: BodyReplies, Hashes: hashes},
	} {
		tracker.Observe("peer", event)
		tracker.ObserveWithClass("peer", false, event)
		tracker.ObserveSession("peer", false, started, event)
	}

	if !slices.Equal(hashes, want) {
		t.Fatal("observation mutated the caller's hash slice")
	}

	p, _ := tracker.peers.Peek("peer")
	retained := p.hashes

	for i := range hashes {
		hashes[i] = common.Hash{0xff}
	}

	if p.hashes != retained {
		t.Fatal("retained history aliased the caller's buffer")
	}
}
