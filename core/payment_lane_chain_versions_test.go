package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

var (
	laneChainKey, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	laneChainSender = crypto.PubkeyToAddress(laneChainKey.PublicKey)
	laneChainTip    = big.NewInt(100_000_000_000)
)

const laneChainGasLimit = 2_000_000

// laneChain generates n blocks under the given lane versions (fork at the
// lowest key) with a token, a burner and a wallet that holds a balance.
type laneChain struct {
	t      *testing.T
	gspec  *Genesis
	signer types.Signer
}

func newLaneChain(t *testing.T, fork int64, versions map[string]*params.PaymentLaneConfig) *laneChain {
	t.Helper()

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(fork)
	bor.PaymentLane = versions
	cfg.Bor = &bor
	require.NoError(t, bor.ValidatePaymentLane())

	gspec := &Genesis{
		Config:   &cfg,
		GasLimit: laneChainGasLimit,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			laneChainSender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000))},
			laneToken:       {Code: laneTestToken},
			laneBurner:      {Code: []byte{byte(vm.INVALID)}},
		},
	}

	return &laneChain{t: t, gspec: gspec, signer: types.LatestSigner(&cfg)}
}

func (c *laneChain) sign(b *BlockGen, to common.Address, gas uint64, data []byte) *types.Transaction {
	feeCap := new(big.Int).Add(new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), laneChainTip)
	tx, err := types.SignNewTx(laneChainKey, c.signer, &types.DynamicFeeTx{
		ChainID: c.gspec.Config.ChainID, Nonce: b.TxNonce(laneChainSender), GasTipCap: laneChainTip,
		GasFeeCap: feeCap, Gas: gas, To: &to, Data: data,
	})
	require.NoError(c.t, err)

	return tx
}

// fillTo adds a burner tx that brings the block's gas used to pct of its limit.
func (c *laneChain) fillTo(b *BlockGen, pct uint64) {
	b.AddTx(c.sign(b, laneBurner, b.header.GasLimit*pct/100-b.header.GasUsed, nil))
}

func (c *laneChain) generate(n int, gen func(i int, b *BlockGen)) ([]*types.Block, []types.Receipts) {
	_, blocks, receipts := GenerateChainWithGenesis(c.gspec, ethash.NewFaker(), n, gen)
	return blocks, receipts
}

// importBoth imports blocks serially and with BlockSTM V2, requires both to
// accept them with the same roots, and returns the serial chain.
func (c *laneChain) importBoth(blocks []*types.Block) *BlockChain {
	c.t.Helper()

	serial, err := NewBlockChain(rawdb.NewMemoryDatabase(), c.gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(c.t, err)
	c.t.Cleanup(serial.Stop)

	n, err := serial.InsertChain(blocks, false)
	require.NoError(c.t, err, "serial import, block %d", n)

	parallel, err := NewParallelBlockChain(rawdb.NewMemoryDatabase(), c.gspec, ethash.NewFaker(), DefaultConfig(), 8, true)
	require.NoError(c.t, err)
	c.t.Cleanup(parallel.Stop)

	n, err = parallel.InsertChain(blocks, false)
	require.NoError(c.t, err, "parallel import, block %d", n)

	for _, block := range blocks {
		require.Equal(c.t, block.Root(), parallel.GetHeaderByNumber(block.NumberU64()).Root, "block %d", block.NumberU64())
	}

	return serial
}

// paid is what the sender spent in block i of blocks.
func (c *laneChain) paid(chain *BlockChain, blocks []*types.Block, i int) *big.Int {
	prev := chain.Genesis().Root()
	if i > 0 {
		prev = blocks[i-1].Root()
	}

	before, err := chain.StateAt(prev)
	require.NoError(c.t, err)
	after, err := chain.StateAt(blocks[i].Root())
	require.NoError(c.t, err)

	return new(big.Int).Sub(before.GetBalance(laneChainSender).ToBig(), after.GetBalance(laneChainSender).ToBig())
}

// laneCuts recomputes the base and tip cut of gas payment gas at scale bps
// from the spec: per-gas cuts are floored, scaled, floored again.
func laneCuts(lane *params.PaymentLaneConfig, scale uint64, baseFee, tip *big.Int, gas uint64) (baseCut, tipCut *big.Int) {
	floorBps := func(v *big.Int, b uint64) *big.Int {
		out := new(big.Int).Mul(v, new(big.Int).SetUint64(b))
		return out.Div(out, big.NewInt(10_000))
	}

	charged := new(big.Int).Sub(tip, floorBps(tip, lane.TipDiscountBps))
	if lane.TipCap != nil && charged.Cmp(lane.TipCap) > 0 {
		charged = new(big.Int).Set(lane.TipCap)
	}

	g := new(big.Int).SetUint64(gas)
	baseCut = floorBps(floorBps(baseFee, lane.BaseFeeDiscountBps), scale)
	tipCut = floorBps(new(big.Int).Sub(tip, charged), scale)

	return baseCut.Mul(baseCut, g), tipCut.Mul(tipCut, g)
}

// requirePaymentPrice checks a block whose first tx is the payment: its lane
// log carries exactly the cuts of lane at scale, and the sender paid nominal
// price for every tx minus them.
func requirePaymentPrice(t *testing.T, lane *params.PaymentLaneConfig, scale uint64, block *types.Block, receipts types.Receipts, paid *big.Int) {
	t.Helper()

	receipt := receipts[0]

	got, ok := PaymentLaneFromLogs(receipt.Logs)
	require.True(t, ok, "block %d: no payment", block.NumberU64())
	require.Equal(t, receipt.GasUsed, got.Gas, "block %d: a direct payment is all payment gas", block.NumberU64())

	baseCut, tipCut := laneCuts(lane, scale, block.BaseFee(), laneChainTip, got.Gas)
	require.Equal(t, baseCut.String(), got.BaseCut.String(), "block %d base cut", block.NumberU64())
	require.Equal(t, tipCut.String(), got.TipCut.String(), "block %d tip cut", block.NumberU64())

	want := nominalPrice(block, receipts)
	want.Sub(want, baseCut).Sub(want, tipCut)
	require.Equal(t, want.String(), paid.String(), "block %d paid", block.NumberU64())
}

func nominalPrice(block *types.Block, receipts types.Receipts) *big.Int {
	var gas uint64
	for _, r := range receipts {
		gas += r.GasUsed
	}

	price := new(big.Int).Add(block.BaseFee(), laneChainTip)

	return price.Mul(price, new(big.Int).SetUint64(gas))
}

func rampLane() *params.PaymentLaneConfig {
	return &params.PaymentLaneConfig{
		Assets: []common.Address{laneToken}, BaseFeeDiscountBps: 7_500, TipCap: big.NewInt(30_000_000_000),
		ActiveGasPercentage: 50, RampEndGasPercentage: 90,
	}
}

// A parent at 70% of its limit, halfway between gate 50 and ramp end 90,
// gives its child half the discount. Block 1 follows an empty genesis
// (full scale), block 3 a parent at the ramp end (none).
func TestPaymentLaneRampOnImport(t *testing.T) {
	t.Parallel()

	lane := rampLane()
	c := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": lane})
	payment := transferInput(lanePayee, 1)

	blocks, receipts := c.generate(4, func(i int, b *BlockGen) {
		b.AddTx(c.sign(b, laneToken, 100_000, payment))

		switch i {
		case 0:
			c.fillTo(b, 70)
		case 1:
			c.fillTo(b, 90)
		}
	})

	chain := c.importBoth(blocks)

	require.Equal(t, uint64(laneChainGasLimit*70/100), blocks[0].GasUsed())
	require.Equal(t, uint64(5_000), lane.ScaleForParent(blocks[0].GasUsed(), blocks[0].GasLimit()))

	requirePaymentPrice(t, lane, 10_000, blocks[0], receipts[0], c.paid(chain, blocks, 0))
	requirePaymentPrice(t, lane, 5_000, blocks[1], receipts[1], c.paid(chain, blocks, 1))
	require.Empty(t, laneLogsOf(receipts[2][0].Logs), "parent at ramp end")
}

// A retune at block 3 prices block 2 with the first version and blocks 3
// and 4 with the second; a node that never learned the retune rejects
// block 3.
func TestPaymentLaneRetuneOnImport(t *testing.T) {
	t.Parallel()

	const retune = 3

	v1 := &params.PaymentLaneConfig{Assets: []common.Address{laneToken}, BaseFeeDiscountBps: 5_000, TipCap: big.NewInt(30_000_000_000), ActiveGasPercentage: 70}
	v2 := &params.PaymentLaneConfig{Assets: []common.Address{laneToken}, BaseFeeDiscountBps: 2_500, TipCap: big.NewInt(60_000_000_000), ActiveGasPercentage: 70}
	c := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": v1, "3": v2})
	payment := transferInput(lanePayee, 1)

	blocks, receipts := c.generate(5, func(i int, b *BlockGen) {
		b.AddTx(c.sign(b, laneToken, 100_000, payment))
	})

	chain := c.importBoth(blocks)

	for i, block := range blocks {
		lane := v1
		if block.NumberU64() >= retune {
			lane = v2
		}

		requirePaymentPrice(t, lane, 10_000, block, receipts[i], c.paid(chain, blocks, i))
	}

	// A second import from genesis, with the versions given in the other
	// order, reaches the same roots.
	again := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"3": v2, "1": v1})
	replay := again.importBoth(blocks)
	require.Equal(t, blocks[len(blocks)-1].Root(), replay.CurrentBlock().Root)

	stale := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": v1})
	staleChain, err := NewBlockChain(rawdb.NewMemoryDatabase(), stale.gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(t, err)
	defer staleChain.Stop()

	n, err := staleChain.InsertChain(blocks, false)
	require.Error(t, err)
	require.Equal(t, retune-1, n, "the first block priced differently is the retune block")
}

// transferFromInput is transferFrom(from, to, amount).
func transferFromInput(from, to common.Address, amount int64) []byte {
	out := []byte{0x23, 0xb8, 0x72, 0xdd}
	out = append(out, common.LeftPadBytes(from[:], 32)...)
	out = append(out, common.LeftPadBytes(to[:], 32)...)

	return append(out, common.LeftPadBytes(big.NewInt(amount).Bytes(), 32)...)
}

// An allowance pull (the sender moves another holder's tokens) is a payment
// only when CountAllowancePulls is set.
func TestPaymentLaneAllowancePullOnImport(t *testing.T) {
	t.Parallel()

	for _, count := range []bool{true, false} {
		lane := &params.PaymentLaneConfig{
			Assets: []common.Address{laneToken}, BaseFeeDiscountBps: 7_500, TipCap: big.NewInt(30_000_000_000),
			ActiveGasPercentage: 70, CountAllowancePulls: count,
		}
		c := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": lane})
		pull := transferFromInput(laneWallet, lanePayee, 1)

		blocks, receipts := c.generate(2, func(i int, b *BlockGen) {
			b.AddTx(c.sign(b, laneToken, 100_000, pull))
		})

		chain := c.importBoth(blocks)

		for i, block := range blocks {
			if count {
				requirePaymentPrice(t, lane, 10_000, block, receipts[i], c.paid(chain, blocks, i))
				continue
			}

			require.Empty(t, laneLogsOf(receipts[i][0].Logs), "block %d", block.NumberU64())

			require.Equal(t, nominalPrice(block, receipts[i]).String(), c.paid(chain, blocks, i).String(), "block %d", block.NumberU64())
		}
	}
}

// A ramp and a non-payment cap in the same version: a parent inside the ramp
// window halves the discount while the cap still logs every payment, and a
// block whose non-payment gas goes over the cap is rejected by both importers.
func TestPaymentLaneRampWithCapOnImport(t *testing.T) {
	t.Parallel()

	lane := rampLane()
	lane.NonPaymentGasCapPercentage = 80

	t.Run("within cap", func(t *testing.T) {
		t.Parallel()

		c := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": lane})
		payment := transferInput(lanePayee, 1)

		blocks, receipts := c.generate(3, func(i int, b *BlockGen) {
			b.AddTx(c.sign(b, laneToken, 100_000, payment))

			if i == 0 {
				c.fillTo(b, 70)
			}
		})

		chain := c.importBoth(blocks)

		requirePaymentPrice(t, lane, 5_000, blocks[1], receipts[1], c.paid(chain, blocks, 1))

		// Under 50% of the limit, so block 3 gets the full discount.
		requirePaymentPrice(t, lane, 10_000, blocks[2], receipts[2], c.paid(chain, blocks, 2))
	})

	t.Run("over cap", func(t *testing.T) {
		t.Parallel()

		c := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": lane})
		payment := transferInput(lanePayee, 1)

		blocks, _ := c.generate(2, func(i int, b *BlockGen) {
			b.AddTx(c.sign(b, laneToken, 100_000, payment))

			if i == 1 {
				c.fillTo(b, 85)
			}
		})

		for name, open := range map[string]func() (*BlockChain, error){
			"serial": func() (*BlockChain, error) {
				return NewBlockChain(rawdb.NewMemoryDatabase(), c.gspec, ethash.NewFaker(), DefaultConfig())
			},
			"blockstm v2": func() (*BlockChain, error) {
				return NewParallelBlockChain(rawdb.NewMemoryDatabase(), c.gspec, ethash.NewFaker(), DefaultConfig(), 8, true)
			},
		} {
			bc, err := open()
			require.NoError(t, err, name)

			n, err := bc.InsertChain(blocks, false)
			require.ErrorIs(t, err, ErrNonPaymentGasCap, name)
			require.Equal(t, 1, n, name)
			bc.Stop()
		}
	})
}

// Under the preset a payment pays its full nominal tip and 80% off the base
// fee in full while the parent is at or below 40% of its limit; a parent at
// 55%, halfway to the ramp end at 70%, halves the cut.
func TestPaymentLanePresetOnImport(t *testing.T) {
	t.Parallel()

	lane := params.DefaultPaymentLaneConfig([]common.Address{laneToken})
	c := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": lane})
	payment := transferInput(lanePayee, 1)

	blocks, receipts := c.generate(3, func(i int, b *BlockGen) {
		b.AddTx(c.sign(b, laneToken, 100_000, payment))

		switch i {
		case 0:
			c.fillTo(b, 40)
		case 1:
			c.fillTo(b, 55)
		}
	})

	chain := c.importBoth(blocks)

	require.Equal(t, uint64(laneChainGasLimit*40/100), blocks[0].GasUsed())
	require.Equal(t, uint64(laneChainGasLimit*55/100), blocks[1].GasUsed())

	for i, scale := range []uint64{10_000, 10_000, 5_000} {
		block := blocks[i]
		got, ok := PaymentLaneFromLogs(receipts[i][0].Logs)
		require.True(t, ok, "block %d", block.NumberU64())
		require.Zero(t, got.TipCut.Sign(), "block %d tip cut", block.NumberU64())

		cut := new(big.Int).Div(new(big.Int).Mul(block.BaseFee(), big.NewInt(8_000)), big.NewInt(10_000))
		cut.Mul(cut, big.NewInt(int64(scale))).Div(cut, big.NewInt(10_000))
		require.Equal(t, cut.Mul(cut, new(big.Int).SetUint64(got.Gas)).String(), got.BaseCut.String(), "block %d base cut", block.NumberU64())

		requirePaymentPrice(t, lane, scale, block, receipts[i], c.paid(chain, blocks, i))
	}
}

// Three very different versions in one chain, imported serially and with
// BlockSTM: A (base and tip discounts, tip cap, hard switch at 50%), B (payment
// gas free, gate at the 65% target) and C (the token is no longer a listed asset).
// A node that misses version C rejects the first block where the token stops
// being discounted.
func TestPaymentLaneLeverVersionsOnImport(t *testing.T) {
	t.Parallel()

	a := &params.PaymentLaneConfig{
		Assets: []common.Address{laneToken}, BaseFeeDiscountBps: 5_000, TipDiscountBps: 5_000,
		TipCap: big.NewInt(2_000_000_000), ActiveGasPercentage: 50,
	}
	b := &params.PaymentLaneConfig{Assets: []common.Address{laneToken}, BaseFeeDiscountBps: 10_000, TipDiscountBps: 10_000, ActiveGasPercentage: 65}
	c := &params.PaymentLaneConfig{Assets: []common.Address{laneBurner}, BaseFeeDiscountBps: 2_500, ActiveGasPercentage: 30, RampEndGasPercentage: 31}
	versions := map[string]*params.PaymentLaneConfig{"1": a, "3": b, "5": c}

	lc := newLaneChain(t, 1, versions)
	payment := transferInput(lanePayee, 1)

	blocks, receipts := lc.generate(6, func(i int, bg *BlockGen) {
		bg.AddTx(lc.sign(bg, laneToken, 100_000, payment))

		switch bg.header.Number.Uint64() {
		case 1:
			lc.fillTo(bg, 60) // past A's hard switch: block 2 gets nothing
		case 3:
			lc.fillTo(bg, 60) // B's gate is 65: block 4 still gets everything
		}
	})

	chain := lc.importBoth(blocks)

	for i, block := range blocks {
		paid := lc.paid(chain, blocks, i)

		switch n := block.NumberU64(); n {
		case 1:
			requirePaymentPrice(t, a, 10_000, block, receipts[i], paid)
		case 3, 4:
			requirePaymentPrice(t, b, 10_000, block, receipts[i], paid)

			got, _ := PaymentLaneFromLogs(receipts[i][0].Logs)
			price := new(big.Int).Add(block.BaseFee(), laneChainTip)
			require.Equal(t, new(big.Int).Mul(price, new(big.Int).SetUint64(got.Gas)).String(),
				new(big.Int).Add(got.BaseCut, got.TipCut).String(), "block %d: payment gas is free", n)
		default:
			_, ok := PaymentLaneFromLogs(receipts[i][0].Logs)
			require.False(t, ok, "block %d: no discount", n)
			require.Equal(t, nominalPrice(block, receipts[i]).String(), paid.String(), "block %d paid", n)
		}
	}

	stale := newLaneChain(t, 1, map[string]*params.PaymentLaneConfig{"1": a, "3": b})
	staleChain, err := NewBlockChain(rawdb.NewMemoryDatabase(), stale.gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(t, err)
	defer staleChain.Stop()

	n, err := staleChain.InsertChain(blocks, false)
	require.Error(t, err)
	require.Equal(t, 4, n, "the first block priced differently is block 5, where the token is dropped")
}
