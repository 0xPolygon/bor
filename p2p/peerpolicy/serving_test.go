package peerpolicy

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
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

func TestPeerPolicyBodyVolumeChangingHashes(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	for i := byte(0); i < 11; i++ {
		tracker.Observe("a", Evidence{Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{i}}, ObjectBytes: []uint64{16 << 20}})
	}
	got := tracker.Snapshot("a")
	if got.Risk != 20 || got.Windows[ServingVolume.String()] != 1 {
		t.Fatalf("missing serving volume: %+v", got)
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
