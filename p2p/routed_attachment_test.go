package p2p

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutedAttachmentValidation(t *testing.T) {
	primary, remote := MsgPipe()
	t.Cleanup(func() { require.NoError(t, primary.Close()) })
	t.Cleanup(func() { require.NoError(t, remote.Close()) })
	require.Same(t, primary, NewMultiChannelRoutedMsgReadWriter(primary, nil))
	require.Same(t, primary, NewRoutedMsgReadWriter(primary, remote, nil))
	rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "eth-bulk" }).(*routedMsgReadWriter)
	for _, channels := range [][]string{nil, {}, {""}, {"", ""}} {
		require.Nil(t, rw.setBulkChannels(channels, remote, nil, nil))
	}
	require.Nil(t, rw.setBulkChannels([]string{"eth-bulk"}, nil, nil, nil))
	require.False(t, rw.HasBulk())
	err := &bulkWriteError{err: io.ErrUnexpectedEOF, committed: true}
	require.Equal(t, io.ErrUnexpectedEOF.Error(), err.Error())
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}
