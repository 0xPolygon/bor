package t8ntool

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func laneTestConfig() *params.ChainConfig {
	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(10)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{"10": {Assets: []common.Address{common.HexToAddress("0x70ce")}, ActiveGasPercentage: 70}}
	cfg.Bor = &bor

	return &cfg
}

// t8n prices a payment from the parent gas the env carries, the same way an
// importing node does. When the env omits the parent gas the gate cannot be
// evaluated and the block prices in full.
func TestStEnvPaymentLaneScale(t *testing.T) {
	t.Parallel()

	cfg := laneTestConfig()

	tests := []struct {
		name string
		env  stEnv
		want uint64
	}{
		{"quiet parent", stEnv{Number: 11, ParentGasLimit: 1_000, ParentGasUsed: 100}, params.PaymentLaneMaxBps},
		{"busy parent", stEnv{Number: 11, ParentGasLimit: 1_000, ParentGasUsed: 900}, 0},
		{"before the fork", stEnv{Number: 9, ParentGasLimit: 1_000, ParentGasUsed: 100}, 0},
		{"parent gas omitted", stEnv{Number: 11}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.env.paymentLaneScale(cfg))
		})
	}
}
