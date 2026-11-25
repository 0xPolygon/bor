package params

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func FuzzPaymentLaneScaleForParent(f *testing.F) {
	f.Add(uint64(20), uint64(70), uint64(0), uint64(30_000_000))
	f.Add(uint64(20), uint64(70), uint64(6_000_000), uint64(30_000_000))
	f.Add(uint64(20), uint64(70), uint64(6_000_001), uint64(30_000_000))
	f.Add(uint64(20), uint64(70), uint64(21_000_000), uint64(30_000_000))
	f.Add(uint64(50), uint64(0), uint64(15_000_001), uint64(30_000_000))
	f.Add(uint64(100), uint64(0), ^uint64(0), ^uint64(0))
	f.Add(uint64(1), uint64(100), ^uint64(0)-1, ^uint64(0))

	f.Fuzz(func(t *testing.T, active, rampEnd, used, limit uint64) {
		c := &PaymentLaneConfig{ActiveGasPercentage: active, RampEndGasPercentage: rampEnd}
		if c.validateGate() != nil {
			return
		}

		got := c.ScaleForParent(used, limit)
		if got > PaymentLaneMaxBps {
			t.Fatalf("scale %d above max", got)
		}

		u := new(big.Int).Mul(new(big.Int).SetUint64(used), big.NewInt(100))
		l := new(big.Int).SetUint64(limit)
		full := new(big.Int).Mul(l, new(big.Int).SetUint64(active))
		end := new(big.Int).Mul(l, new(big.Int).SetUint64(rampEnd))

		switch {
		case u.Cmp(full) <= 0:
			if got != PaymentLaneMaxBps {
				t.Fatalf("at or below the gate: scale %d, want max", got)
			}
		case rampEnd == 0 || u.Cmp(end) >= 0:
			if got != 0 {
				t.Fatalf("past the ramp: scale %d, want 0", got)
			}
		default:
			want := new(big.Int).Sub(end, u)
			want.Mul(want, big.NewInt(PaymentLaneMaxBps))
			want.Div(want, new(big.Int).Sub(end, full))

			if want.Uint64() != got {
				t.Fatalf("on the ramp: scale %d, want %d", got, want)
			}
		}

		if used < ^uint64(0) && c.ScaleForParent(used+1, limit) > got {
			t.Fatalf("scale rises with parent usage at %d/%d", used, limit)
		}
	})
}

func FuzzPaymentLaneNonPaymentGasCap(f *testing.F) {
	f.Add(uint64(81), uint64(30_000_000))
	f.Add(uint64(99), ^uint64(0))
	f.Add(uint64(66), uint64(99))

	f.Fuzz(func(t *testing.T, pct, limit uint64) {
		c := &PaymentLaneConfig{NonPaymentGasCapPercentage: pct % 100}

		got, ok := c.NonPaymentGasCap(limit)
		if ok != (pct%100 != 0) {
			t.Fatalf("cap applies = %v for pct %d", ok, pct%100)
		}

		want := new(big.Int).Mul(new(big.Int).SetUint64(limit), new(big.Int).SetUint64(pct%100))
		want.Div(want, big.NewInt(100))

		if ok && want.Uint64() != got {
			t.Fatalf("cap %d, want %d", got, want)
		}
	})
}

func FuzzPaymentLaneConfigJSON(f *testing.F) {
	f.Add([]byte(`{"assets":["0xa11ce00000000000000000000000000000000001"],"baseFeeDiscountBps":8000,"activeGasPercentage":20,"rampEndGasPercentage":70,"countAllowancePulls":true}`))
	f.Add([]byte(`{"assets":["0xa11ce00000000000000000000000000000000001"],"minAmounts":{"0xa11ce00000000000000000000000000000000001":1000},"baseFeeDiscountBps":9999,"tipDiscountBps":5000,"tipCap":1,"activeGasPercentage":50,"nonPaymentGasCapPercentage":81}`))
	f.Add([]byte(`{"assets":[],"activeGasPercentage":0}`))
	f.Add([]byte(`{"tipCap":-1,"activeGasPercentage":50,"assets":["0x01"]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var c PaymentLaneConfig
		if json.Unmarshal(data, &c) != nil || c.Validate() != nil {
			return
		}

		enc, err := json.Marshal(&c)
		if err != nil {
			t.Fatalf("marshal valid config: %v", err)
		}

		var back PaymentLaneConfig
		if err := json.Unmarshal(enc, &back); err != nil {
			t.Fatalf("unmarshal own encoding: %v", err)
		}

		if err := back.Validate(); err != nil {
			t.Fatalf("round-tripped config no longer valid: %v", err)
		}

		again, _ := json.Marshal(&back)
		if !bytes.Equal(enc, again) || !sameLaneVersion(&c, &back) {
			t.Fatalf("round trip changed the config:\n%s\n%s", enc, again)
		}

		for _, a := range c.Assets {
			if !c.IsAsset(a) {
				t.Fatalf("listed asset %x not recognised", a)
			}

			c.MeetsMinAmount(a, data)
		}

		c.MeetsMinAmount(common.Address{}, data)
		c.ScaleForParent(uint64(len(data)), 30_000_000)
	})
}
