package p2p

import (
	"errors"
	"io"

	"github.com/ethereum/go-ethereum/log"
)

func (rw *routedMsgReadWriter) Done() <-chan struct{} {
	return rw.closed
}

// Close stops pending forwards and closes the lanes. A primary reader without
// io.Closer remains owned by the peer and is interrupted by peer shutdown.
func (rw *routedMsgReadWriter) Close() error {
	rw.closeWithError(io.EOF)
	return rw.closeErr
}

func (rw *routedMsgReadWriter) terminalError() error {
	select {
	case <-rw.closed:
		return rw.readErr
	default:
		return nil
	}
}

func (rw *routedMsgReadWriter) watchPrimary(done <-chan struct{}) {
	select {
	case <-done:
		rw.closeWithError(io.EOF)
	case <-rw.closed:
	}
}

func (rw *routedMsgReadWriter) closeWithError(err error) {
	rw.closeOnce.Do(func() {
		rw.bulkMu.Lock()
		rw.readErr = err
		close(rw.closed)
		lanes := rw.bulks
		rw.bulks = make(map[string]*routedBulkLane)
		rw.bulkMu.Unlock()
		seen := make(map[*routedBulkLane]bool)
		for _, lane := range lanes {
			if !seen[lane] {
				lane.closeReader()
				rw.closeErr = errors.Join(rw.closeErr, lane.closeErr)
				seen[lane] = true
			}
		}
		if closer, ok := rw.primary.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				rw.closeErr = errors.Join(rw.closeErr, err)
				log.Debug("Failed to close primary lane", "err", err)
			}
		}
	})
}

func (rw *routedMsgReadWriter) installBulk(channels []string, lane *routedBulkLane) bool {
	rw.bulkMu.Lock()
	if rw.terminalError() != nil {
		rw.bulkMu.Unlock()
		return false
	}
	rw.bulkSeq++
	lane.id = rw.bulkSeq
	replaced := make(map[*routedBulkLane]struct{})
	for _, channel := range channels {
		if old := rw.bulks[channel]; old != nil {
			replaced[old] = struct{}{}
		}
		rw.bulks[channel] = lane
	}
	for old := range replaced {
		if rw.hasBulkLane(old.id) {
			delete(replaced, old)
		}
	}
	rw.bulkMu.Unlock()
	for old := range replaced {
		old.closeReader()
	}
	return true
}

// The caller holds bulkMu; a shared lane remains live until its last alias is removed.
func (rw *routedMsgReadWriter) hasBulkLane(id uint64) bool {
	for _, lane := range rw.bulks {
		if lane.id == id {
			return true
		}
	}
	return false
}
