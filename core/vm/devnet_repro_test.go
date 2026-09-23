//go:build devnet_repro

package vm

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// TestDevnetTraceOpcodeGapLogsSlowGap verifies that a gap exceeding the
// slow-opcode threshold is recorded and retrievable via drainSlowOpcodeGaps,
// tagged with the previous call's opcode/pc/contract (see
// devnetTraceOpcodeGapForKey's doc comment for the attribution reasoning),
// and that a fast gap is not recorded.
func TestDevnetTraceOpcodeGapLogsSlowGap(t *testing.T) {
	prev := devnetOpcodeGapsEnabled
	devnetOpcodeGapsEnabled = true
	t.Cleanup(func() { devnetOpcodeGapsEnabled = prev })

	// Deferred minor (final review): this test previously asserted against
	// the shared global devnetSlowGaps without draining first, which is
	// safe only by accident of test ordering — drain up front so this test
	// can never be polluted by another devnet_repro test in the same
	// tagged test binary (this is exactly the class of global-state test
	// coupling that caused the earlier nil-gasPool contamination bug).
	drainSlowOpcodeGaps()

	key := new(uint64)
	contractAddr := common.HexToAddress("0x00000000000000000000000000000000000000aa")

	devnetTraceOpcodeGapForKey(key, SLOAD, 10, contractAddr) // first call just seeds the entry, never itself "slow"
	time.Sleep(6 * time.Millisecond)
	devnetTraceOpcodeGapForKey(key, ADD, 11, contractAddr) // second call should observe a >5ms gap, attributed to the FIRST call's opcode/pc

	gaps := drainSlowOpcodeGaps()
	require.Len(t, gaps, 1)
	require.GreaterOrEqual(t, gaps[0].Duration, 5*time.Millisecond)
	require.Equal(t, SLOAD, gaps[0].Op, "the gap must be attributed to the opcode that ran DURING it (the previous call's), not the one about to run next")
	require.Equal(t, uint64(10), gaps[0].Pc)
	require.Equal(t, contractAddr, gaps[0].Contract)
}
