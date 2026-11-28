package p2p

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func routeValidationCode(code uint64) string {
	switch code {
	case 1:
		return "control"
	case 2:
		return "bulk"
	default:
		return ""
	}
}

func TestRoutedIncomingCodeValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		code uint64
	}{
		{"primary-only", 0},
		{"wrong-lane", 1},
		{"unknown", ^uint64(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			rw := NewMultiChannelRoutedMsgReadWriter(newCountedRoutedRW(), routeValidationCode).(*routedMsgReadWriter)
			defer rw.Close()
			rw.buffers = testBulkBudget(1, 1)
			require.True(t, rw.buffers.tryAcquire(1))
			defer rw.buffers.release(1)
			bulk := newCountedRoutedRW()
			rw.AttachBulkChannel("bulk", bulk)
			rw.AttachBulkChannel("control", newCountedRoutedRW())
			bulk.results <- scriptedResult{msg: Msg{Code: test.code, Size: 1, Payload: bytes.NewReader([]byte{0})}}
			waitRoutedClosed(t, rw.Done())
			_, err := rw.ReadMsg()
			require.ErrorContains(t, err, "invalid message code")
			waitRoutedClosed(t, bulk.closed)
		})
	}
}

func TestRoutedIncomingSharedLaneCodes(t *testing.T) {
	rw := NewMultiChannelRoutedMsgReadWriter(newCountedRoutedRW(), routeValidationCode).(*routedMsgReadWriter)
	defer rw.Close()
	bulk := newCountedRoutedRW()
	rw.AttachBulkChannels([]string{"control", "bulk"}, bulk)
	for _, code := range []uint64{1, 2} {
		bulk.PushMsg(code)
		msg := readRoutedTestMessage(t, rw)
		require.Equal(t, code, msg.Code)
		require.NoError(t, msg.Discard())
	}
}

func TestRoutedIncomingRetiredLane(t *testing.T) {
	rw := NewMultiChannelRoutedMsgReadWriter(newCountedRoutedRW(), routeValidationCode).(*routedMsgReadWriter)
	defer rw.Close()
	bulk := newCountedRoutedRW()
	ctx, cancel := context.WithCancel(t.Context())
	lane := rw.setBulkChannels([]string{"bulk"}, bulk, ctx.Done(), cancel)
	rw.AttachBulkChannel("bulk", newCountedRoutedRW())
	require.ErrorIs(t, rw.validateBulkMsgCode(lane, 2), io.EOF)
	require.Nil(t, rw.terminalError())
}
