package miner

// LLS shadow-producer experiment support (see core/shadowtrace). Everything
// here is inert unless the binary is built with -tags lls_shadow AND the
// process runs with BOR_SHADOW_LLS=1 (shadowtrace.Enabled()).

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

// errShadowNoSeal is the reason logged for every shadow block: shadow mode
// never hands a block to the consensus engine's Seal.
var errShadowNoSeal = errors.New("shadow mode: sealing disabled")

// shadowApplied records one builder apply in the shadow trace, with the
// L1 (per-build reader cache) hit/miss delta of exactly this tx: the builder
// applies serially, so the process reader's counters move only for it.
func (w *worker) shadowApplied(env *environment, tx *types.Transaction, start time.Time,
	dur time.Duration, before state.ReaderStats, err error,
) {
	var (
		hit, miss int64
		gasUsed   uint64
		result    = "error"
	)

	if env.processReader != nil {
		after := env.processReader.GetStats()
		hit = (after.AccountHit - before.AccountHit) + (after.StorageHit - before.StorageHit)
		miss = (after.AccountMiss - before.AccountMiss) + (after.StorageMiss - before.StorageMiss)
	}

	if err == nil {
		if n := len(env.receipts); n > 0 {
			r := env.receipts[n-1]
			gasUsed = r.GasUsed
			result = "ok"
			if r.Status == types.ReceiptStatusFailed {
				result = "reverted"
			}
		}
	}

	_, prefetched := env.prefetchedTxHashes.Load(tx.Hash())
	shadowtrace.Applied(env.header.Number.Uint64(), tx.Hash(), start, dur, prefetched,
		gasUsed, hit, miss, result, err)
}

// shadowPassStart marks a fill pass of a production build.
func shadowPassStart(env *environment, genParams *generateParams) {
	if genParams != nil && genParams.production {
		shadowtrace.PassStart(env.header.Number.Uint64())
	}
}

// ShadowSequencer is a local, network-free BlockSequencer for the shadow
// experiment. It makes the worker run the real LLS continuous fill loop
// (RefreshInterval = poll) and accept every gate, without ever talking to a
// sequence store. Consensus still refuses to seal the block, because the
// shadow signer is not in the validator set.
type ShadowSequencer struct {
	Poll time.Duration
}

var _ BlockSequencer = (*ShadowSequencer)(nil)

func (s *ShadowSequencer) OpenBlock(uint64, uint64, common.Hash, uint64, *big.Int) {}
func (s *ShadowSequencer) PublishTx(*types.Transaction)                            {}
func (s *ShadowSequencer) SealBlock(*types.Block)                                  {}
func (s *ShadowSequencer) AdoptWindow(uint64, common.Hash) *AdoptedWindow          { return nil }
func (s *ShadowSequencer) AwaitSequenced(time.Duration, uint64, []*types.Transaction) bool {
	return true
}
func (s *ShadowSequencer) ResyncNeeded() bool                    { return false }
func (s *ShadowSequencer) ConfirmSeal(time.Duration) SealVerdict { return SealConfirmed }
func (s *ShadowSequencer) RefreshInterval() time.Duration        { return s.Poll }
