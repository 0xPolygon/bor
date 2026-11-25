package vm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params"
)

func FuzzPaymentCall(f *testing.F) {
	word := func(b byte) []byte { w := make([]byte, 32); w[31] = b; return w }
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}

		return out
	}
	addr := func(b byte) []byte { w := make([]byte, 32); w[12] = b; return w }

	f.Add(cat(selTransfer[:], addr(2), word(5)), false, false)
	f.Add(cat(selTransfer[:], addr(2), word(5), make([]byte, 20)), false, true)
	f.Add(cat(selTransferFrom[:], addr(1), addr(2), word(5)), true, false)
	f.Add(cat(selTransferFrom[:], addr(3), addr(2), word(5)), false, false)
	f.Add(cat(selTransferWithAuth[:], addr(1), addr(2), word(5), word(0), word(9), word(1), word(27), word(1), word(1)), false, false)
	f.Add(cat(selTransferWithAuthSig[:], addr(1), addr(2), word(5), word(0), word(9), word(1), word(224), word(65), make([]byte, 96)), false, false)
	f.Add(cat(selReceiveWithAuthSig[:], addr(1), addr(2), word(5), word(0), word(9), word(1), word(224), hexutil.MustDecode("0x00000000000000000000000000000000000000000000000000000000ffffffff")), false, false)

	caller, asset := common.Address{1}, common.Address{0xa1}

	f.Fuzz(func(t *testing.T, input []byte, pulls, selfCall bool) {
		from := caller
		if selfCall {
			from = asset
		}

		m := paymentCall(from, asset, input, pulls)
		if m.n == 0 {
			if m != (paymentMove{}) {
				t.Fatalf("non-payment with fields set: %+v", m)
			}

			return
		}

		if m.n > len(input) || len(input) > m.n+maxPaymentTrailingBytes {
			t.Fatalf("payment length %d for %d bytes of input", m.n, len(input))
		}

		if m.maxGas != params.PaymentLaneMaxTransferFrameGas && m.maxGas != params.PaymentLaneMaxFrameGas {
			t.Fatalf("frame bound %d", m.maxGas)
		}

		if m.amount == ([32]byte{}) || m.from == m.to {
			t.Fatalf("zero or self movement classified as a payment: %+v", m)
		}

		if !pulls && paymentCall(from, asset, input, true) != m {
			t.Fatal("enabling allowance pulls changed a payment that counted without them")
		}

		lane := &params.PaymentLaneConfig{Assets: []common.Address{asset}, CountAllowancePulls: pulls}
		if n, g := DirectPaymentCall(lane, from, asset, input); n != m.n || g != m.maxGas {
			t.Fatalf("DirectPaymentCall %d/%d, paymentCall %d/%d", n, g, m.n, m.maxGas)
		}

		if n, _ := DirectPaymentCall(lane, from, common.Address{0xb2}, input); n != 0 {
			t.Fatal("payment to an unlisted asset")
		}
	})
}
