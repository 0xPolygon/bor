package core

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// TestPaymentLaneChainBoundaries builds a chain that crosses the fork at
// block 3 and a congested block at 4, then re-imports it: the importer only
// accepts the blocks if it derives the same lane prices from chain data.
func TestPaymentLaneChainBoundaries(t *testing.T) {
	t.Parallel()

	const (
		forkBlock = 3
		gasLimit  = 2_000_000
	)

	var (
		key, _   = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender   = crypto.PubkeyToAddress(key.PublicKey)
		token    = common.HexToAddress("0x70ce0001")
		burner   = common.HexToAddress("0xb0b0")
		coinbase = common.HexToAddress("0xc0ffee")
		tip      = big.NewInt(100_000_000_000)
		tipCap   = big.NewInt(30_000_000_000)
	)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(forkBlock)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): {Assets: []common.Address{token}, BaseFeeDiscountBps: 5000, TipCap: tipCap, ActiveGasPercentage: 70}}
	cfg.Bor = &bor

	gspec := &Genesis{
		Config:   &cfg,
		GasLimit: gasLimit,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000))},
			token:  {Code: laneTestToken},
			burner: {Code: []byte{byte(vm.INVALID)}},
		},
	}
	signer := types.LatestSigner(&cfg)

	sign := func(b *BlockGen, to common.Address, gas uint64, data []byte) *types.Transaction {
		feeCap := new(big.Int).Add(new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), tip)
		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID: cfg.ChainID, Nonce: b.TxNonce(sender), GasTipCap: tip, GasFeeCap: feeCap,
			Gas: gas, To: &to, Data: data,
		})
		require.NoError(t, err)

		return tx
	}

	payee := common.HexToAddress("0xbee")
	payment := transferInput(payee, 1)

	// Block 4 is filled to 75% by a burner tx, so block 5 runs with the lane off.
	_, blocks, receipts := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 6, func(i int, b *BlockGen) {
		b.SetCoinbase(coinbase)
		b.AddTx(sign(b, token, 100_000, payment))

		if b.Number().Uint64() == 4 {
			b.AddTx(sign(b, burner, 1_500_000, nil))
		}
	})

	serial, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(t, err)
	defer serial.Stop()

	n, err := serial.InsertChain(blocks, false)
	require.NoError(t, err, "serial import, block %d", n)

	// BlockSTM V2 settles the lane refund on the sender inside the tx and the
	// burn/tip from the result fields; it must reach the same state roots.
	parallel, err := NewParallelBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig(), 8, true)
	require.NoError(t, err)
	defer parallel.Stop()

	n, err = parallel.InsertChain(blocks, false)
	require.NoError(t, err, "parallel import, block %d", n)

	chain := serial

	wantLane := map[uint64]bool{1: false, 2: false, 3: true, 4: true, 5: false, 6: true}

	prev := chain.Genesis().Root()
	for i, block := range blocks {
		num := block.NumberU64()
		before, err := chain.StateAt(prev)
		require.NoError(t, err)
		after, err := chain.StateAt(block.Root())
		require.NoError(t, err)

		paid := new(big.Int).Sub(before.GetBalance(sender).ToBig(), after.GetBalance(sender).ToBig())

		var want big.Int
		for j, tx := range block.Transactions() {
			gas := new(big.Int).SetUint64(receipts[i][j].GasUsed)
			price := new(big.Int).Set(block.BaseFee())

			isLane := *tx.To() == token && wantLane[num]
			if isLane {
				require.Len(t, laneLogsOf(receipts[i][j].Logs), 1, "block %d", num)
			} else {
				require.Empty(t, laneLogsOf(receipts[i][j].Logs), "block %d", num)
			}

			if isLane {
				price.Sub(price, new(big.Int).Div(block.BaseFee(), big.NewInt(2)))
				price.Add(price, tipCap)
			} else {
				price.Add(price, tip)
			}

			want.Add(&want, gas.Mul(gas, price))
		}

		require.Equal(t, want.String(), paid.String(), "block %d", num)
		prev = block.Root()
	}
}

type laneGateChain struct {
	consensus.ChainHeaderReader // unused methods panic, which the test would surface

	cfg    *params.ChainConfig
	parent *types.Header
}

func (c *laneGateChain) Config() *params.ChainConfig { return c.cfg }
func (c *laneGateChain) Engine() consensus.Engine    { return nil }

func (c *laneGateChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	if c.parent == nil || c.parent.Hash() != hash || c.parent.Number.Uint64() != number {
		return nil
	}

	return c.parent
}

func TestPaymentLaneActiveGate(t *testing.T) {
	t.Parallel()

	laneCfg := laneChainConfig(t)
	laneCfg.Bor.PaymentLaneBlock = big.NewInt(10)

	parentAt := func(used uint64) *types.Header {
		return &types.Header{Number: big.NewInt(10), GasLimit: 100, GasUsed: used, Difficulty: common.Big1}
	}
	childOf := func(p *types.Header) *types.Header {
		return &types.Header{Number: big.NewInt(11), ParentHash: p.Hash(), Difficulty: common.Big1}
	}

	quiet := parentAt(70)
	busy := parentAt(71)

	tests := []struct {
		name   string
		header *types.Header
		chain  ChainContext
		want   bool
	}{
		{"quiet parent", childOf(quiet), &laneGateChain{cfg: laneCfg, parent: quiet}, true},
		{"parent above target", childOf(busy), &laneGateChain{cfg: laneCfg, parent: busy}, false},
		{"parent not found", childOf(quiet), &laneGateChain{cfg: laneCfg}, false},
		{"before fork", &types.Header{Number: big.NewInt(9), ParentHash: quiet.Hash()}, &laneGateChain{cfg: laneCfg, parent: quiet}, false},
		{"no bor config", childOf(quiet), &laneGateChain{cfg: params.TestChainConfig, parent: quiet}, false},
		{"no chain config", childOf(quiet), &laneGateChain{parent: quiet}, false},
		{"nil chain", childOf(quiet), nil, false},
		{"genesis", &types.Header{Number: big.NewInt(0)}, &laneGateChain{cfg: laneCfg, parent: quiet}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, laneScale(tt.want), paymentLaneScale(tt.header, tt.chain))
		})
	}
}

// Every BlockGen entry point must resolve the lane parent from the blocks
// generated so far, so generated blocks match what an importer derives. A
// real BlockChain does not hold them yet.
func TestPaymentLaneChainMakerChainContext(t *testing.T) {
	t.Parallel()

	var (
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender = crypto.PubkeyToAddress(key.PublicKey)
		token  = common.HexToAddress("0x70ce0001")
	)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(1)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): {Assets: []common.Address{token}, BaseFeeDiscountBps: 5000, ActiveGasPercentage: 70}}
	cfg.Bor = &bor

	gspec := &Genesis{
		Config:   &cfg,
		GasLimit: 2_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: big.NewInt(1e18)},
			token:  {Code: laneTestToken},
		},
	}
	signer := types.LatestSigner(&cfg)

	helper, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(t, err)
	defer helper.Stop()

	tests := []struct {
		name string
		add  func(*BlockGen, *types.Transaction)
	}{
		{"AddTx", func(b *BlockGen, tx *types.Transaction) { b.AddTx(tx) }},
		{"AddTxWithVMConfig", func(b *BlockGen, tx *types.Transaction) { b.AddTxWithVMConfig(tx, vm.Config{}) }},
		{"AddTxWithChain", func(b *BlockGen, tx *types.Transaction) { b.AddTxWithChain(helper, tx) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, blocks, receipts := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 3, func(i int, b *BlockGen) {
				tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
					ChainID: cfg.ChainID, Nonce: b.TxNonce(sender), GasTipCap: common.Big1,
					GasFeeCap: new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), Gas: 100_000, To: &token,
					Data: transferInput(common.HexToAddress("0xbee"), 1),
				})
				require.NoError(t, err)
				tt.add(b, tx)
			})

			for i, rs := range receipts {
				require.Len(t, laneLogsOf(rs[0].Logs), 1, "block %d", i+1)
			}

			chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
			require.NoError(t, err)
			defer chain.Stop()

			n, err := chain.InsertChain(blocks, false)
			require.NoError(t, err, "block %d", n)
		})
	}
}

// laneCapChain builds a 3-block chain with the lane fork at block 2, a 70%
// non-payment cap, a payment in every block and a burner tx of burnGas in
// block burnBlock.
func laneCapChain(t *testing.T, burnBlock uint64, burnGas uint64) (*Genesis, []*types.Block, []types.Receipts) {
	t.Helper()

	var (
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		sender = crypto.PubkeyToAddress(key.PublicKey)
		token  = common.HexToAddress("0x70ce0001")
		burner = common.HexToAddress("0xb0b0")
	)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(2)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{bor.PaymentLaneBlock.String(): {
		Assets: []common.Address{token}, BaseFeeDiscountBps: 5000, ActiveGasPercentage: 70, NonPaymentGasCapPercentage: 70,
	}}
	cfg.Bor = &bor

	gspec := &Genesis{
		Config:   &cfg,
		GasLimit: 2_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000))},
			token:  {Code: laneTestToken},
			burner: {Code: []byte{byte(vm.INVALID)}},
		},
	}
	signer := types.LatestSigner(&cfg)

	sign := func(b *BlockGen, to common.Address, gas uint64, data []byte) *types.Transaction {
		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID: cfg.ChainID, Nonce: b.TxNonce(sender), GasTipCap: common.Big1,
			GasFeeCap: new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), Gas: gas, To: &to, Data: data,
		})
		require.NoError(t, err)

		return tx
	}

	_, blocks, receipts := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 3, func(i int, b *BlockGen) {
		b.AddTx(sign(b, token, 100_000, transferInput(common.HexToAddress("0xbee"), 1)))

		if b.Number().Uint64() == burnBlock {
			b.AddTx(sign(b, burner, burnGas, nil))
		}
	})

	return gspec, blocks, receipts
}

func TestPaymentLaneNonPaymentGasCap(t *testing.T) {
	t.Parallel()

	importBoth := func(t *testing.T, gspec *Genesis, blocks []*types.Block) (serialErr, parallelErr error) {
		t.Helper()

		serial, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
		require.NoError(t, err)
		defer serial.Stop()

		parallel, err := NewParallelBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig(), 8, true)
		require.NoError(t, err)
		defer parallel.Stop()

		_, serialErr = serial.InsertChain(blocks, false)
		_, parallelErr = parallel.InsertChain(blocks, false)

		return serialErr, parallelErr
	}

	t.Run("within cap", func(t *testing.T) {
		t.Parallel()

		gspec, blocks, receipts := laneCapChain(t, 2, 900_000)
		s, p := importBoth(t, gspec, blocks)
		require.NoError(t, s)
		require.NoError(t, p)

		// With a cap configured every payment is logged, discounted or not:
		// block 3 follows a parent below the gate, block 2's payment too.
		for i, rs := range receipts[1:] {
			require.Len(t, laneLogsOf(rs[0].Logs), 1, "block %d", i+2)
		}

		require.Empty(t, laneLogsOf(receipts[0][0].Logs), "before the fork")
	})

	t.Run("over cap is rejected", func(t *testing.T) {
		t.Parallel()

		gspec, blocks, _ := laneCapChain(t, 2, 1_500_000)
		s, p := importBoth(t, gspec, blocks)
		require.ErrorIs(t, s, ErrNonPaymentGasCap)
		require.ErrorIs(t, p, ErrNonPaymentGasCap)
	})

	t.Run("cap does not apply before the fork", func(t *testing.T) {
		t.Parallel()

		gspec, blocks, _ := laneCapChain(t, 1, 1_500_000)
		s, p := importBoth(t, gspec, blocks)
		require.NoError(t, s)
		require.NoError(t, p)
	})
}

func TestValidatePaymentLaneCapacity(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).NonPaymentGasCapPercentage = 50
	header := &types.Header{Number: big.NewInt(10), GasLimit: 1_000, GasUsed: 900}
	payment := func(gas int64) types.Receipts {
		return types.Receipts{{Logs: []*types.Log{laneLog(gas, 0, 0)}}}
	}

	require.NoError(t, ValidatePaymentLaneCapacity(cfg, header, payment(400)))
	require.ErrorIs(t, ValidatePaymentLaneCapacity(cfg, header, payment(399)), ErrNonPaymentGasCap)
	require.NoError(t, ValidatePaymentLaneCapacity(cfg, header, payment(5_000)), "payment gas above gas used")

	cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).NonPaymentGasCapPercentage = 0
	require.NoError(t, ValidatePaymentLaneCapacity(cfg, header, nil))
	require.NoError(t, ValidatePaymentLaneCapacity(params.TestChainConfig, header, nil))
}

// Serial, BlockSTM V1 and BlockSTM V2 imports must agree on blocks where many
// senders pay at once, directly and through a wallet, next to non-payment
// traffic. Either one of the payers is the block's coinbase (its refund and
// its tip land on the same account), or the coinbase sends nothing, which is
// what lets BlockSTM V1 defer burn and tip to the end of the block.
func TestPaymentLaneExecutorParity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		capPct        uint64
		coinbasePayer bool
	}{{0, true}, {90, true}, {0, false}, {90, false}} {
		capPct := tc.capPct
		t.Run(fmt.Sprintf("cap %d, coinbase pays %t", capPct, tc.coinbasePayer), func(t *testing.T) {
			t.Parallel()

			var (
				token  = common.HexToAddress("0x70ce0001")
				wallet = common.HexToAddress("0x3a11e701")
				burner = common.HexToAddress("0xb0b0")
				keys   = make([]*ecdsa.PrivateKey, 6)
				alloc  = types.GenesisAlloc{
					token:  {Code: laneTestToken},
					wallet: {Code: forwarder(token, false)},
					burner: {Code: []byte{byte(vm.INVALID)}},
				}
			)

			for i := range keys {
				keys[i], _ = crypto.GenerateKey()
				alloc[crypto.PubkeyToAddress(keys[i].PublicKey)] = types.Account{Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(100))}
			}

			cfg := *params.BorUnittestChainConfig
			bor := *cfg.Bor
			bor.PaymentLaneBlock = big.NewInt(1)
			bor.PaymentLane = map[string]*params.PaymentLaneConfig{"1": {
				Assets: []common.Address{token}, BaseFeeDiscountBps: 7_500, TipCap: big.NewInt(30_000_000_000),
				ActiveGasPercentage: 70, NonPaymentGasCapPercentage: capPct,
			}}
			cfg.Bor = &bor

			gspec := &Genesis{Config: &cfg, GasLimit: 10_000_000, BaseFee: big.NewInt(params.InitialBaseFee), Alloc: alloc}
			signer := types.LatestSigner(&cfg)
			coinbase := common.HexToAddress("0xc0ffee")
			if tc.coinbasePayer {
				coinbase = crypto.PubkeyToAddress(keys[0].PublicKey)
			}
			payment := transferInput(common.HexToAddress("0xbee"), 1)

			_, blocks, receipts := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 4, func(i int, b *BlockGen) {
				b.SetCoinbase(coinbase)

				for j, key := range keys {
					to, data, gas := token, payment, uint64(100_000)
					switch (i + j) % 3 {
					case 1:
						to = wallet
					case 2:
						to, data, gas = burner, nil, 50_000
					}

					tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
						ChainID: cfg.ChainID, Nonce: b.TxNonce(crypto.PubkeyToAddress(key.PublicKey)),
						GasTipCap: big.NewInt(100_000_000_000), GasFeeCap: new(big.Int).Add(new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), big.NewInt(100_000_000_000)),
						Gas: gas, To: &to, Data: data,
					})
					require.NoError(t, err)
					b.AddTx(tx)
				}
			})

			// The cap compares header gas used with payment gas from the
			// receipts, so both must stay in the same unit: post-refund gas
			// that sums to the header's (an upstream change to block gas
			// accounting would break this silently otherwise).
			var lanes int
			for i, rs := range receipts {
				var used uint64
				for _, r := range rs {
					used += r.GasUsed
					lanes += len(laneLogsOf(r.Logs))
					require.LessOrEqual(t, PaymentLaneCapacityFromLogs(r.Logs), r.GasUsed)
				}

				require.Equal(t, blocks[i].GasUsed(), used, "block %d", blocks[i].NumberU64())
			}

			require.Positive(t, lanes)

			imports := map[string]func() (*BlockChain, error){
				"serial": func() (*BlockChain, error) {
					return NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
				},
				"blockstm v1": func() (*BlockChain, error) {
					bc, err := NewParallelBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig(), 8, true)
					if err == nil {
						bc.parallelProcessor = NewParallelStateProcessor(bc.hc, bc)
					}

					return bc, err
				},
				"blockstm v2": func() (*BlockChain, error) {
					return NewParallelBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig(), 8, true)
				},
			}

			for name, open := range imports {
				bc, err := open()
				require.NoError(t, err, name)

				n, err := bc.InsertChain(blocks, false)
				require.NoError(t, err, "%s import, block %d", name, n)
				require.Equal(t, blocks[len(blocks)-1].Root(), bc.CurrentBlock().Root, name)
				bc.Stop()
			}
		})
	}
}

// The gate on import: a parent at exactly the active percentage of its gas
// limit leaves the lane on for the child, one gas more turns it off.
func TestPaymentLaneActiveGateOnImport(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		extra uint64
		lane  bool
	}{{"at gate", 0, true}, {"one gas above", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
				sender = crypto.PubkeyToAddress(key.PublicKey)
				token  = common.HexToAddress("0x70ce0001")
				burner = common.HexToAddress("0xb0b0")
			)

			cfg := *params.BorUnittestChainConfig
			bor := *cfg.Bor
			bor.PaymentLaneBlock = big.NewInt(1)
			bor.PaymentLane = map[string]*params.PaymentLaneConfig{"1": {Assets: []common.Address{token}, BaseFeeDiscountBps: 5000, ActiveGasPercentage: 70}}
			cfg.Bor = &bor

			gspec := &Genesis{
				Config:   &cfg,
				GasLimit: 2_000_000,
				BaseFee:  big.NewInt(params.InitialBaseFee),
				Alloc: types.GenesisAlloc{
					sender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000))},
					token:  {Code: laneTestToken},
					burner: {Code: []byte{byte(vm.INVALID)}},
				},
			}
			signer := types.LatestSigner(&cfg)

			sign := func(b *BlockGen, to common.Address, gas uint64, data []byte) *types.Transaction {
				tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
					ChainID: cfg.ChainID, Nonce: b.TxNonce(sender), GasTipCap: common.Big1,
					GasFeeCap: new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), Gas: gas, To: &to, Data: data,
				})
				require.NoError(t, err)

				return tx
			}

			_, blocks, receipts := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 2, func(i int, b *BlockGen) {
				b.AddTx(sign(b, token, 100_000, transferInput(common.HexToAddress("0xbee"), 1)))

				if i == 0 {
					// The burner uses all its gas, so the block lands on gate + extra.
					gate := b.header.GasLimit * 70 / 100
					b.AddTx(sign(b, burner, gate+tc.extra-b.header.GasUsed, nil))
				}
			})

			gate := blocks[0].GasLimit() * 70 / 100
			require.Equal(t, gate+tc.extra, blocks[0].GasUsed())

			if tc.lane {
				require.Len(t, laneLogsOf(receipts[1][0].Logs), 1)
			} else {
				require.Empty(t, laneLogsOf(receipts[1][0].Logs))
			}

			chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
			require.NoError(t, err)
			defer chain.Stop()

			n, err := chain.InsertChain(blocks, false)
			require.NoError(t, err, "block %d", n)
		})
	}
}

// A payment that fails after the fork pays full price and makes no payment:
// no discount, and no lane log unless a cap needs its attempt counted.
func TestPaymentLaneFailedPaymentOnImport(t *testing.T) {
	t.Parallel()

	for _, capPct := range []uint64{0, 70} {
		t.Run(fmt.Sprintf("cap %d", capPct), func(t *testing.T) {
			t.Parallel()

			var (
				key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
				sender = crypto.PubkeyToAddress(key.PublicKey)
				token  = common.HexToAddress("0x70ce0001")
				tip    = big.NewInt(100_000_000_000)
			)

			cfg := *params.BorUnittestChainConfig
			bor := *cfg.Bor
			bor.PaymentLaneBlock = big.NewInt(1)
			bor.PaymentLane = map[string]*params.PaymentLaneConfig{"1": {
				Assets: []common.Address{token}, BaseFeeDiscountBps: 5000, TipCap: big.NewInt(30_000_000_000),
				ActiveGasPercentage: 70, NonPaymentGasCapPercentage: capPct,
			}}
			cfg.Bor = &bor

			gspec := &Genesis{
				Config:   &cfg,
				GasLimit: 2_000_000,
				BaseFee:  big.NewInt(params.InitialBaseFee),
				Alloc: types.GenesisAlloc{
					sender: {Balance: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000))},
					token:  {Code: laneTestToken},
				},
			}
			signer := types.LatestSigner(&cfg)

			// The token logs its transfer, then runs out of gas on its SSTORE.
			_, blocks, receipts := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 1, func(i int, b *BlockGen) {
				tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
					ChainID: cfg.ChainID, Nonce: b.TxNonce(sender), GasTipCap: tip,
					GasFeeCap: new(big.Int).Add(new(big.Int).Mul(b.header.BaseFee, big.NewInt(2)), tip), Gas: 30_000,
					To: &token, Data: transferInput(common.HexToAddress("0xbee"), 1),
				})
				require.NoError(t, err)
				b.AddTx(tx)
			})

			receipt := receipts[0][0]
			require.Equal(t, types.ReceiptStatusFailed, receipt.Status)

			_, paid := PaymentLaneFromLogs(receipt.Logs)
			require.False(t, paid)

			if capPct == 0 {
				require.Empty(t, laneLogsOf(receipt.Logs))
			}

			chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
			require.NoError(t, err)
			defer chain.Stop()

			n, err := chain.InsertChain(blocks, false)
			require.NoError(t, err, "block %d", n)

			before, err := chain.StateAt(chain.Genesis().Root())
			require.NoError(t, err)
			after, err := chain.StateAt(blocks[0].Root())
			require.NoError(t, err)

			price := new(big.Int).Add(blocks[0].BaseFee(), tip)
			want := price.Mul(price, new(big.Int).SetUint64(receipt.GasUsed))
			got := new(big.Int).Sub(before.GetBalance(sender).ToBig(), after.GetBalance(sender).ToBig())
			require.Equal(t, want.String(), got.String())
		})
	}
}
