package core

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/params"
)

func TestLocalWriteHonoursWhitelistedMilestone(t *testing.T) {
	writers := map[string]func(*BlockChain, *types.Block, *state.StateDB) (WriteStatus, error){
		"standard": func(bc *BlockChain, block *types.Block, statedb *state.StateDB) (WriteStatus, error) {
			return bc.WriteBlockAndSetHead(block, nil, nil, statedb, false)
		},
		"pipelined": func(bc *BlockChain, block *types.Block, statedb *state.StateDB) (WriteStatus, error) {
			return bc.WriteBlockAndSetHeadPipelined(block, nil, nil, statedb, false, nil)
		},
	}

	tests := []struct {
		name            string
		milestoneOnSide bool
		wantErr         error
	}{
		{name: "parent matches milestone", milestoneOnSide: true},
		{name: "parent conflicts with milestone", milestoneOnSide: false, wantErr: whitelist.ErrMismatch},
	}

	for writerName, write := range writers {
		for _, tt := range tests {
			t.Run(writerName+"/"+tt.name, func(t *testing.T) {
				bc, checker, canon, side := newLocalWriteChain(t)

				milestone := canon[0]
				if tt.milestoneOnSide {
					milestone = side[0]
				}
				checker.ProcessMilestone(milestone.NumberU64(), milestone.Hash())

				statedb, err := bc.StateAt(bc.Genesis().Root())
				require.NoError(t, err)

				status, err := write(bc, side[1], statedb)
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
					require.Equal(t, NonStatTy, status)
					require.Equal(t, canon[0].Hash(), bc.CurrentBlock().Hash())
					require.Equal(t, canon[0].Hash(), bc.GetCanonicalHash(1))
					require.False(t, bc.HasBlock(side[1].Hash(), side[1].NumberU64()))
					return
				}
				require.NoError(t, err)
				require.Equal(t, CanonStatTy, status)
				require.Equal(t, side[1].Hash(), bc.CurrentBlock().Hash())
				require.Equal(t, side[0].Hash(), bc.GetCanonicalHash(1))
			})
		}
	}
}

// newLocalWriteChain returns a chain whose head is canon[0], with side[0]
// stored as a non-canonical sibling and side[1], its child, not yet written.
func newLocalWriteChain(t *testing.T) (*BlockChain, *whitelist.Service, []*types.Block, []*types.Block) {
	t.Helper()

	genesis := &Genesis{
		Config:  params.AllEthashProtocolChanges,
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	engine := ethash.NewFaker()
	_, canon, _ := GenerateChainWithGenesis(genesis, engine, 1, nil)
	_, side, _ := GenerateChainWithGenesis(genesis, engine, 2, func(_ int, b *BlockGen) {
		b.SetCoinbase(common.Address{0x01})
	})

	db := rawdb.NewMemoryDatabase()
	checker := whitelist.NewService(db, false, 0)
	config := DefaultConfig()
	config.Checker = checker
	bc, err := NewBlockChain(db, genesis, engine, config)
	require.NoError(t, err)
	t.Cleanup(bc.Stop)

	_, err = bc.InsertChain(canon, false)
	require.NoError(t, err)

	rawdb.WriteBlock(db, side[0])
	rawdb.WriteTd(db, side[0].Hash(), side[0].NumberU64(), new(big.Int).Add(bc.GetTd(genesis.ToBlock().Hash(), 0), side[0].Difficulty()))
	require.Equal(t, canon[0].Hash(), bc.CurrentBlock().Hash())

	return bc, checker, canon, side
}
