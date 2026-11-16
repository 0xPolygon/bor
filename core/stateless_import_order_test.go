package core

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

func transferTx(key *ecdsa.PrivateKey, nonce uint64, value int64, baseFee *big.Int) *types.Transaction {
	return types.MustSignNewTx(key, types.LatestSigner(params.TestChainConfig), &types.LegacyTx{
		Nonce: nonce, To: &common.Address{1}, Value: big.NewInt(value),
		Gas: params.TxGas, GasPrice: baseFee,
	})
}

func TestInsertChainStatelessVerifiesBeforeExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		failAt uint64
		// prepare may store or replace blocks before the stateless import.
		prepare func(t *testing.T, chain *BlockChain, key *ecdsa.PrivateKey, blocks types.Blocks)
		wantErr string
		wantN   int
	}{
		{name: "failing header", failAt: 2, wantErr: "invalid tester pow", wantN: 1},
		{
			name:   "known block with failing header",
			failAt: 2,
			prepare: func(t *testing.T, chain *BlockChain, _ *ecdsa.PrivateKey, blocks types.Blocks) {
				rawdb.WriteBlock(chain.db, blocks[1])
			},
			wantErr: "header verification failed for known block",
			wantN:   1,
		},
		{
			name: "body does not match header",
			prepare: func(t *testing.T, _ *BlockChain, key *ecdsa.PrivateKey, blocks types.Blocks) {
				swapped := transferTx(key, 2, 2, blocks[2].BaseFee())
				blocks[2] = types.NewBlockWithHeader(blocks[2].Header()).WithBody(types.Body{Transactions: types.Transactions{swapped}})
			},
			wantErr: "invalid block body",
			wantN:   2,
		},
	}
	for _, tt := range tests {
		for _, parallel := range []bool{false, true} {
			mode := "sequential"
			if parallel {
				mode = "parallel"
			}
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				t.Parallel()

				key, _ := crypto.GenerateKey()
				gspec := &Genesis{
					Config: params.TestChainConfig,
					Alloc:  types.GenesisAlloc{crypto.PubkeyToAddress(key.PublicKey): {Balance: big.NewInt(params.Ether)}},
				}
				_, blocks, _ := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 3, func(i int, b *BlockGen) {
					b.AddTx(transferTx(key, uint64(i), 1, b.BaseFee()))
				})

				full, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
				require.NoError(t, err)
				defer full.Stop()
				witnesses := make([]*stateless.Witness, len(blocks))
				for i, blk := range blocks {
					witnesses[i], _, err = full.insertChain(types.Blocks{blk}, true, true)
					require.NoError(t, err)
				}

				var executed atomic.Int32
				cfg := DefaultConfig()
				cfg.Stateless = true
				cfg.VmConfig.Tracer = &tracing.Hooks{
					OnTxStart: func(*tracing.VMContext, *types.Transaction, common.Address) { executed.Add(1) },
				}
				chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFakeFailer(tt.failAt), cfg)
				require.NoError(t, err)
				defer chain.Stop()
				if parallel {
					chain.ParallelStatelessImportEnable()
					chain.SetParallelStatelessImportWorkers(len(blocks))
				}
				if tt.prepare != nil {
					tt.prepare(t, chain, key, blocks)
				}

				n, err := chain.InsertChainStateless(blocks, witnesses)
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, tt.wantN, n)
				require.Equal(t, int32(tt.wantN), executed.Load())
				require.Equal(t, uint64(tt.wantN), chain.CurrentBlock().Number.Uint64())
			})
		}
	}
}

func TestAwaitHeaderResults(t *testing.T) {
	t.Parallel()

	errA, errB := errors.New("a"), errors.New("b")
	tests := []struct {
		name         string
		results      []error
		wantFirstBad int
	}{
		{name: "all valid", results: []error{nil, nil, nil}, wantFirstBad: 3},
		{name: "first failure wins", results: []error{nil, errA, errB}, wantFirstBad: 1},
		{name: "failure at start", results: []error{errA, nil, errB}, wantFirstBad: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chans := make([]chan error, len(tt.results))
			for i, err := range tt.results {
				chans[i] = make(chan error, 1)
				chans[i] <- err
			}
			errs, firstBad := awaitHeaderResults(chans)
			require.Equal(t, tt.results, errs)
			require.Equal(t, tt.wantFirstBad, firstBad)
		})
	}
}
