// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"go.opentelemetry.io/otel"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/tracing"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

const (
	// resultQueueSize is the size of channel listening to sealing result.
	resultQueueSize = 10

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 4096

	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 10

	// resubmitAdjustChanSize is the size of resubmitting interval adjustment channel.
	resubmitAdjustChanSize = 10

	// minRecommitInterval is the minimal time interval to recreate the sealing block with
	// any newly arrived transactions.
	minRecommitInterval = 1 * time.Second

	// intervalAdjustRatio is the impact a single interval adjustment has on sealing work
	// resubmitting interval.
	intervalAdjustRatio = 0.1

	// intervalAdjustBias is applied during the new resubmit interval calculation in favor of
	// increasing upper limit or decreasing lower limit so that the limit can be reachable.
	intervalAdjustBias = 200 * 1000.0 * 1000.0

	// staleThreshold is the maximum depth of the acceptable stale block.
	// In PoW chains (like pre-merge Ethereum), this is set to 7 because orphaned blocks
	// can still be included as "uncle blocks" up to 6-7 blocks deep, earning partial rewards.
	// In Bor's PoS consensus, validators take turns producing blocks deterministically,
	// so there are no competing miners and no uncle block concept. Any non-canonical block
	// is immediately stale and can be discarded, hence staleThreshold is set to 0.
	staleThreshold = 0

	// interruptBuffer is the buffer time to give some buffer for state root computation
	interruptBuffer = 100 * time.Millisecond

	// prefetchChanBufSize is the default buffer for the unified prefetcher's tx
	// stream channel. ≈ one full block's worth of 21k-gas txs at the 100M-gas
	// block limit. Sized to absorb the idle provider's per-loop burst (bounded
	// by gas budget) without ever blocking a sender; workers drain far faster
	// than the idle heap can fill in practice. Channel memory is ~33 KB.
	prefetchChanBufSize = 4096

	// prefetchIdleLoopInterval is the minimum cadence between idle-phase pool
	// snapshots in runIdleTxProvider.
	prefetchIdleLoopInterval = 100 * time.Millisecond

	// prefetchDefaultGasLimitPercent is the default percentage of header
	// gas limit used as the idle-phase prefetch budget when unconfigured.
	prefetchDefaultGasLimitPercent = 100

	// prefetchMaxGasLimitPercent caps the idle-phase prefetch gas budget to
	// guard against misconfiguration DoS.
	prefetchMaxGasLimitPercent = 150
)

var (
	errBlockInterruptedByNewHead  = errors.New("new head arrived while building block")
	errBlockInterruptedByRecommit = errors.New("recommit interrupt while building block")
	errBlockInterruptedByTimeout  = errors.New("timeout while building block")

	// metrics gauge to track total and empty blocks sealed by a miner
	sealedBlocksCounter      = metrics.NewRegisteredCounter("worker/sealedBlocks", nil)
	sealedEmptyBlocksCounter = metrics.NewRegisteredCounter("worker/sealedEmptyBlocks", nil)
	txCommitInterruptCounter = metrics.NewRegisteredCounter("worker/txCommitInterrupt", nil)

	// txHeapInitTimer measures time taken to initialise a heap of pending transactions from pool
	txHeapInitTimer = metrics.NewRegisteredTimer("worker/txheapinit", nil)
	// prepareWorkTimer measures time taken to prepare environment for block building which
	// includes the `bor.Prepare` call as well.
	prepareWorkTimer = metrics.NewRegisteredTimer("worker/prepareWork", nil)
	// pendingTimer measures time taken to fetch transactions from pool in the actual block
	// building cycle (excluding the calls made by prefetcher).
	pendingTimer = metrics.NewRegisteredTimer("worker/pending", nil)
	// commitTransactionsTimer measures time taken to execute transactions
	commitTransactionsTimer = metrics.NewRegisteredTimer("worker/commitTransactions", nil)
	// txApplyDurationTimer captures per-transaction apply latency during block building.
	// Uses a larger reservoir to preserve tail visibility on high-throughput blocks.
	txApplyDurationTimer = newRegisteredCustomTimer("worker/txApplyDuration", 8192)
	// Split variants of txApplyDuration by prefetch status. The aggregate timer
	// above stays to preserve existing Grafana dashboards.
	txApplyDurationPrefetchedTimer    = newRegisteredCustomTimer("worker/txApplyDuration/prefetched", 8192)
	txApplyDurationNotPrefetchedTimer = newRegisteredCustomTimer("worker/txApplyDuration/notPrefetched", 8192)
	// finalizeAndAssembleTimer measures time taken to finalize and assemble the block (state root calculation)
	finalizeAndAssembleTimer = metrics.NewRegisteredTimer("worker/finalizeAndAssemble", nil)
	// intermediateRootTimer measures time taken to calculate intermediate root
	intermediateRootTimer = metrics.NewRegisteredTimer("worker/intermediateRoot", nil)
	// commitTimer measures total time for complete block building (tx execution + finalization + state root)
	commitTimer = metrics.NewRegisteredTimer("worker/commit", nil)
	// writeBlockAndSetHeadTimer measures total time for WriteBlockAndSetHead in the seal result loop.
	// This covers the entire gap between block sealing and event posting: witness encoding, batch write,
	// state commit, and (in hashdb mode) trie GC. Spikes here directly delay block broadcasting.
	writeBlockAndSetHeadTimer = metrics.NewRegisteredTimer("worker/writeBlockAndSetHead", nil)

	// Cache hit/miss metrics for block production (miner path)
	// These are the same meters used by the import path in blockchain.go
	accountCacheHitMeter  = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/process/hit", nil)
	accountCacheMissMeter = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/process/miss", nil)
	storageCacheHitMeter  = metrics.NewRegisteredMeter("worker/chain/storage/reads/cache/process/hit", nil)
	storageCacheMissMeter = metrics.NewRegisteredMeter("worker/chain/storage/reads/cache/process/miss", nil)

	accountCacheHitPrefetchMeter  = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/prefetch/hit", nil)
	accountCacheMissPrefetchMeter = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/prefetch/miss", nil)
	storageCacheHitPrefetchMeter  = metrics.NewRegisteredMeter("worker/chain/storage/reads/cache/prefetch/hit", nil)
	storageCacheMissPrefetchMeter = metrics.NewRegisteredMeter("worker/chain/storage/reads/cache/prefetch/miss", nil)

	// Additional prefetch attribution metrics
	accountHitFromPrefetchMeter       = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/process/hit_from_prefetch", nil)
	storageHitFromPrefetchMeter       = metrics.NewRegisteredMeter("worker/chain/storage/reads/cache/process/hit_from_prefetch", nil)
	accountInsertPrefetchMeter        = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/prefetch/insert", nil)
	storageInsertPrefetchMeter        = metrics.NewRegisteredMeter("worker/chain/storage/reads/cache/prefetch/insert", nil)
	accountHitFromPrefetchUniqueMeter = metrics.NewRegisteredMeter("worker/chain/account/reads/cache/process/prefetch_used_unique", nil)
	prefetchPanicMeter                = metrics.NewRegisteredMeter("worker/prefetch/panic", nil)

	// prefetchMissRateHistogram tracks percentage of block transactions that were NOT prefetched.
	// Values range 0-100. High percentiles indicate prefetch degradation.
	prefetchMissRateHistogram = metrics.NewRegisteredHistogram(
		"worker/prefetch/miss_rate_percent",
		nil,
		metrics.NewExpDecaySample(1028, 0.015),
	)

	// prefetchBuilderAddedHistogram tracks the percentage of block transactions that were
	// prefetched exclusively during the builder phase (i.e. would have been a miss if the
	// idle phase had been the only prefetch source). Directly measures the payoff of the
	// builder-phase prefetch over the aggregate miss rate above.
	prefetchBuilderAddedHistogram = metrics.NewRegisteredHistogram(
		"worker/prefetch/builder_added_percent",
		nil,
		metrics.NewExpDecaySample(1028, 0.015),
	)

	// Trie read/hash/execution metrics for block production (mirroring blockchain.go import path).
	// Namespaced under worker/chain/ to distinguish from import-path chain/ metrics.
	workerAccountReadTimer         = metrics.NewRegisteredResettingTimer("worker/chain/account/reads", nil)
	workerStorageReadTimer         = metrics.NewRegisteredResettingTimer("worker/chain/storage/reads", nil)
	workerSnapshotAccountReadTimer = metrics.NewRegisteredResettingTimer("worker/chain/snapshot/account/reads", nil)
	workerSnapshotStorageReadTimer = metrics.NewRegisteredResettingTimer("worker/chain/snapshot/storage/reads", nil)
	workerAccountUpdateTimer       = metrics.NewRegisteredResettingTimer("worker/chain/account/updates", nil)
	workerStorageUpdateTimer       = metrics.NewRegisteredResettingTimer("worker/chain/storage/updates", nil)
	workerAccountHashTimer         = metrics.NewRegisteredResettingTimer("worker/chain/account/hashes", nil)
	workerStorageHashTimer         = metrics.NewRegisteredTimer("worker/chain/storage/hashes", nil)
	workerBorConsensusTimer        = metrics.NewRegisteredTimer("worker/chain/bor/consensus", nil)
	workerBlockExecutionTimer      = metrics.NewRegisteredTimer("worker/chain/execution", nil)
	workerMgaspsTimer              = metrics.NewRegisteredResettingTimer("worker/chain/mgasps", nil)

	// Trie commit metrics for block production (populated after WriteBlockAndSetHead → CommitWithUpdate).
	workerAccountCommitTimer     = metrics.NewRegisteredResettingTimer("worker/chain/account/commits", nil)
	workerStorageCommitTimer     = metrics.NewRegisteredResettingTimer("worker/chain/storage/commits", nil)
	workerSnapshotCommitTimer    = metrics.NewRegisteredResettingTimer("worker/chain/snapshot/commits", nil)
	workerTriedbCommitTimer      = metrics.NewRegisteredResettingTimer("worker/chain/triedb/commits", nil)
	workerWitnessCollectionTimer = metrics.NewRegisteredTimer("worker/chain/witness/collection", nil)
)

// firstNonZeroTime returns a if non-zero, otherwise b.
func firstNonZeroTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

// productionStartFrom extracts the productionStart time from genParams.
// Returns zero time if genParams is nil, matching the guarded access pattern
// already used elsewhere in commit() (e.g. the genParams != nil check at the
// prefetch coverage block).
func productionStartFrom(genParams *generateParams) time.Time {
	if genParams == nil {
		return time.Time{}
	}
	return genParams.productionStart
}

func newRegisteredCustomTimer(name string, reservoirSize int) *metrics.Timer {
	return metrics.GetOrRegister(name, func() interface{} {
		return metrics.NewCustomTimer(
			metrics.NewHistogram(metrics.NewExpDecaySample(reservoirSize, 0.015)),
			metrics.NewMeter(),
		)
	}).(*metrics.Timer)
}

// environment is the worker's current environment and holds all
// information of the sealing block generation.
type environment struct {
	signer   types.Signer
	state    *state.StateDB // apply state changes here
	tcount   int            // tx count in cycle
	size     uint64         // size of the block we are building
	gasPool  *core.GasPool  // available gas used to pack transactions
	coinbase common.Address
	evm      *vm.EVM

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt
	sidecars []*types.BlobTxSidecar
	blobs    int

	mvReadMapList []map[blockstm.Key]blockstm.ReadDescriptor
	witness       *stateless.Witness

	// Readers with stats tracking for metrics reporting
	prefetchReader state.ReaderWithStats
	processReader  state.ReaderWithStats

	// prefetchedTxHashes is the live set written by the prefetch stream's
	// onSuccess callback. Read at tx-commit time to annotate slow-tx logs and
	// split the apply-duration histogram by prefetch status. May be nil.
	prefetchedTxHashes *sync.Map

	// Observability for pre block building phase
	makeEnvDuration    time.Duration
	makeHeaderDuration time.Duration // primarily includes call to bor.Prepare
	// Track time taken to fetch pending transactions during block building
	pendingDuration time.Duration
}

// copy creates a deep copy of environment.
func (env *environment) copy() *environment {
	cpy := &environment{
		signer:             env.signer,
		state:              env.state.Copy(),
		tcount:             env.tcount,
		coinbase:           env.coinbase,
		header:             types.CopyHeader(env.header),
		receipts:           copyReceipts(env.receipts),
		mvReadMapList:      env.mvReadMapList,
		prefetchReader:     env.prefetchReader,
		processReader:      env.processReader,
		prefetchedTxHashes: env.prefetchedTxHashes,
		makeEnvDuration:    env.makeEnvDuration,
		makeHeaderDuration: env.makeHeaderDuration,
		pendingDuration:    env.pendingDuration,
	}

	if env.gasPool != nil {
		gasPool := *env.gasPool
		cpy.gasPool = &gasPool
	}
	cpy.txs = make([]*types.Transaction, len(env.txs))
	copy(cpy.txs, env.txs)

	cpy.sidecars = make([]*types.BlobTxSidecar, len(env.sidecars))
	copy(cpy.sidecars, env.sidecars)

	return cpy
}

// discard terminates the background prefetcher go-routine. It should
// always be called for all created environment instances otherwise
// the go-routine leak can happen.
func (env *environment) discard() {
	if env.state == nil {
		return
	}

	env.state.StopPrefetcher()
}

// task contains all information for consensus engine sealing and result submitting.
type task struct {
	receipts             []*types.Receipt
	state                *state.StateDB
	block                *types.Block
	createdAt            time.Time
	productionElapsed    time.Duration // elapsed from after prepareWork to task submission (excludes sealing wait); used for workerMgaspsTimer and workerBlockExecutionTimer
	intermediateRootTime time.Duration // time spent in IntermediateRoot inside FinalizeAndAssemble; subtracted when computing workerBlockExecutionTimer
}

// txFits reports whether the transaction fits into the block size limit.
func (env *environment) txFitsSize(tx *types.Transaction) bool {
	return env.size+tx.Size() < params.MaxBlockSize-maxBlockSizeBufferZone
}

const (
	commitInterruptNone int32 = iota
	commitInterruptNewHead
	commitInterruptResubmit
	commitInterruptTimeout
)

// Block size is capped by the protocol at params.MaxBlockSize. When producing blocks, we
// try to say below the size including a buffer zone, this is to avoid going over the
// maximum size with auxiliary data added into the block.
const maxBlockSizeBufferZone = 1_000_000

// newWorkReq represents a request for new sealing work submitting with relative interrupt notifier.
type newWorkReq struct {
	interrupt *atomic.Int32
	noempty   bool
	timestamp int64
}

// newPayloadResult is the result of payload generation.
type newPayloadResult struct {
	err      error
	block    *types.Block
	fees     *big.Int               // total block fees
	sidecars []*types.BlobTxSidecar // collected blobs of blob transactions
	stateDB  *state.StateDB         // StateDB after executing the transactions
	receipts []*types.Receipt       // Receipts collected during construction
	requests [][]byte               // Consensus layer requests collected during block construction
	witness  *stateless.Witness     // Witness is an optional stateless proof

}

// getWorkReq represents a request for getting a new sealing work with provided parameters.
type getWorkReq struct {
	//nolint:containedctx
	ctx    context.Context
	params *generateParams
	result chan *newPayloadResult // non-blocking channel
}

// intervalAdjust represents a resubmitting interval adjustment.
type intervalAdjust struct {
	ratio float64
	inc   bool
}

// worker is the main object which takes care of submitting new work to consensus engine
// and gathering the sealing result.
type worker struct {
	config      *Config
	chainConfig *params.ChainConfig
	engine      consensus.Engine
	eth         Backend
	chain       *core.BlockChain

	prio []common.Address // A list of senders to prioritize

	// Feeds
	pendingLogsFeed event.Feed

	// Subscriptions
	mux          *event.TypeMux
	txsCh        chan core.NewTxsEvent
	txsSub       event.Subscription
	chainHeadCh  chan core.ChainHeadEvent
	chainHeadSub event.Subscription

	// Channels
	newWorkCh          chan *newWorkReq
	getWorkCh          chan *getWorkReq
	taskCh             chan *task
	resultCh           chan *consensus.NewSealedBlockEvent
	startCh            chan struct{}
	exitCh             chan struct{}
	resubmitIntervalCh chan time.Duration
	resubmitAdjustCh   chan *intervalAdjust

	wg         sync.WaitGroup
	prefetchWg sync.WaitGroup

	currentMu sync.RWMutex // The lock used to protect the current environment
	current   *environment // An environment for current running cycle.

	mu       sync.RWMutex // The lock used to protect the coinbase and extra fields
	coinbase common.Address
	extra    []byte
	tip      *uint256.Int // Minimum tip needed for non-local transaction to include them

	pendingMu    sync.RWMutex
	pendingTasks map[common.Hash]*task

	// Block number which is currently being worked on (0 = none).
	// Used to prevent duplicate work.
	pendingWorkBlock atomic.Uint64

	snapshotMu       sync.RWMutex // The lock used to protect the snapshots below
	snapshotBlock    *types.Block
	snapshotReceipts types.Receipts
	snapshotState    *state.StateDB

	// atomic status counters
	running atomic.Bool  // The indicator whether the consensus engine is running or not.
	newTxs  atomic.Int32 // New arrival transaction count since last sealing work submitting.
	syncing atomic.Bool  // The indicator whether the node is still syncing.

	// newpayloadTimeout is the maximum timeout allowance for creating payload.
	// The default value is 2 seconds but node operator can set it to arbitrary
	// large value. A large timeout allowance may cause Geth to fail creating
	// a non-empty payload within the specified time and eventually miss the slot
	// in case there are some computation expensive transactions in txpool.
	newpayloadTimeout time.Duration

	// recommit is the time interval to re-create sealing work or to re-build
	// payload in proof-of-stake stage.
	recommit time.Duration

	// External functions
	isLocalBlock func(header *types.Header) bool // Function used to determine whether the specified block is mined by local miner.

	// Test hooks
	newTaskHook  func(*task)                        // Method to call upon receiving a new sealing task.
	skipSealHook func(*task) bool                   // Method to decide whether skipping the sealing.
	fullTaskHook func()                             // Method to call before pushing the full sealing task.
	resubmitHook func(time.Duration, time.Duration) // Method to call upon updating resubmitting interval.

	// Interrupt commit to stop block building on time
	interruptCommitFlag    bool        // Denotes whether interrupt commit is enabled or not
	interruptBlockBuilding atomic.Bool // A toggle to denote whether to stop block building or not
	interruptFlagSetAt     atomic.Int64
	mockTxDelay            uint // A mock delay for transaction execution, only used in tests

	blockTime     time.Duration     // The block time defined by the miner. Needs to be larger or equal to the consensus block time. If not set (default = 0), the miner will use the consensus block [...]
	slowTxTracker *slowTxTopTracker // Tracks top slow transactions for periodic reporting.

	// noempty is the flag used to control whether the feature of pre-seal empty
	// block is enabled. The default value is false(pre-seal is enabled by default).
	// But in some special scenario the consensus engine will seal blocks instantaneously,
	// in this case this feature will add all empty blocks into canonical chain
	// non-stop and no real transaction will be included.
	noempty atomic.Bool

	makeWitness bool
}

//nolint:staticcheck
func newWorker(config *Config, chainConfig *params.ChainConfig, engine consensus.Engine, eth Backend, mux *event.TypeMux, isLocalBlock func(header *types.Header) bool, init bool, makeWitness bool[...]