package relay

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type privateTxReservation struct {
	inflight int
}

// Begin hides a transaction during admission. Overlapping submissions share a
// reservation so a failed call cannot expose another call's pending admission.
// The returned callback takes the result of local pool admission.
func (s *PrivateTxStore) Begin(hash common.Hash) func(bool) {
	s.mu.Lock()
	pending := s.pending[hash]
	if pending == nil {
		pending = new(privateTxReservation)
		s.pending[hash] = pending
	}
	pending.inflight++
	s.mu.Unlock()

	var once sync.Once
	return func(accepted bool) {
		once.Do(func() { s.finishSubmission(hash, pending, accepted) })
	}
}

func (s *PrivateTxStore) finishSubmission(hash common.Hash, pending *privateTxReservation, accepted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Inclusion or an explicit purge invalidates callbacks from older attempts.
	if s.pending[hash] != pending {
		return
	}
	if accepted {
		s.txs[hash] = time.Now()
		s.txsAdded.Add(1)
	}
	pending.inflight--
	if pending.inflight == 0 {
		delete(s.pending, hash)
	}
}
