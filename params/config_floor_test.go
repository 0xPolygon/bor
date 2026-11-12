package params

import (
	"math/big"
	"testing"
)

func TestGetBaseFeeFloor(t *testing.T) {
	t.Parallel()
	on, off := true, false
	target, zero := uint64(250*GWei), uint64(0)
	cfg := func(lisovo int64, floor *bool, target *uint64) *BorConfig {
		return &BorConfig{LisovoBlock: big.NewInt(lisovo), BaseFeeFloor: floor, TargetBaseFee: target}
	}
	tests := []struct {
		name string
		cfg  *BorConfig
		want *big.Int
	}{
		{"enabled", cfg(10, &on, &target), new(big.Int).SetUint64(target)},
		{"pre-Lisovo", cfg(1000, &on, &target), nil},
		{"flag unset", cfg(10, nil, &target), nil},
		{"flag off", cfg(10, &off, &target), nil},
		{"no target", cfg(10, &on, nil), nil},
		{"zero target", cfg(10, &on, &zero), nil},
	}
	for _, tt := range tests {
		got := tt.cfg.GetBaseFeeFloor(big.NewInt(100))
		if (got == nil) != (tt.want == nil) || (got != nil && got.Cmp(tt.want) != 0) {
			t.Errorf("%s: floor %v, want %v", tt.name, got, tt.want)
		}
	}
}
