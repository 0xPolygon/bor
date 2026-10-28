package p2p

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type bulkChannelReady struct {
	claimed chan struct{}
	done    chan struct{}
	once    sync.Once
	err     error // Published by closing done.
}

var errBulkChannelAbandoned = errors.New("bulk channel acknowledgement abandoned")

// A stored inbound stream is not ready for traffic until a local caller takes
// ownership. The opener waits for this acknowledgement before exposing its lane.
func (s *bulkSession) acknowledgeChannel(conn *quic.Conn, rw *bulkStreamMsgRW, hello bulkChannelHello) error {
	timer := time.NewTimer(bulkChannelOpenTimeout)
	defer timer.Stop()
	var closed <-chan struct{}
	if conn != nil {
		closed = conn.Context().Done()
	}
	var abandoned <-chan struct{}
	if stream, ok := rw.stream.(interface{ Context() context.Context }); ok {
		abandoned = stream.Context().Done()
	}
	select {
	case <-rw.ready.claimed:
		if err := writeBulkControl(rw.stream, hello); err != nil {
			return errors.Join(errBulkChannelAbandoned, err)
		}
		return nil
	case <-timer.C:
		return errBulkChannelTimeout
	case <-abandoned:
		return errBulkChannelAbandoned
	case <-closed:
		return io.EOF
	case <-s.sidecar.closeCh:
		return io.EOF
	}
}

func (s *bulkSession) claimChannel(ctx context.Context, channel string, channelRW MsgReadWriter) (MsgReadWriter, error) {
	rw, ok := channelRW.(*bulkStreamMsgRW)
	if !ok || rw.ready == nil {
		return channelRW, nil
	}
	err := ctx.Err()
	if err == nil {
		rw.ready.once.Do(func() { close(rw.ready.claimed) })
		// Once claimed, the bounded acknowledgement determines ownership. A
		// concurrent caller timeout must not discard a successfully acked lane.
		<-rw.ready.done
		err = rw.ready.err
	}
	if err != nil {
		s.removeChannel(channel, rw)
		return nil, errors.Join(err, rw.Close())
	}
	return rw, nil
}

func (s *bulkSession) removeChannel(channel string, rw MsgReadWriter) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.channels[channel] == rw {
		delete(s.channels, channel)
	}
}
