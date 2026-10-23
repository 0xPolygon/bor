package wit

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

func TestWitnessObserverConcurrentAdmission(t *testing.T) {
	p := new(Peer)
	p.Observe(peerpolicy.InvalidWitnessBody)
	p.SetObserver(nil)
	p.Observe(peerpolicy.InvalidWitnessBody)
	var calls atomic.Uint64
	observer := func(reason peerpolicy.Reason) {
		require.Equal(t, peerpolicy.InvalidWitnessAnnouncement, reason)
		calls.Add(1)
	}
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			for range 100 {
				p.SetObserver(observer)
				p.Observe(peerpolicy.InvalidWitnessAnnouncement)
			}
		})
	}
	workers.Wait()
	require.Equal(t, uint64(200), calls.Load())
}
