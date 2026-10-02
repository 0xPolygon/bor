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
	return &routedMsgReadWriter{
		primary: primary,
		route:   route,
		reads:   make(chan routedReadResult, 2),
		bulks:   make(map[string]*routedBulkLane),
	}
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

type routedReadResult struct {
	msg Msg
	err error
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

	start   sync.Once
	reads   chan routedReadResult
	bulkMu  sync.RWMutex
	bulks   map[string]*routedBulkLane
	bulkSeq uint64
}

type routedBulkLane struct {
	id      uint64
	channel string
	rw      MsgReadWriter
}

type bulkWriteError struct {
	err       error
	committed bool
}

func (e *bulkWriteError) Error() string { return e.err.Error() }
func (e *bulkWriteError) Unwrap() error { return e.err }

func (rw *routedMsgReadWriter) ReadMsg() (Msg, error) {
	rw.start.Do(func() {
		go rw.readPrimaryLoop()
	})

	result := <-rw.reads
	return result.msg, result.err
}

func (rw *routedMsgReadWriter) WriteMsg(msg Msg) error {
	channel := rw.route(msg.Code)
	lane, ok := rw.bulk(channel)
	if !ok {
		return rw.primary.WriteMsg(msg)
	}
	if msg.Size > bulkMaxMessageSize {
		return rw.writeFallback(msg, channel)
	}
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
	rw.clearBulk(channel, lane.id)
	if closer, ok := lane.rw.(io.Closer); ok {
		err = errors.Join(err, closer.Close())
	}
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
	lane := rw.setBulkChannels(channels, bulk)
	if lane == nil {
		return
	}
	go rw.readBulkLoop(lane)
}

func (rw *routedMsgReadWriter) AttachBulkChannel(channel string, bulk MsgReadWriter) {
	if channel == "" || bulk == nil {
		return
	}
	lane := rw.setBulk(channel, bulk)
	go rw.readBulkLoop(lane)
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

func (rw *routedMsgReadWriter) setBulk(channel string, bulk MsgReadWriter) *routedBulkLane {
	return rw.setBulkChannels([]string{channel}, bulk)
}

func (rw *routedMsgReadWriter) setBulkChannels(channels []string, bulk MsgReadWriter) *routedBulkLane {
	if bulk == nil {
		return nil
	}
	rw.bulkMu.Lock()
	defer rw.bulkMu.Unlock()

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
	rw.bulkSeq++
	lane := &routedBulkLane{
		id:      rw.bulkSeq,
		channel: unique[0],
		rw:      bulk,
	}
	for _, channel := range unique {
		rw.bulks[channel] = lane
	}
	return lane
}

func (rw *routedMsgReadWriter) isCurrentBulk(channel string, id uint64) bool {
	rw.bulkMu.RLock()
	defer rw.bulkMu.RUnlock()

	lane := rw.bulks[channel]
	return lane != nil && lane.id == id
}

func (rw *routedMsgReadWriter) clearBulk(channel string, id uint64) {
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
	rw.clearBulk(channel, bulkID)
}

func (rw *routedMsgReadWriter) readPrimaryLoop() {
	for {
		msg, err := rw.primary.ReadMsg()
		if err != nil {
			rw.reads <- routedReadResult{msg: msg, err: err}
			return
		}
		if err := rw.forwardMsg(msg); err != nil {
			return
		}
	}
}

func (rw *routedMsgReadWriter) readBulkLoop(lane *routedBulkLane) {
	defer lane.closeReader()
	for {
		msg, err := rw.readBulkMsg(lane)
		if !rw.isCurrentBulk(lane.channel, lane.id) {
			return
		}
		if err != nil {
			rw.failBulkRead(lane.channel, lane.id, err)
			return
		}
		if err := rw.forwardMsg(msg); err != nil {
			return
		}
	}
}

func (rw *routedMsgReadWriter) readBulkMsg(lane *routedBulkLane) (Msg, error) {
	msg, err := lane.rw.ReadMsg()
	if err != nil || !rw.isCurrentBulk(lane.channel, lane.id) {
		return msg, err
	}
	// Protocol handlers must never observe a partially received sidecar frame.
	payload, err := readRoutedPayload(msg)
	msg.Payload = bytes.NewReader(payload)
	return msg, err
}

func (lane *routedBulkLane) closeReader() {
	if closer, ok := lane.rw.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			log.Debug("Failed to close bulk lane", "channel", lane.channel, "err", err)
		}
	}
}

func (rw *routedMsgReadWriter) forwardMsg(msg Msg) error {
	if msg.Size == 0 {
		rw.reads <- routedReadResult{msg: msg}
		return nil
	}
	done := make(chan error, 1)
	msg.Payload = &routedPayload{reader: msg.Payload, remaining: msg.Size, done: done}
	rw.reads <- routedReadResult{msg: msg}
	return <-done
}

func isTimeoutError(err error) bool {
	var timeoutErr net.Error
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}
