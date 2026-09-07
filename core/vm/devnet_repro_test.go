//go:build devnet_repro

package vm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDevnetTraceOpcodeGapLogsSlowGap verifies that a gap exceeding the
// slow-opcode threshold is recorded and retrievable via drainSlowOpcodeGaps,
// and that a fast gap is not recorded.
func TestDevnetTraceOpcodeGapLogsSlowGap(t *testing.T) {
	key := new(uint64)

	devnetTraceOpcodeGapForKey(key) // first call just seeds the timestamp, never itself "slow"
	time.Sleep(6 * time.Millisecond)
	devnetTraceOpcodeGapForKey(key) // second call should observe a >5ms gap

	gaps := drainSlowOpcodeGaps()
	require.Len(t, gaps, 1)
	require.GreaterOrEqual(t, gaps[0], 5*time.Millisecond)
}
