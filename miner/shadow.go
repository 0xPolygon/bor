package miner

// LLS shadow-producer experiment support (see core/shadowtrace). Everything
// here is inert unless the binary is built with -tags lls_shadow AND the
// process runs with BOR_SHADOW_LLS=1 (shadowtrace.Enabled()).

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// errShadowNoSeal is the reason logged for every shadow block: shadow mode
// never hands a block to the consensus engine's Seal.
var errShadowNoSeal = errors.New("shadow mode: sealing disabled")

// shadowSnap is the state just before one builder apply.
type shadowSnap struct {
	stats    state.ReaderStats
	pfStats  state.PrefetchStats
	reads    time.Duration
	gasLeft  uint64
	gasUsed  uint64
	index    int
	hasStats bool
}

func shadowSnapshot(env *environment, gasPoolBefore uint64) shadowSnap {
	sn := shadowSnap{gasLeft: gasPoolBefore, index: env.tcount}
	sn.gasUsed = env.header.GasLimit - gasPoolBefore
	if env.state != nil {
		sn.reads = env.state.AccountReads + env.state.StorageReads
	}
	if env.processReader != nil {
		sn.stats = env.processReader.GetStats()
		sn.pfStats = env.processReader.GetPrefetchStats()
		sn.hasStats = true
	}

	return sn
}

// shadowApplied records one builder apply in the shadow trace, with the
// L1 (per-build reader cache) hit/miss delta and the StateDB read time of
// exactly this tx: the builder applies serially, so the process reader's
// counters move only for it.
func (w *worker) shadowApplied(env *environment, tx *types.Transaction, from common.Address,
	start time.Time, dur time.Duration, before shadowSnap, err error,
) {
	a := shadowtrace.ApplyInfo{
		Start: start, Dur: dur, Result: "error", Err: err,
		From: from, To: tx.To(), GasLimit: tx.Gas(),
		Index: before.index, GasBefore: before.gasUsed, GasLeft: before.gasLeft,
	}

	if data := tx.Data(); len(data) >= 4 {
		a.Selector = hexutil.Encode(data[:4])
	}

	if tip, e := tx.EffectiveGasTip(env.header.BaseFee); e == nil {
		f, _ := new(big.Float).Quo(new(big.Float).SetInt(tip), big.NewFloat(1e9)).Float64()
		a.TipGwei = f
	}

	if env.state != nil {
		a.ReadDur = env.state.AccountReads + env.state.StorageReads - before.reads
	}

	if before.hasStats && env.processReader != nil {
		after := env.processReader.GetStats()
		a.L1Hit = (after.AccountHit - before.stats.AccountHit) + (after.StorageHit - before.stats.StorageHit)
		a.L1Miss = (after.AccountMiss - before.stats.AccountMiss) + (after.StorageMiss - before.stats.StorageMiss)
		pf := env.processReader.GetPrefetchStats()
		a.HitFromPrefetch = (pf.AccountHitFromPrefetch - before.pfStats.AccountHitFromPrefetch) +
			(pf.StorageHitFromPrefetch - before.pfStats.StorageHitFromPrefetch)
	}

	if err == nil {
		if n := len(env.receipts); n > 0 {
			r := env.receipts[n-1]
			a.GasUsed = r.GasUsed
			a.Result = "ok"
			if r.Status == types.ReceiptStatusFailed {
				a.Result = "reverted"
			}
		}
	}

	_, a.Prefetched = env.prefetchedTxHashes.Load(tx.Hash())
	shadowtrace.Applied(env.header.Number.Uint64(), tx.Hash(), a)
}

// shadowFillReason says why the fill loop ended.
func shadowFillReason(err error, env *environment) string {
	if env != nil && env.gasPool != nil && env.gasPool.Gas() < params.TxGas {
		return "gas-full"
	}

	switch {
	case err == nil:
		return "done"
	case errors.Is(err, errBlockInterruptedByTimeout):
		return "timeout"
	case errors.Is(err, errBlockInterruptedByNewHead):
		return "newhead"
	case errors.Is(err, errBlockInterruptedByRecommit):
		return "recommit"
	default:
		return err.Error()
	}
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
