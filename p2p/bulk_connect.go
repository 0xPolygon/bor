package p2p

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	bulkConnectionAttempts       = 2
	bulkConnectionAttemptTimeout = bulkDialTimeout + bulkAuthTimeout
	bulkConnectionRetryDelay     = 100 * time.Millisecond
	// Allow both preferred attempts, a refusal wait, and a full reverse dial.
	bulkConnectionSetupTimeout = (bulkConnectionAttempts+2)*bulkConnectionAttemptTimeout + 2*bulkConnectionRetryDelay
)

// Wait for the preferred dial before trying or retrying the reverse direction.
// Tests shorten this delay; admission arbitration remains the same.
var bulkFallbackGrace = bulkConnectionAttemptTimeout

func (s *bulkSession) ensureConn(ctx context.Context) (*quic.Conn, error) {
	// Connection setup has its own budget; the lane's five-second window starts
	// only after it succeeds. A failed shared attempt leaves one bounded retry.
	ctx, cancel := context.WithTimeout(ctx, bulkConnectionSetupTimeout)
	defer cancel()
	var err error
	for attempt := 0; attempt < bulkConnectionAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var conn *quic.Conn
		conn, err = s.connectOnce(ctx)
		if err == nil {
			return conn, nil
		}
		// Only the remote can connect when its endpoint is missing locally.
		if errors.Is(err, errBulkSidecarNoQUIC) {
			return s.waitIncomingConn(ctx)
		}
		if attempt+1 < bulkConnectionAttempts {
			if retryErr := s.waitConnectionRetry(ctx); retryErr != nil {
				return nil, retryErr
			}
		}
	}
	// Outgoing failures still leave the reverse direction available.
	return s.waitIncomingConn(ctx)
}

// A zero grace must not turn refused dials into a busy loop.
func (s *bulkSession) waitRefusedRetry(ctx context.Context) error {
	s.lock.Lock()
	if s.closed {
		s.lock.Unlock()
		return io.EOF
	}
	ready := s.connReadyLocked()
	s.lock.Unlock()
	delay := bulkFallbackGrace
	if delay < bulkConnectionRetryDelay {
		delay = bulkConnectionRetryDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.sidecar.closeCh:
		return io.EOF
	case <-ready:
	case <-timer.C:
	}
	return nil
}

func (s *bulkSession) waitConnectionRetry(ctx context.Context) error {
	s.lock.Lock()
	if s.closed {
		s.lock.Unlock()
		return io.EOF
	}
	ready := s.connReadyLocked()
	s.lock.Unlock()
	timer := time.NewTimer(bulkConnectionRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.sidecar.closeCh:
		return io.EOF
	case <-ready:
	case <-timer.C:
	}
	return nil
}
