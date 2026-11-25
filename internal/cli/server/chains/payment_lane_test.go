package chains

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/params"
)

// Scheduling the payment lane means setting the block and the parameters in
// every surface a node may start from; a block present in one but missing in
// another is a chain split at activation.
func TestPaymentLanePresetParity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		params  *params.ChainConfig
		chain   *Chain
		genesis string
	}{
		{"mainnet", params.BorMainnetChainConfig, mainnetBor, "../../../../builder/files/genesis-mainnet-v1.json"},
		{"amoy", params.AmoyChainConfig, amoyTestnet, "../../../../builder/files/genesis-amoy.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want, got := tc.params.Bor, tc.chain.Genesis.Config.Bor
			require.Equal(t, want.PaymentLaneBlock, got.PaymentLaneBlock, "fork block: params vs CLI preset")
			require.Equal(t, want.PaymentLane, got.PaymentLane, "lane parameters: params vs CLI preset")
			require.NoError(t, want.ValidatePaymentLane())
			require.NoError(t, got.ValidatePaymentLane())

			raw, err := os.ReadFile(tc.genesis)
			require.NoError(t, err)

			var file struct {
				Config struct {
					Bor params.BorConfig `json:"bor"`
				} `json:"config"`
			}
			require.NoError(t, json.Unmarshal(raw, &file))
			require.Equal(t, want.PaymentLaneBlock, file.Config.Bor.PaymentLaneBlock, "fork block: params vs packaged genesis")
			require.Equal(t, want.PaymentLane, file.Config.Bor.PaymentLane, "lane parameters: params vs packaged genesis")
		})
	}
}
