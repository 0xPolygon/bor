//go:build integration

package bor

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	borSpan "github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
)

type validationShutdownFixture struct {
	stack          *node.Node
	chain          *core.BlockChain
	engine         *bor.Bor
	heimdallClient bor.IHeimdallClient
	parent         *types.Block
	block          *types.Block
	output         *synchronizedBuffer
	closed         bool
}

func newValidationShutdownFixture(t *testing.T) *validationShutdownFixture {
	output := &synchronizedBuffer{}
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(output, log.LevelInfo, true)))

	genesis := InitGenesis(t, keys, "./testdata/genesis.json", sprintSize)
	genesis.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": 128}
	span0 := createMockSpan(addr, genesis.Config.ChainID.String())
	span1 := loadSpanFromFile(t)
	ctrl := gomock.NewController(t)
	heimdallClient := createMockHeimdall(ctrl, &span0, span1)
	heimdallClient.EXPECT().StateSyncEvents(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*clerk.EventRecordWithTime{getSampleEventRecord(t)}, nil).AnyTimes()

	stack, ethereum, err := initShutdownTestNode(t, genesis, heimdallClient)
	require.NoError(t, err)
	f := &validationShutdownFixture{stack: stack, chain: ethereum.BlockChain(), engine: ethereum.Engine().(*bor.Bor), heimdallClient: heimdallClient, output: output}
	t.Cleanup(func() {
		if !f.closed {
			_ = stack.Close()
		}
		log.SetDefault(previousLogger)
	})

	validators := borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet).Validators
	f.engine.SetSpanner(getMockedSpanner(t, validators))
	parent := genesis.ToBlock()
	signer := crypto.FromECDSA(key)
	for number := uint64(1); number < sprintSize; number++ {
		parent = buildNextBlock(t, f.engine, f.chain, parent, signer, genesis.Config.Bor, nil, validators, true, nil, nil)
		insertNewBlock(t, f.chain, parent)
	}
	f.parent = parent
	f.block = buildNextBlock(t, f.engine, f.chain, parent, signer, genesis.Config.Bor, nil, validators, true, nil, nil)
	return f
}

func reopenValidationDatabase(t *testing.T, stack *node.Node) ethdb.Database {
	kvdb, err := pebble.New(stack.ResolvePath("chaindata"), 16, 16, "", false)
	require.NoError(t, err)
	db, err := rawdb.Open(kvdb, rawdb.OpenOptions{
		Ancient: stack.ResolvePath("chaindata/ancient/chain"),
		Era:     stack.ResolvePath("chaindata/ancient/chain/era"),
	})
	require.NoError(t, err)
	return db
}

func TestValidationFailuresRemainBadBlocksAcrossShutdown(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *validationShutdownFixture) *types.Block
	}{
		{
			name: "invalid signature",
			mutate: func(t *testing.T, f *validationShutdownFixture) *types.Block {
				header := types.CopyHeader(f.block.Header())
				header.Extra = bytes.Clone(header.Extra)
				header.Extra[len(header.Extra)-1] ^= 0xff
				return f.block.WithSeal(header)
			},
		},
		{
			name: "state root mismatch",
			mutate: func(t *testing.T, f *validationShutdownFixture) *types.Block {
				header := types.CopyHeader(f.block.Header())
				header.Root = common.Hash{0x01}
				sign(t, header, crypto.FromECDSA(key), f.chain.Config().Bor)
				return f.block.WithSeal(header)
			},
		},
		{
			name: "receipt root mismatch",
			mutate: func(t *testing.T, f *validationShutdownFixture) *types.Block {
				header := types.CopyHeader(f.block.Header())
				header.ReceiptHash = common.Hash{0x02}
				sign(t, header, crypto.FromECDSA(key), f.chain.Config().Bor)
				return f.block.WithSeal(header)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newValidationShutdownFixture(t)
			invalid := tt.mutate(t, f)
			_, importErr := f.chain.InsertChain([]*types.Block{invalid}, false)
			require.Error(t, importErr)
			require.NotErrorIs(t, importErr, consensus.ErrEngineStopped)

			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- f.stack.Close() }()
			select {
			case err := <-shutdownDone:
				require.NoError(t, err)
				f.closed = true
			case <-time.After(15 * time.Second):
				t.Fatal("shutdown did not finish after invalid import")
			}
			require.Contains(t, f.output.String(), "BAD BLOCK")
			db := reopenValidationDatabase(t, f.stack)
			defer db.Close()
			require.NotNil(t, rawdb.ReadBadBlock(db, invalid.Hash()))
		})
	}
}

type shutdownCompletionHeimdall struct {
	bor.IHeimdallClient
	entered chan struct{}
	once    sync.Once
	records []*clerk.EventRecordWithTime
}

func (c *shutdownCompletionHeimdall) StateSyncEvents(ctx context.Context, _ uint64, _ int64) ([]*clerk.EventRecordWithTime, error) {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return c.records, nil
}

func TestRootValidationFailuresRemainBadBlocksAfterEngineCancellation(t *testing.T) {
	for _, name := range []string{"state", "receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newValidationShutdownFixture(t)
			header := types.CopyHeader(f.block.Header())
			if name == "state" {
				header.Root = common.Hash{0x01}
			} else {
				header.ReceiptHash = common.Hash{0x02}
			}
			sign(t, header, crypto.FromECDSA(key), f.chain.Config().Bor)
			invalid := f.block.WithSeal(header)
			gate := &shutdownCompletionHeimdall{
				IHeimdallClient: f.heimdallClient,
				entered:         make(chan struct{}),
				records:         []*clerk.EventRecordWithTime{getSampleEventRecord(t)},
			}
			f.engine.SetHeimdallClient(gate)
			defer f.engine.Close()

			importDone := make(chan error, 1)
			go func() {
				_, err := f.chain.InsertChain([]*types.Block{invalid}, false)
				importDone <- err
			}()
			select {
			case <-gate.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("import did not reach StateSyncEvents")
			}
			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- f.stack.Close() }()

			var importErr error
			select {
			case importErr = <-importDone:
			case <-time.After(15 * time.Second):
				t.Fatal("invalid import did not complete after engine cancellation")
			}
			require.Error(t, importErr)
			require.NotErrorIs(t, importErr, consensus.ErrEngineStopped)
			if name == "state" {
				require.ErrorContains(t, importErr, "invalid merkle root")
			} else {
				require.ErrorIs(t, importErr, core.ErrReceiptRootMismatch)
			}
			select {
			case err := <-shutdownDone:
				require.NoError(t, err)
				f.closed = true
			case <-time.After(15 * time.Second):
				t.Fatal("shutdown did not join invalid import")
			}
			require.Contains(t, f.output.String(), "BAD BLOCK")
			db := reopenValidationDatabase(t, f.stack)
			defer db.Close()
			require.NotNil(t, rawdb.ReadBadBlock(db, invalid.Hash()))
		})
	}
}
