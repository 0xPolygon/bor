package peerpolicy

import (
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
