package p2p

import (
	"context"
	"io"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	bulkConnectionAttempts       = 2
	bulkConnectionAttemptTimeout = bulkDialTimeout + bulkAuthTimeout
	bulkConnectionRetryDelay     = 100 * time.Millisecond
	bulkConnectionSetupTimeout   = bulkConnectionAttempts*bulkConnectionAttemptTimeout + bulkConnectionRetryDelay
)

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
		if attempt+1 < bulkConnectionAttempts {
			if retryErr := s.waitConnectionRetry(ctx); retryErr != nil {
				return nil, retryErr
			}
		}
	}
	return nil, err
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
