package vm

import (
	"bytes"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

var (
	selTransfer            = [4]byte{0xa9, 0x05, 0x9c, 0xbb} // transfer(address,uint256)
	selTransferFrom        = [4]byte{0x23, 0xb8, 0x72, 0xdd} // transferFrom(address,address,uint256)
	selTransferWithAuth    = [4]byte{0xe3, 0xee, 0x16, 0x0e} // EIP-3009 transferWithAuthorization (v,r,s)
	selTransferWithAuthSig = [4]byte{0xcf, 0x09, 0x29, 0x95} // EIP-3009 transferWithAuthorization (bytes signature)
	selReceiveWithAuth     = [4]byte{0xef, 0x55, 0xbe, 0xc6} // EIP-3009 receiveWithAuthorization (v,r,s)
	selReceiveWithAuthSig  = [4]byte{0x88, 0xb7, 0xab, 0x63} // EIP-3009 receiveWithAuthorization (bytes signature)

	// keccak256("Transfer(address,address,uint256)")
	transferEventSig = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
)

const (
	// maxPaymentSignatureLen bounds the bytes signature of the EIP-3009
	// variants, which the asset copies and hashes.
	maxPaymentSignatureLen = 512

	// maxPaymentTrailingBytes is the calldata a payment call may carry
	// after its ABI encoding (e.g. the 20-byte sender appended by
	// meta-transaction self-calls). Listed assets are proxies that copy
	// the whole calldata into memory, so more would let a caller inflate
	// the payment frame's gas.
	maxPaymentTrailingBytes = 32
)

// paymentMove is the movement a payment call asks for.
type paymentMove struct {
	from, to common.Address
	amount   [32]byte
	n        int    // ABI-encoded length of the call, 0 if not a payment
	maxGas   uint64 // most gas the frame may be credited with
}

// DirectPaymentCall reports whether a transaction from sender calling to
// with input is a payment attempt the lane recognises: a listed asset, a
// payment-shaped call for at least the asset's minimum amount. Whether it
// is discounted is only known after execution (the asset must log the
// transfer); this is what the capacity rule and the miner see beforehand.
// It returns the canonical calldata length of the payment and the most gas
// its frame may be credited with, or zeros if it is not one.
func DirectPaymentCall(lane *params.PaymentLaneConfig, sender, to common.Address, input []byte) (callLen int, maxGas uint64) {
	if lane == nil || !lane.IsAsset(to) {
		return 0, 0
	}

	m := paymentCall(sender, to, input, lane.CountAllowancePulls)
	if m.n == 0 || !lane.MeetsMinAmount(to, m.amount[:]) {
		return 0, 0
	}

	return m.n, m.maxGas
}

// paymentCall decodes a payment call to asset made by caller: a non-zero
// movement between two distinct addresses that the payer authorised
// directly. Allowance pulls (transferFrom by a third party) are how
// exchanges and routers settle, so they only count when allowPulls is set.
// The returned move has n == 0 if input is not a payment call.
func paymentCall(caller, asset common.Address, input []byte, allowPulls bool) paymentMove {
	m := decodePaymentCall(caller, asset, input, allowPulls)
	if m.n == 0 || len(input) > m.n+maxPaymentTrailingBytes || m.amount == ([32]byte{}) || m.from == m.to {
		return paymentMove{}
	}

	return m
}

func decodePaymentCall(caller, asset common.Address, input []byte, allowPulls bool) paymentMove {
	if len(input) < 4+2*32 {
		return paymentMove{}
	}

	sel := [4]byte(input[:4])
	if sel == selTransfer {
		return paymentMove{
			from: metaTxSender(caller, asset, input, 4+2*32), to: common.Address(input[4+12 : 4+32]),
			amount: [32]byte(input[4+32 : 4+64]), n: 4 + 2*32, maxGas: params.PaymentLaneMaxTransferFrameGas,
		}
	}

	if len(input) < 4+3*32 {
		return paymentMove{}
	}

	m := paymentMove{from: common.Address(input[4+12 : 4+32]), to: common.Address(input[4+32+12 : 4+64]), amount: [32]byte(input[4+64 : 4+96]), maxGas: params.PaymentLaneMaxFrameGas}
	switch sel {
	case selTransferFrom:
		if allowPulls || m.from == metaTxSender(caller, asset, input, 4+3*32) {
			m.n, m.maxGas = 4+3*32, params.PaymentLaneMaxTransferFrameGas
		}
	case selTransferWithAuth, selReceiveWithAuth:
		if len(input) >= 4+9*32 {
			m.n = 4 + 9*32
		}
	case selTransferWithAuthSig, selReceiveWithAuthSig:
		m.n = signedPaymentLen(input)
	}

	return m
}

// metaTxSender is the account a call acts for. Tokens with native
// meta-transactions (e.g. the Polygon PoS bridged UChildERC20) execute them
// as a call to themselves with the signer appended to the calldata; only the
// asset's own code can make such a call.
func metaTxSender(caller, asset common.Address, input []byte, n int) common.Address {
	if caller == asset && len(input) == n+common.AddressLength {
		return common.Address(input[n:])
	}

	return caller
}

// signedPaymentLen is the length of the bytes-signature EIP-3009 variants:
// six static words, the offset of the signature (always right after them),
// its length word and its padded bytes.
func signedPaymentLen(input []byte) int {
	const head = 4 + 7*32
	if len(input) < head+32 || new(big.Int).SetBytes(input[head-32:head]).Cmp(big.NewInt(6*32+32)) != 0 {
		return 0
	}

	sigLen := new(big.Int).SetBytes(input[head : head+32])
	if sigLen.Sign() == 0 || sigLen.Cmp(big.NewInt(maxPaymentSignatureLen)) > 0 {
		return 0
	}

	n := head + 32 + (int(sigLen.Int64())+31)/32*32
	if len(input) < n {
		return 0
	}

	return n
}

// laneMeter is the payment lane bookkeeping of the current transaction.
type laneMeter struct {
	cfg           *params.PaymentLaneConfig // non-nil from the fork on; metering runs even at zero discount scale
	gas           uint64                    // asset gas of successful payment frames
	txCallLen     int                       // canonical calldata length when the tx-level call is a payment
	inFrame       bool                      // a payment frame is on the call stack
	asset         common.Address            // asset of the open payment frame
	inForeign     bool                      // code outside asset is running inside the payment frame
	foreign       uint64                    // gas used by foreign frames inside the open payment frame
	refund        uint64                    // refund counter when the open payment frame started
	foreignRefund int64                     // refund counter change made by foreign frames inside the open payment frame
	move          paymentMove               // movement the open payment frame asked for
	moved         bool                      // the asset logged that movement
}

func paymentLaneAt(config *params.ChainConfig, number *big.Int) *params.PaymentLaneConfig {
	if config.Bor == nil {
		return nil
	}

	return config.Bor.PaymentLaneAt(number)
}

func (evm *EVM) initPaymentLane(config *params.ChainConfig) {
	evm.lane.cfg = paymentLaneAt(config, evm.Context.BlockNumber)
	if evm.Context.PaymentLaneScaleBps != 0 && evm.lane.cfg == nil {
		// The block context was priced with a chain whose lane parameters this
		// EVM does not have: without them nothing is metered, so the scale is
		// a caller bug that would otherwise pass unnoticed. Fail safe to full
		// price rather than trust a scale nothing can apply.
		log.Error("Payment lane scale without lane parameters, pricing at full price",
			"block", evm.Context.BlockNumber)
		evm.Context.PaymentLaneScaleBps = 0
	}
}

// laneFrame is the payment lane bookkeeping of one call frame.
type laneFrame struct {
	checkpoint uint64 // laneGas when the frame started, restored if it fails
	moved      bool   // laneMoved when the frame started, restored if it fails
	credit     bool   // the frame is a payment frame
	foreign    bool   // the frame runs code outside the asset, inside a payment frame
	callLen    int    // canonical calldata length of a payment frame
	refund     uint64 // refund counter when a foreign frame started
}

// laneStart opens the bookkeeping of a frame. Every frame that fails drops
// the credits taken inside it, because the payments it made were rolled back.
func (evm *EVM) laneStart() laneFrame {
	return laneFrame{checkpoint: evm.lane.gas, moved: evm.lane.moved}
}

// laneDropOnError settles a DELEGATECALL or CALLCODE frame, which can only
// contain payments, never be or meter one.
func (evm *EVM) laneDropOnError(f laneFrame, err error) {
	if err != nil {
		evm.lane.gas, evm.lane.moved = f.checkpoint, f.moved
	}
}

// laneOpenCall is called once a CALL frame is about to run code at addr. A
// CALL into a listed asset with a payment shape opens a payment frame (only
// the outermost one, so proxy delegatecalls are not counted twice).
func (evm *EVM) laneOpenCall(f *laneFrame, caller, addr common.Address, input []byte) {
	if evm.lane.cfg == nil || evm.lane.inFrame {
		evm.laneOpenFrame(f, addr)
		return
	}

	if !evm.lane.cfg.IsAsset(addr) {
		return
	}

	m := paymentCall(caller, addr, input, evm.lane.cfg.CountAllowancePulls)
	if m.n == 0 || !evm.lane.cfg.MeetsMinAmount(addr, m.amount[:]) {
		return
	}

	f.credit, f.callLen = true, m.n
	evm.lane.inFrame, evm.lane.asset, evm.lane.foreign = true, addr, 0
	evm.lane.refund, evm.lane.foreignRefund = evm.StateDB.GetRefund(), 0
	evm.lane.move, evm.lane.moved = m, false
}

// laneOpenFrame is called once a frame is about to run code in the context
// of ctx. Inside a payment frame, code running in any other context
// (signature callbacks, hooks, contracts it creates) is foreign: its gas is
// not the asset's own work and is not credited. DELEGATECALL and CALLCODE
// keep the caller's context, so they are never foreign on their own and
// don't call this. Precompiles never reach here.
func (evm *EVM) laneOpenFrame(f *laneFrame, ctx common.Address) {
	if !evm.lane.inFrame || evm.lane.inForeign || ctx == evm.lane.asset {
		return
	}

	f.foreign, evm.lane.inForeign, f.refund = true, true, evm.StateDB.GetRefund()
}

// laneLog is called for every log emitted. A payment frame is only credited
// once the asset itself logged the exact transfer it was asked for: a call
// that succeeds without moving funds (a false return, a no-op, a
// fee-on-transfer amount) is not a payment.
func (evm *EVM) laneLog(addr common.Address, topics []common.Hash, data []byte) {
	if !evm.lane.inFrame || evm.lane.inForeign || addr != evm.lane.asset || len(topics) != 3 || topics[0] != transferEventSig {
		return
	}

	m := &evm.lane.move
	evm.lane.moved = evm.lane.moved || common.Address(topics[1][12:]) == m.from && common.Address(topics[2][12:]) == m.to && bytes.Equal(data, m.amount[:])
}

// laneClose settles a frame opened with laneStart, given the gas it used.
// A payment frame is credited its gas net of the refunds the asset's own
// code earned (paid back at settlement), and bounded per kind of call: real
// transfers use well under the bound, which caps what memory or returndata
// tricks inside the asset's own context could add. The refund counter is
// journaled, so a failed frame has already rolled back what it earned.
func (evm *EVM) laneClose(f laneFrame, gasUsed uint64, err error) {
	if f.foreign {
		evm.lane.inForeign = false
		evm.lane.foreign += gasUsed
		evm.lane.foreignRefund += int64(evm.StateDB.GetRefund()) - int64(f.refund)
	}

	if f.credit {
		evm.lane.inFrame = false
		if err == nil && evm.lane.moved {
			// Foreign frames' refunds go with their gas, already excluded in full.
			earned := uint64(max(int64(evm.StateDB.GetRefund())-int64(evm.lane.refund)-evm.lane.foreignRefund, 0))
			evm.lane.gas += min(gasUsed-min(evm.lane.foreign+earned, gasUsed), evm.lane.move.maxGas)
			if evm.depth == 0 {
				evm.lane.txCallLen = f.callLen
			}
		}
	}

	if err != nil {
		evm.lane.gas, evm.lane.moved = f.checkpoint, f.moved
	}
}

// PaymentLaneGas returns the gas the asset code of successful payment frames
// used in the current transaction, and, when the transaction itself is a
// payment call, the length of its canonical calldata (0 otherwise), so the
// intrinsic gas of that calldata can be credited too.
func (evm *EVM) PaymentLaneGas() (gas uint64, txCallLen int) {
	return evm.lane.gas, evm.lane.txCallLen
}

// PaymentLane returns the lane config from the fork on, nil before it.
func (evm *EVM) PaymentLane() *params.PaymentLaneConfig {
	return evm.lane.cfg
}

func (evm *EVM) resetPaymentLane() {
	evm.lane = laneMeter{cfg: evm.lane.cfg}
}
