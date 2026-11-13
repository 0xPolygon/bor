package miner

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

// TestInlineSealHonoursWhitelistedMilestone seals the speculative block of a
// pipeline session after a milestone has been whitelisted at its height.
func TestInlineSealHonoursWhitelistedMilestone(t *testing.T) {
	tests := []struct {
		name          string
		milestone     bool
		wantAnnounced bool
	}{
		{name: "block without milestone conflict is announced and written", wantAnnounced: true},
		{name: "block conflicting with milestone is neither announced nor written", milestone: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, backend, checker := newCheckedPipelineWorker(t)
			session := newSpecSession(w, newPipelineRequest(t, w, backend))
			require.True(t, session.setupInitial())
			<-session.initialFillDone
			require.True(t, session.waitForSRCAndSealBlockN())
			head := backend.chain.CurrentBlock()

			finalHeader, flatDiff, syncData, ok := session.finalizeCurrent()
			require.True(t, ok)
			next, ok := session.prepareNextIteration(finalHeader, flatDiff, syncData)
			require.True(t, ok)
			if tt.milestone {
				checker.ProcessMilestone(finalHeader.Number.Uint64(), common.HexToHash("0x01"))
			}

			announced := collectMinedBlocks(t, w.mux)
			sealed, exitEarly, ok := session.sealCurrentAndAdvance(finalHeader, syncData, next)

			if tt.wantAnnounced {
				require.True(t, ok)
				require.False(t, exitEarly)
				<-session.prevDBWriteDone
				require.Equal(t, sealed.Hash(), backend.chain.CurrentBlock().Hash())
				select {
				case got := <-announced:
					require.Equal(t, sealed.Hash(), got.Hash())
				case <-time.After(time.Second):
					t.Fatal("sealed block was not announced")
				}
				return
			}

			require.False(t, ok)
			require.True(t, exitEarly, "session retried on the head the refused block was built on")
			require.Nil(t, sealed)
			require.Nil(t, session.prevDBWriteDone)
			require.Equal(t, head.Hash(), backend.chain.CurrentBlock().Hash())
			require.Nil(t, backend.chain.GetBlockByNumber(finalHeader.Number.Uint64()))
			select {
			case got := <-announced:
				t.Fatalf("refused block %d was announced", got.NumberU64())
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func TestRefusedOnCurrentHead(t *testing.T) {
	w, backend := newPipelineWorkerFixture(t, nil)
	head := backend.chain.CurrentBlock().Hash()

	tests := []struct {
		name   string
		parent common.Hash
		err    error
		want   bool
	}{
		{name: "milestone conflict on head", parent: head, err: whitelist.ErrMismatch, want: true},
		{name: "wrapped milestone conflict on head", parent: head, err: errors.Join(errors.New("write"), whitelist.ErrMismatch), want: true},
		{name: "milestone conflict after head moved", parent: common.HexToHash("0x01"), err: whitelist.ErrMismatch},
		{name: "other error on head", parent: head, err: errors.New("seal failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1), ParentHash: tt.parent})
			require.Equal(t, tt.want, w.refusedOnCurrentHead(block, tt.err))
		})
	}
}

func newCheckedPipelineWorker(t *testing.T) (*worker, *testWorkerBackend, *whitelist.Service) {
	t.Helper()

	chainConfig := *params.BorUnittestChainConfig
	borConfig := *chainConfig.Bor
	borConfig.RioBlock = big.NewInt(0)
	chainConfig.Bor = &borConfig

	engine, ctrl := getFakeBorFromConfig(t, &chainConfig)
	t.Cleanup(ctrl.Finish)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })

	db := rawdb.NewMemoryDatabase()
	checker := whitelist.NewService(db, false, 0)
	chainCfg := core.DefaultConfig()
	chainCfg.Checker = checker
	backend := newTestWorkerBackendWithConfig(t, &chainConfig, engine, db, chainCfg)
	checker.SetBlockchain(backend.chain)
	backend.txPool.Add(pendingTxs, false)

	w := newWorker(DefaultTestConfig(), &chainConfig, engine, backend, new(event.TypeMux), nil, false, false)
	w.setEtherbase(testBankAddress)
	t.Cleanup(w.close)
	return w, backend, checker
}

func collectMinedBlocks(t *testing.T, mux *event.TypeMux) <-chan *types.Block {
	t.Helper()

	sub := mux.Subscribe(core.NewMinedBlockEvent{})
	t.Cleanup(sub.Unsubscribe)
	out := make(chan *types.Block, 4)
	go func() {
		for ev := range sub.Chan() {
			if mined, ok := ev.Data.(core.NewMinedBlockEvent); ok {
				out <- mined.Block
			}
		}
	}()
	return out
}
