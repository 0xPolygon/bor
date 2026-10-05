package p2p

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestBulkNextMappingRefresh(t *testing.T) {
	_, ok := nextMappingRefresh(nil)
	require.False(t, ok)
	for _, first := range []mclock.AbsTime{0, 1, 50} {
		next, ok := nextMappingRefresh(map[string]*portMapping{
			"tcp": {nextTime: 100}, "discovery": {nextTime: 200}, "bulk": {nextTime: first},
		})
		require.True(t, ok)
		require.Equal(t, first, next)
	}
}
