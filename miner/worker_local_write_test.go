package miner

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

// TestResultLoopHonoursWhitelistedMilestone delivers a sealed child of one of
// two equal-TD siblings to resultLoop after a milestone has been whitelisted on
// the canonical (higher-hash) sibling.
func TestResultLoopHonoursWhitelistedMilestone(t *testing.T) {
	tests := []struct {
		name            string
		buildOnStale    bool
		headOnStale     bool
		wantCanonical   bool
		wantRebuildWork bool
	}{
		{name: "child of milestone block becomes head", wantCanonical: true},
		{name: "child of sibling conflicting with milestone is refused", buildOnStale: true, wantRebuildWork: true},
		{name: "child of head conflicting with milestone is refused without rebuild", buildOnStale: true, headOnStale: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainConfig := localWriteChainConfig()
			engine, ctrl := getFakeBorFromConfig(t, chainConfig)
			defer engine.Close()
			defer ctrl.Finish()

			now := uint64(time.Now().Unix())
			genesis := &core.Genesis{
				Config:    chainConfig,
				Timestamp: now,
				GasLimit:  params.GenesisGasLimit,
				BaseFee:   big.NewInt(params.InitialBaseFee),
			}
			stale, canonical := makeEqualTDSiblings(t, chainConfig, genesis.ToBlock(), now+1)

			chain, checker := newLocalWriteChain(t, genesis, engine)
			defer chain.Stop()
			_, err := chain.InsertChain(types.Blocks{stale}, false)
			require.NoError(t, err)
			wantHead := stale
			if !tt.headOnStale {
				_, err = chain.InsertChain(types.Blocks{canonical}, false)
				require.NoError(t, err)
				wantHead = canonical
			}
			require.Equal(t, wantHead.Hash(), chain.CurrentBlock().Hash())
			checker.ProcessMilestone(canonical.NumberU64(), canonical.Hash())

			parent := canonical
			if tt.buildOnStale {
				parent = stale
			}
			child := newSignedLocalWriteBlock(t, chainConfig, parent, now+1+chainConfig.Bor.CalculatePeriod(2), []byte{0xff})

			seq := &recordingSequencer{verdict: SealConfirmed}
			w, sealHash := startResultLoop(t, chain, engine, chainConfig, seq, child)

			headEvents := make(chan core.ChainHeadEvent, 1)
			headSub := chain.SubscribeChainHeadEvent(headEvents)
			defer headSub.Unsubscribe()

			w.resultCh <- &consensus.NewSealedBlockEvent{Block: child}

			if tt.wantCanonical {
				select {
				case ev := <-headEvents:
					require.Equal(t, child.Hash(), ev.Header.Hash())
				case <-time.After(3 * time.Second):
					t.Fatal("sealed block was not made canonical")
				}
				require.Equal(t, child.Hash(), chain.CurrentBlock().Hash())
				require.Equal(t, []uint64{child.NumberU64()}, sealedNumbers(seq))
				return
			}

			wait := 500 * time.Millisecond
			if tt.wantRebuildWork {
				wait = 3 * time.Second
			}
			select {
			case <-w.newWorkCh:
				require.True(t, tt.wantRebuildWork, "worker rebuilt on a head the refused block was built on")
			case ev := <-headEvents:
				t.Fatalf("sealed block on conflicting parent became head %s", ev.Header.Hash())
			case <-time.After(wait):
				require.False(t, tt.wantRebuildWork, "worker did not rebuild after the refused write")
			}
			require.Equal(t, wantHead.Hash(), chain.CurrentBlock().Hash())
			require.Equal(t, wantHead.Hash(), chain.GetCanonicalHash(wantHead.NumberU64()))
			require.False(t, chain.HasBlock(child.Hash(), child.NumberU64()))
			require.Empty(t, sealedNumbers(seq), "refused block's seal was published")
			w.pendingMu.RLock()
			_, pending := w.pendingTasks[sealHash]
			w.pendingMu.RUnlock()
			require.False(t, pending)
			if tt.wantRebuildWork {
				require.Equal(t, chain.CurrentBlock().Number.Uint64()+1, w.pendingWorkBlock.Load())
			}
		})
	}
}

func sealedNumbers(r *recordingSequencer) []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.seals...)
}

func startResultLoop(t *testing.T, chain *core.BlockChain, engine consensus.Engine, chainConfig *params.ChainConfig,
	seq BlockSequencer, child *types.Block) (*worker, common.Hash) {
	t.Helper()

	statedb, err := chain.StateAt(chain.Genesis().Root())
	require.NoError(t, err)

	w := &worker{
		chain:        chain,
		chainConfig:  chainConfig,
		sequencer:    seq,
		engine:       engine,
		mux:          new(event.TypeMux),
		resultCh:     make(chan *consensus.NewSealedBlockEvent, 1),
		newWorkCh:    make(chan *newWorkReq, 1),
		exitCh:       make(chan struct{}),
		pendingTasks: make(map[common.Hash]*task),
	}
	sealHash := engine.SealHash(child.Header())
	w.pendingTasks[sealHash] = &task{block: child, state: statedb, createdAt: time.Now()}

	w.wg.Add(1)
	go w.resultLoop()
	t.Cleanup(func() {
		close(w.exitCh)
		w.wg.Wait()
	})
	return w, sealHash
}

func localWriteChainConfig() *params.ChainConfig {
	config := *params.BorUnittestChainConfig
	borConfig := *config.Bor
	config.Bor = &borConfig
	config.ShanghaiBlock = big.NewInt(1)
	config.CancunBlock = big.NewInt(1)
	config.Bor.RioBlock = big.NewInt(1)
	config.Bor.GiuglianoBlock = big.NewInt(1)
	config.Bor.AustinBlock = big.NewInt(1)
	return &config
}

func newLocalWriteChain(t *testing.T, genesis *core.Genesis, engine consensus.Engine) (*core.BlockChain, *whitelist.Service) {
	t.Helper()

	db := rawdb.NewMemoryDatabase()
	checker := whitelist.NewService(db, false, 0)
	config := core.DefaultConfig()
	config.Checker = checker
	chain, err := core.NewBlockChain(db, genesis, engine, config)
	require.NoError(t, err)
	checker.SetBlockchain(chain)
	return chain, checker
}

// makeEqualTDSiblings returns two signed blocks on the same parent, ordered so
// that the second has the larger hash and wins the equal-TD tie-break.
func makeEqualTDSiblings(t *testing.T, config *params.ChainConfig, parent *types.Block, timestamp uint64) (*types.Block, *types.Block) {
	t.Helper()

	first := newSignedLocalWriteBlock(t, config, parent, timestamp, []byte{0})
	for marker := uint16(1); marker != 0; marker++ {
		candidate := newSignedLocalWriteBlock(t, config, parent, timestamp, []byte{byte(marker), byte(marker >> 8)})
		if bytes.Compare(candidate.Hash().Bytes(), first.Hash().Bytes()) > 0 {
			return first, candidate
		}
	}
	t.Fatal("no higher-hash sibling found")
	return nil, nil
}

func newSignedLocalWriteBlock(t *testing.T, config *params.ChainConfig, parent *types.Block, timestamp uint64, vanity []byte) *types.Block {
	t.Helper()

	number := new(big.Int).Add(parent.Number(), common.Big1)
	gasTarget := eip1559.CalcGasTarget(config, parent.Header())
	baseFeeChangeDenom := params.BaseFeeChangeDenominator(config.Bor, parent.Number())
	extraFields, err := types.EncodeBlockExtraData(config, number, nil, &gasTarget, &baseFeeChangeDenom)
	require.NoError(t, err)
	extra := make([]byte, types.ExtraVanityLength+len(extraFields)+types.ExtraSealLength)
	copy(extra, vanity)
	copy(extra[types.ExtraVanityLength:], extraFields)

	header := &types.Header{
		ParentHash: parent.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Root:       parent.Root(),
		Difficulty: common.Big1,
		Number:     number,
		GasLimit:   parent.GasLimit(),
		Time:       timestamp,
		Extra:      extra,
		BaseFee:    eip1559.CalcBaseFee(config, parent.Header()),
	}
	block := types.NewBlock(header, &types.Body{}, nil, trie.NewStackTrie(nil))
	header = block.Header()
	require.NoError(t, bor.Sign(func(_ accounts.Account, _ string, data []byte) ([]byte, error) {
		return crypto.Sign(crypto.Keccak256(data), testBankKey)
	}, TestBankAddress, header, config.Bor))
	return block.WithSeal(header)
}
