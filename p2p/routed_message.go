// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package p2p

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/ethereum/go-ethereum/log"
)

const routedDefaultBulkChannel = "__bulk__"

// NewMultiChannelRoutedMsgReadWriter multiplexes reads from the primary lane
// plus any number of named sidecar lanes while routing writes by message code
// to the named lane selected by route.
func NewMultiChannelRoutedMsgReadWriter(primary MsgReadWriter, route func(code uint64) string) MsgReadWriter {
	if route == nil {
		return primary
	}
	rw := &routedMsgReadWriter{
		primary: primary,
		route:   route,
		reads:   make(chan routedRead),
		closed:  make(chan struct{}),
		bulks:   make(map[string]*routedBulkLane),
		buffers: bulkBuffersFor(primary),
	}
	if source, ok := primary.(interface{ Done() <-chan struct{} }); ok && source.Done() != nil {
		go rw.watchPrimary(source.Done())
	}
	return rw
}

// NewRoutedMsgReadWriter multiplexes reads from the primary and bulk lanes while
// routing writes for selected message codes over the bulk lane.
//
// If bulk is nil or shouldRoute is nil, the primary lane is returned unchanged.
func NewRoutedMsgReadWriter(primary MsgReadWriter, bulk MsgReadWriter, shouldRoute func(code uint64) bool) MsgReadWriter {
	return newRoutedMsgReadWriter(primary, bulk, "", shouldRoute)
}

// NewChannelRoutedMsgReadWriter attaches a metrics label to routed bulk-lane
// traffic so fallbacks and read failures can be surfaced per channel.
func NewChannelRoutedMsgReadWriter(primary MsgReadWriter, bulk MsgReadWriter, channel string, shouldRoute func(code uint64) bool) MsgReadWriter {
	return newRoutedMsgReadWriter(primary, bulk, channel, shouldRoute)
}

func newRoutedMsgReadWriter(primary MsgReadWriter, bulk MsgReadWriter, channel string, shouldRoute func(code uint64) bool) MsgReadWriter {
	if shouldRoute == nil {
		return primary
	}
	if channel == "" {
		channel = routedDefaultBulkChannel
	}
	rw := NewMultiChannelRoutedMsgReadWriter(primary, func(code uint64) string {
		if shouldRoute(code) {
			return channel
		}
		return ""
	}).(*routedMsgReadWriter)
	rw.defaultChannel = channel
	if bulk != nil {
		rw.AttachBulkChannel(channel, bulk)
	}
	return rw
}

type routedPayload struct {
	reader    io.Reader
	remaining uint32
	done      chan<- error
	once      sync.Once
}

func (p *routedPayload) Read(buf []byte) (int, error) {
	if p.remaining == 0 {
		p.finish(nil)
		return 0, io.EOF
	}
	buf = buf[:min(uint32(len(buf)), p.remaining)]
	n, err := p.reader.Read(buf)
	p.remaining -= uint32(n)

	if p.remaining == 0 {
		if errors.Is(err, io.EOF) {
			err = nil
		}
		p.finish(err)
	} else if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		p.finish(err)
	}
	return n, err
}

func (p *routedPayload) finish(err error) {
	p.once.Do(func() { p.done <- err })
}

type routedMsgReadWriter struct {
	primary        MsgReadWriter
	defaultChannel string
	route          func(code uint64) string

	start     sync.Once
	reads     chan routedRead
	closed    chan struct{}
	closeOnce sync.Once
	readErr   error
	closeErr  error
	bulkMu    sync.RWMutex
	bulks     map[string]*routedBulkLane
	bulkSeq   uint64
	buffers   *bulkBufferBudget
}

type routedBulkLane struct {
	id        uint64
	channel   string
	rw        MsgReadWriter
	closed    <-chan struct{}
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

type routedRead struct {
	msg        Msg
	laneClosed <-chan struct{}
	done       chan<- error
}

type bulkWriteError struct {
	err       error
	committed bool
}

func (e *bulkWriteError) Error() string { return e.err.Error() }
func (e *bulkWriteError) Unwrap() error { return e.err }

func (rw *routedMsgReadWriter) ReadMsg() (Msg, error) {
	if err := rw.terminalError(); err != nil {
		return Msg{}, err
	}
	rw.start.Do(func() {
		go rw.readPrimaryLoop()
	})

	for {
		select {
		case read := <-rw.reads:
			// A send can win the forwarder's select even after lane cancellation.
			// Reject retired deliveries before the handler takes ownership.
			select {
			case <-read.laneClosed:
				read.done <- io.EOF
				continue
			default:
				return read.msg, rw.terminalError()
			}
		case <-rw.closed:
			return Msg{}, rw.readErr
		}
	}
}

func (rw *routedMsgReadWriter) WriteMsg(msg Msg) error {
	if err := rw.terminalError(); err != nil {
		return err
	}
	channel := rw.route(msg.Code)
	lane, ok := rw.bulk(channel)
	if !ok {
		return rw.primary.WriteMsg(msg)
	}
	if msg.Size > bulkMaxMessageSize {
		return rw.writeFallback(msg, channel)
	}
	// Writes can use the primary lane without copying when buffers are busy.
	if !rw.buffers.tryAcquireWrite(msg.Size) {
		return rw.writeFallback(msg, channel)
	}
	defer rw.buffers.releaseWrite(msg.Size)
	payload, err := readRoutedPayload(msg)
	if err != nil {
		return err
	}
	bulkMsg := msg
	bulkMsg.Payload = bytes.NewReader(payload)
	err = lane.rw.WriteMsg(bulkMsg)
	if err == nil {
		return nil
	}
	rw.clearBulk(lane.id)
	lane.closeReader()
	err = errors.Join(err, lane.closeErr)
	// Only the transport can confirm that replay will not duplicate a frame.
	var writeErr *bulkWriteError
	if !errors.As(err, &writeErr) || writeErr.committed {
		return err
	}
	msg.Payload = bytes.NewReader(payload)
	return rw.writeFallback(msg, channel)
}

func (rw *routedMsgReadWriter) writeFallback(msg Msg, channel string) error {
	bulkSidecarWriteFallbackMeter.Mark(1)
	bulkSidecarStats.markChannelWriteFallback(channel)
	return rw.primary.WriteMsg(msg)
}

func readRoutedPayload(msg Msg) ([]byte, error) {
	if msg.Size > bulkMaxMessageSize {
		return nil, fmt.Errorf("routed message too large: %d", msg.Size)
	}
	payload := make([]byte, msg.Size)
	if msg.Size == 0 {
		return payload, nil
	}
	if _, err := io.ReadFull(msg.Payload, payload); err != nil {
		return nil, fmt.Errorf("buffer routed message payload: %w", err)
	}
	return payload, nil
}

func (rw *routedMsgReadWriter) AttachBulk(bulk MsgReadWriter) {
	rw.AttachBulkChannel(rw.defaultChannel, bulk)
}

func (rw *routedMsgReadWriter) AttachBulkChannels(channels []string, bulk MsgReadWriter) {
	ctx, cancel := context.WithCancel(context.Background())
	lane := rw.setBulkChannels(channels, bulk, ctx.Done(), cancel)
	if lane == nil {
		cancel()
		return
	}
	go rw.readBulkLoop(ctx, lane)
}

func (rw *routedMsgReadWriter) AttachBulkChannel(channel string, bulk MsgReadWriter) {
	rw.AttachBulkChannels([]string{channel}, bulk)
}

func (rw *routedMsgReadWriter) bulk(channel string) (*routedBulkLane, bool) {
	if channel == "" {
		return nil, false
	}
	rw.bulkMu.RLock()
	defer rw.bulkMu.RUnlock()
	lane := rw.bulks[channel]
	if lane == nil {
		return nil, false
	}
	return lane, true
}

func (rw *routedMsgReadWriter) HasBulkChannel(channel string) bool {
	_, ok := rw.bulk(channel)
	return ok
}

func (rw *routedMsgReadWriter) HasBulk() bool {
	rw.bulkMu.RLock()
	defer rw.bulkMu.RUnlock()
	return len(rw.bulks) > 0
}

func (rw *routedMsgReadWriter) setBulkChannels(channels []string, bulk MsgReadWriter, closed <-chan struct{}, cancel context.CancelFunc) *routedBulkLane {
	if bulk == nil {
		return nil
	}
	unique := make([]string, 0, len(channels))
	seen := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if channel == "" {
			continue
		}
		if _, ok := seen[channel]; ok {
			continue
		}
		seen[channel] = struct{}{}
		unique = append(unique, channel)
	}
	if len(unique) == 0 {
		return nil
	}
	lane := &routedBulkLane{
		channel: unique[0],
		rw:      bulk,
		closed:  closed,
		cancel:  cancel,
	}
	if !rw.installBulk(unique, lane) {
		lane.closeReader()
		return nil
	}
	return lane
}

func (rw *routedMsgReadWriter) isCurrentBulk(id uint64) bool {
	rw.bulkMu.RLock()
	defer rw.bulkMu.RUnlock()

	return rw.hasBulkLane(id)
}

func (rw *routedMsgReadWriter) clearBulk(id uint64) {
	rw.bulkMu.Lock()
	defer rw.bulkMu.Unlock()

	for name, candidate := range rw.bulks {
		if candidate.id == id {
			delete(rw.bulks, name)
		}
	}
}

func (rw *routedMsgReadWriter) failBulkRead(channel string, bulkID uint64, err error) {
	if isTimeoutError(err) {
		bulkSidecarReadTimeoutMeter.Mark(1)
		bulkSidecarStats.markChannelReadTimeout(channel)
	} else {
		bulkSidecarReadErrorMeter.Mark(1)
		bulkSidecarStats.markChannelReadError(channel)
	}
	rw.clearBulk(bulkID)
}

func (rw *routedMsgReadWriter) readPrimaryLoop() {
	for {
		msg, err := rw.primary.ReadMsg()
		if err != nil {
			rw.closeWithError(err)
			return
		}
		if err := rw.forwardMsg(msg, nil); err != nil {
			rw.closeWithError(err)
			return
		}
	}
}

func (rw *routedMsgReadWriter) readBulkLoop(ctx context.Context, lane *routedBulkLane) {
	defer lane.closeReader()
	for {
		err := rw.forwardBulkMsg(ctx, lane)
		if !rw.isCurrentBulk(lane.id) {
			return
		}
		if err != nil {
			rw.failBulkRead(lane.channel, lane.id, err)
			return
		}
	}
}

func (rw *routedMsgReadWriter) forwardBulkMsg(ctx context.Context, lane *routedBulkLane) error {
	msg, err := lane.rw.ReadMsg()
	if err != nil || !rw.isCurrentBulk(lane.id) {
		return err
	}
	if msg.Size > bulkMaxMessageSize {
		return fmt.Errorf("routed message too large: %d", msg.Size)
	}
	if err := rw.buffers.acquire(ctx, msg.Size); err != nil {
		return err
	}
	// Protocol handlers must never observe a partially received sidecar frame.
	payload, err := readRoutedPayload(msg)
	if err != nil {
		rw.buffers.release(msg.Size)
		return err
	}
	buffered := &bulkBufferedPayload{reader: *bytes.NewReader(payload), budget: rw.buffers, size: msg.Size}
	defer buffered.release()
	msg.Payload = buffered
	return rw.forwardMsg(msg, lane.closed)
}

func (lane *routedBulkLane) closeReader() {
	lane.closeOnce.Do(func() {
		lane.cancel()
		if closer, ok := lane.rw.(io.Closer); ok {
			lane.closeErr = closer.Close()
			if lane.closeErr != nil {
				log.Debug("Failed to close bulk lane", "channel", lane.channel, "err", lane.closeErr)
			}
		}
	})
}

func (rw *routedMsgReadWriter) forwardMsg(msg Msg, laneClosed <-chan struct{}) error {
	done := make(chan error, 1)
	if msg.Size != 0 {
		msg.Payload = &routedPayload{reader: msg.Payload, remaining: msg.Size, done: done}
	}
	select {
	case <-rw.closed:
		return rw.readErr
	case <-laneClosed:
		return io.EOF
	case rw.reads <- routedRead{msg: msg, laneClosed: laneClosed, done: done}:
	}
	if msg.Size == 0 {
		return nil
	}
	// Once delivered, the complete buffered frame belongs to the handler.
	// Replacing its lane must not truncate it; peer shutdown still cancels it.
	select {
	case <-rw.closed:
		return rw.readErr
	case err := <-done:
		return err
	}
}

func isTimeoutError(err error) bool {
	var timeoutErr net.Error
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}
