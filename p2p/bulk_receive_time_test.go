package p2p

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
)

func TestBulkFrameReceiveTime(t *testing.T) {
	stream := new(writeTestStream)
	rw := &bulkStreamMsgRW{stream: stream, log: log.New()}
	require.NoError(t, SendItems(rw, 1, uint64(42)))
	before := time.Now()
	msg, err := rw.ReadMsg()
	require.NoError(t, err)
	require.False(t, msg.Time().Before(before))
	require.False(t, msg.Time().After(time.Now()))
	var payload []uint64
	require.NoError(t, msg.Decode(&payload))
	require.Equal(t, []uint64{42}, payload)
}

func TestRoutedBulkReceiveTime(t *testing.T) {
	for _, stamped := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "preserved"}[stamped], func(t *testing.T) {
			primary, remote := MsgPipe()
			t.Cleanup(func() { require.NoError(t, remote.Close()) })
			bulk := newStressMsgRW()
			rw := NewRoutedMsgReadWriter(primary, bulk, func(uint64) bool { return true }).(*routedMsgReadWriter)
			t.Cleanup(func() { require.NoError(t, rw.Close()) })
			var arrival time.Time
			if stamped {
				arrival = time.Now().Add(-time.Second)
			}
			before := time.Now()
			bulk.results <- scriptedResult{msg: Msg{Code: 1, Size: 1, Payload: bytes.NewReader([]byte{0xc0}), ReceivedAt: arrival}}
			msg := readRoutedTestMessage(t, rw)
			if stamped {
				require.Equal(t, arrival, msg.Time())
			} else {
				require.False(t, msg.Time().Before(before))
				require.False(t, msg.Time().After(time.Now()))
			}
			require.NoError(t, msg.Discard())
		})
	}
}
