package peerpolicy

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestPeerPolicyRepeatedBodyDownloads(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	e := Evidence{Family: BodyReplies, Items: 1, Bytes: 16 << 20, Hashes: []common.Hash{{1}}, ObjectBytes: []uint64{16 << 20}}
	for range 4 {
		tracker.Observe("a", e)
	}
	assertRisk(t, tracker, "a", 0, "none")
	tracker.Observe("a", e)
	got := tracker.Snapshot("a")
	if got.Risk != 20 || got.Windows[ServingRepetition.String()] != 1 {
		t.Fatalf("missing repeat evidence: %+v", got)
	}
	tracker.Observe("b", e)
	assertRisk(t, tracker, "b", 0, "none")
	clock.Run(time.Minute)
	tracker.Observe("a", e)
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
