package params

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The preset gives the full discount up to 40% parent usage and fades it to
// none at the producers' high EIP-1559 target.
func TestDefaultPaymentLaneConfigRamp(t *testing.T) {
	t.Parallel()

	c := DefaultPaymentLaneConfig(MainnetPaymentLaneAssets)
	require.NoError(t, c.Validate())

	const limit = 160_000_000
	for used, want := range map[uint64]uint64{
		0:                10_000,
		limit * 20 / 100: 10_000,
		limit * 40 / 100: 10_000,
		limit*40/100 + 1: 9_999,
		limit * 55 / 100: 5_000,
		limit*70/100 - 1: 0,
		limit * 70 / 100: 0,
	} {
		require.Equal(t, want, c.ScaleForParent(used, limit), "parent gas used %d", used)
	}

	c.NonPaymentGasCapPercentage = 66
	require.NoError(t, c.Validate(), "a cap just above the default target still fits the preset gate")
}

func TestDefaultPaymentLaneConfigPricing(t *testing.T) {
	t.Parallel()

	c := DefaultPaymentLaneConfig(MainnetPaymentLaneAssets)
	require.Equal(t, uint64(8_000), c.BaseFeeDiscountBps)
	require.Nil(t, c.TipCap)
	require.Zero(t, c.TipDiscountBps)
	require.True(t, c.CountAllowancePulls)
	require.NoError(t, c.Validate())
}
