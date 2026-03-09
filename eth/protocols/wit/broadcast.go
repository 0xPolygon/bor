package wit

import (
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

var witnessSendTimer = metrics.NewRegisteredTimer("wit/peer/send_witness_duration", nil)

// broadcastWitness is a write loop that multiplexes witness and witness announcements
// to the remote peer. The goal is to have an async writer that does not lock up
// node internals and at the same time rate limits queued data.
func (p *Peer) broadcastWitness() {
	defer p.logger.Info("witness propagator stopped")

	for {
		select {
		case witness := <-p.queuedWitness:
			start := time.Now()
			if err := p.sendNewWitness(witness); err != nil {
				return
			}
			witnessSendTimer.Update(time.Since(start))
			p.logger.Debug("propagated witness", "hash", witness.Header().Hash())

		case packet := <-p.queuedWitnessAnns:
			if err := p.sendNewWitnessHashes(packet); err != nil {
				log.Debug("failed to send new witness hashes", "error", err)
				return
			}
			p.logger.Debug("propagated witness hashes", "hashes", packet.Hashes, "numbers", packet.Numbers)

		case <-p.term:
			return
		}
	}
}
