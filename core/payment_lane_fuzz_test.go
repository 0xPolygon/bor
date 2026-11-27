package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

// The discount must never exceed what the payment gas paid, must vanish at
// scale 0, and must not fall as the scale rises.
func FuzzPaymentLaneDiscountFor(f *testing.F) {
	f.Add(uint16(8000), uint16(0), false, uint64(0), uint64(30_000_000_000), uint64(30_000_000_000), uint16(10_000), uint64(60_000))
	f.Add(uint16(10_000), uint16(10_000), true, uint64(0), ^uint64(0), ^uint64(0), uint16(10_000), ^uint64(0))
	f.Add(uint16(5000), uint16(3000), true, uint64(7), uint64(1), uint64(1_000), uint16(1), uint64(1))

	f.Fuzz(func(t *testing.T, baseBps, tipBps uint16, capped bool, tipCap, baseFee, tip uint64, scale uint16, gas uint64) {
		lane := &params.PaymentLaneConfig{BaseFeeDiscountBps: uint64(baseBps), TipDiscountBps: uint64(tipBps)}
		if capped {
			lane.TipCap = new(big.Int).SetUint64(tipCap)
		}

		if lane.BaseFeeDiscountBps > params.PaymentLaneMaxBps || lane.TipDiscountBps > params.PaymentLaneMaxBps || scale > params.PaymentLaneMaxBps {
			return
		}

		bf, tp := new(big.Int).SetUint64(baseFee), new(big.Int).SetUint64(tip)
		got := PaymentLaneDiscountFor(lane, bf, tp, uint64(scale), gas)

		paid := new(big.Int).Add(bf, tp)
		paid.Mul(paid, new(big.Int).SetUint64(gas))

		if got.Sign() < 0 || got.Cmp(paid) > 0 {
			t.Fatalf("discount %v outside [0, %v]", got, paid)
		}

		if tc := tipCutPerGas(tp, lane); tc.Sign() < 0 || tc.Cmp(tp) > 0 {
			t.Fatalf("tip cut %v outside [0, %v]", tc, tp)
		}

		if scale == 0 && got.Sign() != 0 {
			t.Fatalf("discount %v at scale 0", got)
		}

		if scale < params.PaymentLaneMaxBps && PaymentLaneDiscountFor(lane, bf, tp, uint64(scale)+1, gas).Cmp(got) < 0 {
			t.Fatalf("discount falls as the scale rises from %d", scale)
		}

		if PaymentLaneDiscountFor(nil, bf, tp, uint64(scale), gas).Sign() != 0 {
			t.Fatal("discount without a lane")
		}
	})
}
