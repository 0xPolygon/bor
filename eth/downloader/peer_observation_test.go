package downloader

import (
	"testing"

	"github.com/ethereum/go-ethereum/log"
)

func TestPeerPolicyDownloaderObservation(t *testing.T) {
	d := &Downloader{peers: newPeerSet()}
	calls := 0
	d.SetFailureObserver(func(id string) {
		if id != "peer" {
			t.Fatalf("wrong peer: %s", id)
		}
		calls++
	})
	peer := newPeerConnection("peer", 69, nil, log.New())
	d.respondToPeer(peer, peerFailureNoRemote, nil)
	if calls != 1 {
		t.Fatal("missing classified failure")
	}
	if len(d.peers.jailed) != 0 {
		t.Fatal("observation changed jail state")
	}
	d.observeFailure("peer", peerFailureReason("arbitrary"))
	if calls != 1 {
		t.Fatal("unknown reason accepted")
	}
	d.SetFailureObserver(nil)
	d.respondToPeer(peer, peerFailureNoRemote, nil)
	if calls != 1 {
		t.Fatal("disabled observer called")
	}
}
