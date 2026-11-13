package peerpolicy

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyRepeatedBodyDownloads(t *testing.T) {
	clock := new(mclock.Simulated)
	clock.Run(40 * time.Second)
	tracker := New(clock)
	e := Evidence{Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{1}}, ObjectBytes: []uint64{16 << 20}}
	for range 4 {
		tracker.Observe("a", e)
	}
	assertRisk(t, tracker, "a", 0, "none")
	tracker.Observe("a", e)
	assertRisk(t, tracker, "a", 0, "none")
	tracker.Observe("b", e)
	assertRisk(t, tracker, "b", 0, "none")
	clock.Run(time.Minute)
	for range 3 {
		tracker.Observe("a", e)
	}
	assertRisk(t, tracker, "a", 0, "none")
}

func TestPeerPolicyCompletedBodyVolumeStaysNeutral(t *testing.T) {
	metrics.Enable()
	for _, repeated := range []bool{false, true} {
		tracker := New(new(mclock.Simulated))
		itemsBefore := tracker.metrics.items[BodyReplies].Snapshot().Count()
		bytesBefore := tracker.metrics.bytes[BodyReplies].Snapshot().Count()
		reasonsBefore := tracker.metrics.reasons[ServingVolume].Snapshot().Count()
		for i := byte(0); i < 26; i++ {
			hash := common.Hash{i}
			if repeated {
				hash = common.Hash{1}
			}
			tracker.Observe("peer", Evidence{Family: BodyReplies, Items: 1024, Bytes: 8 << 20,
				Hashes: []common.Hash{hash}, ObjectBytes: []uint64{8 << 20},
				Activity: ActivityEvent{ServedResponses: 1, ServedBytes: 8 << 20}})
		}
		got := tracker.Snapshot("peer")
		require.Zero(t, got.Risk)
		require.Empty(t, got.Windows)
		require.Equal(t, uint64(26), got.Contribution.ServedResponses)
		require.Equal(t, uint64(208<<20), got.Contribution.ServedBytes)
		require.Equal(t, int64(26*1024), tracker.metrics.items[BodyReplies].Snapshot().Count()-itemsBefore)
		require.Equal(t, int64(208<<20), tracker.metrics.bytes[BodyReplies].Snapshot().Count()-bytesBefore)
		require.Equal(t, reasonsBefore, tracker.metrics.reasons[ServingVolume].Snapshot().Count())
	}
}

func TestPeerPolicyBodyManifestLength(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.Observe("a", Evidence{Family: BodyReplies, Hashes: []common.Hash{{1}}})
	assertRisk(t, tracker, "a", 0, "none")
}

func TestPeerPolicyServingRefusals(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		tracker := New(new(mclock.Simulated))
		e := Evidence{Refused: true}
		if repeated {
			e.Hashes = []common.Hash{{1}}
		}
		for range 2 {
			tracker.Observe("peer", e)
		}
		assertRisk(t, tracker, "peer", 0, "none")
		tracker.Observe("peer", e)
		reason := ServingVolume
		if repeated {
			reason = ServingRepetition
		}
		got := tracker.Snapshot("peer")
		if got.Risk != 20 || got.Windows[reason.String()] != 1 {
			t.Fatalf("refused repeat=%v: %+v", repeated, got)
		}
	}
}

func TestPeerPolicyRefusalCountsRequests(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	e := Evidence{Refused: true, Hashes: []common.Hash{{1}, {1}, {1}, {1}}}
	for range 2 {
		tracker.Observe("peer", e)
		assertRisk(t, tracker, "peer", 0, "none")
	}
	tracker.Observe("peer", e)
	assertRisk(t, tracker, "peer", 20, "none")
}

func TestPeerPolicyRefusalHistoryAge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
		risk  uint64
	}{
		{"successive intervals", 2 * windowWidth, 20},
		{"last live interval", (windowCount - 1) * windowWidth, 20},
		{"expired interval", windowCount * windowWidth, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := new(mclock.Simulated)
			clock.Run(40 * time.Second)
			tracker := New(clock)
			event := Evidence{Refused: true, Hashes: []common.Hash{{1}}}
			tracker.Observe("peer", event)
			clock.Run(windowWidth)
			tracker.Observe("peer", event)
			require.Zero(t, tracker.Snapshot("peer").Risk)
			clock.Run(tc.delay - windowWidth)
			tracker.Observe("peer", event)
			got := tracker.Snapshot("peer")
			require.Equal(t, tc.risk, got.Risk)
			require.Equal(t, tc.risk/20, got.Windows[ServingRepetition.String()])
			require.Zero(t, got.Windows[ServingVolume.String()])
		})
	}
}

func TestPeerPolicyRequestOnlyWithinAllowance(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	for range 12 {
		tracker.Observe("reader", Evidence{Family: Requests, Items: allowances[Requests].items, Bytes: allowances[Requests].bytes})
		tracker.Observe("reader", Evidence{Family: BodyReplies, Items: 20_000, Bytes: 208 << 20})
		assertRisk(t, tracker, "reader", 0, "none")
		clock.Run(windowWidth)
	}
	clock.Run(time.Minute)
	assertRisk(t, tracker, "reader", 0, "none")
}

func TestPeerPolicyRequestBytesRemainDiagnostic(t *testing.T) {
	metrics.Enable()
	tracker := New(new(mclock.Simulated))
	before := tracker.metrics.bytes[Requests].Snapshot().Count()
	tracker.Observe("reader", Evidence{Family: Requests, Items: allowances[Requests].items, Bytes: 32 << 20})
	require.Zero(t, tracker.Snapshot("reader").Risk)
	require.Equal(t, int64(32<<20), tracker.metrics.bytes[Requests].Snapshot().Count()-before)
	tracker.Observe("large", Evidence{Family: Requests, Bytes: 1 << 30})
	require.Zero(t, tracker.Snapshot("large").Risk)
	tracker.Observe("reader", Evidence{Family: Requests, Items: 1})
	require.Equal(t, uint64(20), tracker.Snapshot("reader").Risk, "byte exemption must preserve the request-count bound")
}

func TestPeerPolicyRequestOnlyScoreAndExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	body := Evidence{Refused: true, Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{1}}, ObjectBytes: []uint64{16 << 20}}
	for i := uint64(1); i <= 5; i++ {
		for range 5 {
			tracker.Observe("reader", body)
		}
		tracker.Observe("reader", Evidence{Family: Requests, Items: allowances[Requests].items + 1})
		tracker.Observe("reader", Evidence{Family: BodyReplies, Bytes: 208 << 20})
		for range 3 {
			tracker.Observe("reader", Evidence{Refused: true})
		}
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
	body := Evidence{Refused: true, Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{1}}, ObjectBytes: []uint64{16 << 20}}
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
	for i, history := range []*hashHistory{&record.hashes.blocks, &record.hashes.transactions, &record.hashes.refusedBodies} {
		if history.Len() > maxHashes {
			t.Fatalf("history %d exceeds bound", i)
		}
	}
}
