package bor

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

type statelessImportFixture struct {
	chain     *core.BlockChain
	cfg       *params.ChainConfig
	genesis   *types.Header
	signerKey *ecdsa.PrivateKey
	userKey   *ecdsa.PrivateKey
}

func newStatelessImportFixture(t *testing.T) *statelessImportFixture {
	t.Helper()

	signerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	borCfg := &params.BorConfig{
		Sprint:        map[string]uint64{"0": 64},
		Period:        map[string]uint64{"0": 2},
		RioBlock:      big.NewInt(0),
		BurntContract: map[string]string{"0": "0x000000000000000000000000000000000000dead"},
	}
	cfg := newAllForksChainConfig(borCfg)
	cfg.ShanghaiBlock = big.NewInt(0)
	cfg.CancunBlock = big.NewInt(0)

	sp := &fakeSpanner{vals: []*valset.Validator{{Address: crypto.PubkeyToAddress(signerKey.PublicKey), VotingPower: 1}}}
	chain, _ := newChainAndBorForTestWithConfig(t, sp, cfg, false, common.Address{}, uint64(1_700_000_000), func(g *core.Genesis) {
		g.GasLimit = 30_000_000
		g.BaseFee = big.NewInt(params.InitialBaseFee)
		g.Alloc = types.GenesisAlloc{
			crypto.PubkeyToAddress(userKey.PublicKey): {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)},
		}
	})
	t.Cleanup(chain.Stop)

	return &statelessImportFixture{chain: chain, cfg: cfg, genesis: chain.CurrentBlock(), signerKey: signerKey, userKey: userKey}
}

func (f *statelessImportFixture) tx(t *testing.T, blob bool) *types.Transaction {
	t.Helper()

	var inner types.TxData = &types.DynamicFeeTx{
		ChainID: f.cfg.ChainID, GasTipCap: big.NewInt(params.InitialBaseFee),
		GasFeeCap: big.NewInt(2 * params.InitialBaseFee), Gas: params.TxGas, To: &common.Address{1},
	}
	if blob {
		inner = &types.BlobTx{
			ChainID: uint256.MustFromBig(f.cfg.ChainID), GasTipCap: uint256.NewInt(params.InitialBaseFee),
			GasFeeCap: uint256.NewInt(2 * params.InitialBaseFee), Gas: params.TxGas, To: common.Address{1},
			BlobFeeCap: uint256.NewInt(params.BlobTxMinBlobGasprice), BlobHashes: []common.Hash{{0x01}},
		}
	}
	tx, err := types.SignTx(types.NewTx(inner), types.LatestSigner(f.cfg), f.userKey)
	require.NoError(t, err)
	return tx
}

func (f *statelessImportFixture) block(t *testing.T, sealer *ecdsa.PrivateKey, tx *types.Transaction, blobFields bool) *types.Block {
	t.Helper()

	h := &types.Header{
		ParentHash: f.genesis.Hash(), Number: big.NewInt(1), Time: f.genesis.Time + 2, Difficulty: big.NewInt(1),
		GasLimit: f.genesis.GasLimit, BaseFee: eip1559.CalcBaseFee(f.cfg, f.genesis),
	}
	if blobFields {
		zero, used := uint64(0), uint64(params.BlobTxBlobGasPerBlob)
		h.ExcessBlobGas, h.BlobGasUsed = &zero, &used
	}
	unsigned := types.NewBlock(h, &types.Body{Transactions: types.Transactions{tx}}, nil, trie.NewStackTrie(nil))
	sealed := unsigned.Header()
	signHeader(t, sealed, sealer, f.cfg.Bor)
	return unsigned.WithSeal(sealed)
}

func (f *statelessImportFixture) witness(t *testing.T, block *types.Block) *stateless.Witness {
	t.Helper()

	w, err := stateless.NewWitness(block.Header(), f.chain)
	require.NoError(t, err)
	tr, err := trie.New(trie.StateTrieID(f.genesis.Root), f.chain.TrieDB())
	require.NoError(t, err)
	it, err := tr.NodeIterator(nil)
	require.NoError(t, err)
	nodes := map[string][]byte{}
	for it.Next(true) {
		if it.Hash() != (common.Hash{}) {
			nodes[string(it.Path())] = common.CopyBytes(it.NodeBlob())
		}
	}
	require.NoError(t, it.Error())
	w.AddState(nodes)
	return w
}

func TestInsertChainRejectsBlobTxWithoutBlobSchedule(t *testing.T) {
	t.Parallel()

	f := newStatelessImportFixture(t)
	block := f.block(t, f.signerKey, f.tx(t, true), true)

	var err error
	require.NotPanics(t, func() { _, err = f.chain.InsertChain(types.Blocks{block}, false) })
	require.ErrorIs(t, err, core.ErrNoBlobBaseFee)
	require.Equal(t, f.genesis.Hash(), f.chain.CurrentBlock().Hash())
}

func TestInsertChainStatelessRejectsBeforeExecution(t *testing.T) {
	t.Parallel()

	outsider, err := crypto.GenerateKey()
	require.NoError(t, err)

	tests := []struct {
		name       string
		outsider   bool
		blobTx     bool
		blobFields bool
		check      func(t *testing.T, err error)
	}{
		{
			name: "unauthorized signer, blob tx", outsider: true, blobTx: true, blobFields: true,
			check: func(t *testing.T, err error) { var e *UnauthorizedSignerError; require.ErrorAs(t, err, &e) },
		},
		{
			name: "unauthorized signer, plain tx", outsider: true,
			check: func(t *testing.T, err error) { var e *UnauthorizedSignerError; require.ErrorAs(t, err, &e) },
		},
		{
			name: "authorized signer, blob tx without blob header fields", blobTx: true,
			check: func(t *testing.T, err error) { require.ErrorContains(t, err, "data blobs present in block body") },
		},
		{
			name: "authorized signer, blob tx with blob header fields", blobTx: true, blobFields: true,
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, core.ErrNoBlobBaseFee) },
		},
	}
	for _, tt := range tests {
		for _, parallel := range []bool{false, true} {
			name := tt.name + "/sequential"
			if parallel {
				name = tt.name + "/parallel"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				f := newStatelessImportFixture(t)
				if parallel {
					f.chain.ParallelStatelessImportEnable()
				}
				sealer := f.signerKey
				if tt.outsider {
					sealer = outsider
				}
				block := f.block(t, sealer, f.tx(t, tt.blobTx), tt.blobFields)

				var err error
				require.NotPanics(t, func() {
					_, err = f.chain.InsertChainStateless(types.Blocks{block}, []*stateless.Witness{f.witness(t, block)})
				})
				require.Error(t, err)
				tt.check(t, err)
				require.Equal(t, f.genesis.Hash(), f.chain.CurrentBlock().Hash())
			})
		}
	}
}
