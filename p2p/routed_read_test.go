package p2p

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
)

type closableScriptedRW struct {
	*stressMsgRW
	closeErr error
}

func (rw *closableScriptedRW) Close() error {
	rw.stressMsgRW.Close()
	return rw.closeErr
}

func assertRoutedPrimaryRead(t *testing.T, primary MsgWriter, routed MsgReader) {
	t.Helper()
	sent := make(chan error, 1)
	go func() { sent <- SendItems(primary, 17, uint64(42)) }()
	msg := readRoutedTestMessage(t, routed)
	require.Equal(t, uint64(17), msg.Code)
	var payload []uint64
	require.NoError(t, msg.Decode(&payload))
	require.Equal(t, []uint64{42}, payload)
	select {
	case err := <-sent:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("primary send remained blocked")
	}
}

func readRoutedTestMessage(t *testing.T, rw MsgReader) Msg {
	t.Helper()
	results := make(chan scriptedResult, 1)
	go func() {
		msg, err := rw.ReadMsg()
		results <- scriptedResult{msg: msg, err: err}
	}()
	select {
	case result := <-results:
		require.NoError(t, result.err)
		return result.msg
	case <-time.After(time.Second):
		t.Fatal("message read remained blocked")
		return Msg{}
	}
}

func TestRoutedBulkReadFailureClosesLane(t *testing.T) {
	for _, test := range []struct {
		name   string
		result scriptedResult
	}{
		{"header timeout", scriptedResult{err: timeoutErr{}}},
		{"header error", scriptedResult{err: io.ErrUnexpectedEOF}},
		{"payload timeout", scriptedResult{msg: Msg{Size: 2, Payload: &partialTimeoutReader{}}}},
		{"payload error", scriptedResult{msg: Msg{Size: 2, Payload: &partialErrorReader{}}}},
		{"payload EOF", scriptedResult{msg: Msg{Size: 2, Payload: bytes.NewReader([]byte{0xc0})}}},
		{"oversized", scriptedResult{msg: Msg{Size: bulkMaxMessageSize + 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			primaryApp, primaryNet := MsgPipe()
			defer primaryApp.Close()
			bulk := &closableScriptedRW{stressMsgRW: newStressMsgRW(), closeErr: errPartialPayload}
			defer bulk.Close()
			rw := NewMultiChannelRoutedMsgReadWriter(primaryNet, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
			rw.AttachBulkChannels([]string{"bulk", "control"}, bulk)
			bulk.results <- test.result
			select {
			case <-bulk.closed:
			case <-time.After(time.Second):
				t.Fatal("failed lane was not closed")
			}
			require.False(t, rw.HasBulk())
			assertRoutedPrimaryRead(t, primaryApp, rw)
			sent := make(chan error, 1)
			go func() { sent <- SendItems(rw, 2, uint64(22)) }()
			msg := readRoutedTestMessage(t, primaryApp)
			require.NoError(t, msg.Discard())
			require.NoError(t, <-sent)
		})
	}
}

func TestRoutedBulkReplacementDuringRead(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "failed"}[failure], func(t *testing.T) {
			primaryApp, primaryNet := MsgPipe()
			defer primaryApp.Close()
			old := &closableScriptedRW{stressMsgRW: newStressMsgRW()}
			defer old.Close()
			rw := NewChannelRoutedMsgReadWriter(primaryNet, old, "bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			old.results <- scriptedResult{msg: Msg{Code: 2, Size: 2, Payload: reader}}
			written := make(chan error, 1)
			go func() { _, err := writer.Write([]byte{0xc1}); written <- err }()
			select {
			case err := <-written:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("payload buffering did not start")
			}
			next := &closableScriptedRW{stressMsgRW: newStressMsgRW()}
			defer next.Close()
			rw.AttachBulkChannel("bulk", next)
			if failure {
				require.NoError(t, writer.CloseWithError(errPartialPayload))
			} else {
				_, err := writer.Write([]byte{0x80})
				require.NoError(t, err)
			}
			select {
			case <-old.closed:
			case <-time.After(time.Second):
				t.Fatal("replaced lane was not closed")
			}
			require.True(t, rw.HasBulkChannel("bulk"))
			next.PushMsg(3)
			msg := readRoutedTestMessage(t, rw)
			require.Equal(t, uint64(3), msg.Code)
			require.NoError(t, msg.Discard())
			select {
			case <-next.closed:
				t.Fatal("replacement lane was closed")
			default:
			}
		})
	}
}

func TestRoutedBulkPartialFrame(t *testing.T) {
	for _, size := range []int{5, bulkFrameHeaderSize + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			primaryApp, primaryNet := MsgPipe()
			defer primaryApp.Close()
			bulk := &bulkStreamMsgRW{stream: local, log: log.New()}
			rw := NewRoutedMsgReadWriter(primaryNet, bulk, func(uint64) bool { return true }).(*routedMsgReadWriter)
			frame := make([]byte, bulkFrameHeaderSize+1)
			binary.BigEndian.PutUint32(frame[8:12], 2)
			written := make(chan error, 1)
			go func() { _, err := remote.Write(frame[:size]); written <- err }()
			select {
			case err := <-written:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("frame read remained blocked")
			}
			require.NoError(t, remote.Close())
			require.Eventually(t, func() bool { return !rw.HasBulk() }, time.Second, time.Millisecond)
			assertRoutedPrimaryRead(t, primaryApp, rw)
		})
	}
}

func TestRoutedPrimaryStreamsPayload(t *testing.T) {
	primary := newStressMsgRW()
	defer primary.Close()
	rw := NewRoutedMsgReadWriter(primary, nil, func(uint64) bool { return false })
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	primary.results <- scriptedResult{msg: Msg{Size: 1, Payload: reader}}
	msg := readRoutedTestMessage(t, rw)
	require.NoError(t, writer.CloseWithError(errPartialPayload))
	_, err := io.ReadAll(msg.Payload)
	require.ErrorIs(t, err, errPartialPayload)
}

func TestRoutedPayloadBufferBounds(t *testing.T) {
	for _, size := range []uint32{0, 1, bulkMaxMessageSize} {
		t.Run(strconv.FormatUint(uint64(size), 10), func(t *testing.T) {
			reader := bytes.NewReader(bytes.Repeat([]byte{0x42}, int(size)+1))
			payload, err := readRoutedPayload(Msg{Size: size, Payload: reader})
			require.NoError(t, err)
			require.Len(t, payload, int(size))
			require.Equal(t, 1, reader.Len())
			if size > 0 {
				require.Equal(t, byte(0x42), payload[size-1])
			}
		})
	}
	_, err := readRoutedPayload(Msg{Size: ^uint32(0)})
	require.Error(t, err)
}
