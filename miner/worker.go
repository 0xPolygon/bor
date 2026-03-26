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
	// commitTransactionsTimer measures time taken to execute transactions
	commitTransactionsTimer = metrics.NewRegisteredTimer("worker/commitTransactions", nil)
	// txApplyDurationTimer captures per-transaction apply latency during block building.
	// Uses a larger reservoir to preserve tail visibility on high-throughput blocks.
	txApplyDurationTimer = newRegisteredCustomTimer("worker/txApplyDuration", 8192)
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
}

// copy creates a deep copy of environment.
func (env *environment) copy() *environment {
	cpy := &environment{
		signer:         env.signer,
		state:          env.state.Copy(),
		tcount:         env.tcount,
		coinbase:       env.coinbase,
		header:         types.CopyHeader(env.header),
		receipts:       copyReceipts(env.receipts),
		mvReadMapList:  env.mvReadMapList,
		prefetchReader: env.prefetchReader,
		processReader:  env.processReader,
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
const maxBlockSizeBufferZone = 0

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

	wg sync.WaitGroup

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

	blockTime     time.Duration     // The block time defined by the miner. Needs to be larger or equal to the consensus block time. If not set (default = 0), the miner will use the consensus block time.
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
func newWorker(config *Config, chainConfig *params.ChainConfig, engine consensus.Engine, eth Backend, mux *event.TypeMux, isLocalBlock func(header *types.Header) bool, init bool, makeWitness bool) *worker {
	worker := &worker{
		config:              config,
		chainConfig:         chainConfig,
		engine:              engine,
		eth:                 eth,
		chain:               eth.BlockChain(),
		mux:                 mux,
		isLocalBlock:        isLocalBlock,
		coinbase:            config.Etherbase,
		extra:               config.ExtraData,
		tip:                 uint256.MustFromBig(config.GasPrice),
		pendingTasks:        make(map[common.Hash]*task),
		txsCh:               make(chan core.NewTxsEvent, txChanSize),
		chainHeadCh:         make(chan core.ChainHeadEvent, chainHeadChanSize),
		newWorkCh:           make(chan *newWorkReq),
		getWorkCh:           make(chan *getWorkReq),
		taskCh:              make(chan *task),
		resultCh:            make(chan *consensus.NewSealedBlockEvent, resultQueueSize),
		startCh:             make(chan struct{}, 1),
		exitCh:              make(chan struct{}),
		resubmitIntervalCh:  make(chan time.Duration),
		resubmitAdjustCh:    make(chan *intervalAdjust, resubmitAdjustChanSize),
		interruptCommitFlag: config.CommitInterruptFlag,
		blockTime:           config.BlockTime,
		slowTxTracker:       newSlowTxTopTracker(),
		makeWitness:         makeWitness,
	}
	worker.noempty.Store(true)
	// Subscribe for transaction insertion events (whether from network or resurrects)
	worker.txsSub = eth.TxPool().SubscribeTransactions(worker.txsCh, true)
	// Subscribe events for blockchain
	worker.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)

	if !worker.interruptCommitFlag {
		worker.noempty.Store(false)
	}

	// Sanitize recommit interval if the user-specified one is too short.
	recommit := worker.config.Recommit
	if recommit < minRecommitInterval {
		log.Warn("Sanitizing miner recommit interval", "provided", recommit, "updated", minRecommitInterval)
		recommit = minRecommitInterval
	}

	worker.recommit = recommit

	// Sanitize the timeout config for creating payload.
	newpayloadTimeout := worker.config.NewPayloadTimeout
	if newpayloadTimeout == 0 {
		log.Warn("Sanitizing new payload timeout to default", "provided", newpayloadTimeout, "updated", DefaultConfig.NewPayloadTimeout)
		newpayloadTimeout = DefaultConfig.NewPayloadTimeout
	}

	if newpayloadTimeout < time.Millisecond*100 {
		log.Warn("Low payload timeout may cause high amount of non-full blocks", "provided", newpayloadTimeout, "default", DefaultConfig.NewPayloadTimeout)
	}

	worker.newpayloadTimeout = newpayloadTimeout

	worker.wg.Add(4)

	go worker.mainLoop()
	go worker.newWorkLoop(recommit)
	go worker.resultLoop()
	go worker.taskLoop()

	// Submit first work to initialize pending state.
	if init {
		worker.startCh <- struct{}{}
	}

	return worker
}

// setMockTxDelay sets the delay field used for inducing delay in between
// transaction execution in tests.
func (w *worker) setMockTxDelay(mockTxDelay uint) {
	w.mockTxDelay = mockTxDelay
}

// setEtherbase sets the etherbase used to initialize the block coinbase field.
func (w *worker) setEtherbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

// etherbase retrieves the configured etherbase address.
func (w *worker) etherbase() common.Address {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.coinbase
}

func (w *worker) setGasCeil(ceil uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.config.GasCeil = ceil
}

// calculateDesiredGasLimit determines the target gas limit based on configuration.
// If dynamic gas limit is enabled, it adjusts based on the parent's base fee:
// - When base fee > target + buffer: target max gas limit (increase supply)
// - When base fee < target - buffer: target min gas limit (decrease supply)
// - When within buffer: maintain current gas limit (no change)
// If dynamic gas limit is disabled, returns the static GasCeil value.
func (w *worker) calculateDesiredGasLimit(parent *types.Header) uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	// If dynamic gas limit is not enabled, use the static GasCeil
	if !w.config.EnableDynamicGasLimit {
		return w.config.GasCeil
	}

	// Pre-London blocks don't have base fee, use static GasCeil
	if parent.BaseFee == nil {
		return w.config.GasCeil
	}

	parentBaseFee := parent.BaseFee.Uint64()
	targetBaseFee := w.config.TargetBaseFee
	buffer := w.config.BaseFeeBuffer

	// Calculate bounds
	upperBound := targetBaseFee + buffer
	var lowerBound uint64
	if buffer < targetBaseFee {
		lowerBound = targetBaseFee - buffer
	} else {
		lowerBound = 0 // Prevent underflow
	}

	// Determine desired gas limit based on base fee position
	if parentBaseFee > upperBound {
		// Base fee is too high, increase gas limit to max to reduce fee pressure
		return w.config.GasLimitMax
	} else if parentBaseFee < lowerBound {
		// Base fee is too low, decrease gas limit to min to increase fee pressure
		return w.config.GasLimitMin
	}

	// Within buffer zone, maintain current gas limit
	return parent.GasLimit
}

// setExtra sets the content used to initialize the block extra field.
func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extra = extra
}

// setGasTip sets the minimum miner tip needed to include a non-local transaction.
func (w *worker) setGasTip(tip *big.Int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tip = uint256.MustFromBig(tip)
}

// setPrio sets the list of addresses to prioritize for transaction inclusion.
func (w *worker) setPrio(prio []common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prio = prio
}

// getCurrent returns the current environment safely for testing.
func (w *worker) getCurrent() *environment {
	w.currentMu.RLock()
	defer w.currentMu.RUnlock()
	return w.current
}

// setRecommitInterval updates the interval for miner sealing work recommitting.
func (w *worker) setRecommitInterval(interval time.Duration) {
	select {
	case w.resubmitIntervalCh <- interval:
	case <-w.exitCh:
	}
}

// pending returns the pending state and corresponding block. The returned
// values can be nil in case the pending block is not initialized.
func (w *worker) pending() (*types.Block, types.Receipts, *state.StateDB) {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()

	if w.snapshotState == nil {
		return nil, nil, nil
	}

	return w.snapshotBlock, w.snapshotReceipts, w.snapshotState.Copy()
}

// pendingBlock returns pending block. The returned block can be nil in case the
// pending block is not initialized.
func (w *worker) pendingBlock() *types.Block {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()

	return w.snapshotBlock
}

// start sets the running status as 1 and triggers new work submitting.
func (w *worker) start() {
	w.running.Store(true)
	w.startCh <- struct{}{}
}

// stop sets the running status as 0.
func (w *worker) stop() {
	w.running.Store(false)
}

// IsRunning returns an indicator whether worker is running or not.
func (w *worker) IsRunning() bool {
	return w.running.Load()
}

// close terminates all background threads maintained by the worker.
// Note the worker does not support being closed multiple times.
func (w *worker) close() {
	w.running.Store(false)
	close(w.exitCh)
	w.wg.Wait()
}

// recalcRecommit recalculates the resubmitting interval upon feedback.
func recalcRecommit(minRecommit, prev time.Duration, target float64, inc bool) time.Duration {
	//var (
	//	prevF = float64(prev.Nanoseconds())
	//	next  float64
	//)
	//if inc {
	//	next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target+intervalAdjustBias)
	//	max := float64(maxRecommitInterval.Nanoseconds())
	//	if next > max {
	//		next = max
	//	}
	//} else {
	//	next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target-intervalAdjustBias)
	//	min := float64(minRecommit.Nanoseconds())
	//	if next < min {
	//		next = min
	//	}
	//}
	return prev
}

// newWorkLoop is a standalone goroutine to submit new sealing work upon received events.
//
//nolint:gocognit
func (w *worker) newWorkLoop(recommit time.Duration) {
	defer w.wg.Done()

	var (
		interrupt   *atomic.Int32
		minRecommit = recommit // minimal resubmit interval specified by user.
		timestamp   int64      // timestamp for each round of sealing.
	)

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // discard the initial tick

	veblopTimeout := time.Duration(w.chainConfig.Bor.CalculatePeriod(w.chain.CurrentBlock().Number.Uint64())) * time.Second
	if veblopTimeout < w.blockTime {
		veblopTimeout = w.blockTime
	}
	veblopTimer := time.NewTimer(veblopTimeout)
	defer veblopTimer.Stop()

	// commit aborts in-flight transaction execution with given signal and resubmits a new one.
	commit := func(noempty bool, s int32) {
		if interrupt != nil {
			interrupt.Store(s)
		}

		interrupt = new(atomic.Int32)
		select {
		case w.newWorkCh <- &newWorkReq{interrupt: interrupt, timestamp: timestamp, noempty: noempty}:
		case <-w.exitCh:
			return
		}
		timer.Reset(recommit)
		veblopTimeout = time.Duration(w.chainConfig.Bor.CalculatePeriod(w.chain.CurrentBlock().Number.Uint64())) * time.Second
		if veblopTimeout < w.blockTime {
			veblopTimeout = w.blockTime
		}
		veblopTimer.Reset(veblopTimeout)
		w.newTxs.Store(0)
	}

	for {
		select {
		case <-w.startCh:
			w.clearPending(w.chain.CurrentBlock().Number.Uint64())

			timestamp = time.Now().Unix()
			w.pendingWorkBlock.Store(w.chain.CurrentBlock().Number.Uint64() + 1)
			commit(false, commitInterruptNewHead)

		case head := <-w.chainHeadCh:
			w.clearPending(head.Header.Number.Uint64())

			pendingWorkBlock := w.pendingWorkBlock.Load()
			if pendingWorkBlock == head.Header.Number.Uint64()+1 {
				// Next block is already being worked on, skip the commit.
				continue
			}

			timestamp = time.Now().Unix()
			w.pendingWorkBlock.Store(head.Header.Number.Uint64() + 1)
			commit(false, commitInterruptNewHead)

		case <-veblopTimer.C:
			currentBlock := w.chain.CurrentBlock()

			veblopTimeout = time.Duration(w.chainConfig.Bor.CalculatePeriod(currentBlock.Number.Uint64())) * time.Second
			if veblopTimeout < w.blockTime {
				veblopTimeout = w.blockTime
			}

			if w.chainConfig.Bor == nil || !w.chainConfig.Bor.IsRio(currentBlock.Number) {
				veblopTimer.Reset(veblopTimeout)
				continue
			}

			w.pendingMu.RLock()
			hasPendingTasks := len(w.pendingTasks) > 0
			w.pendingMu.RUnlock()

			pendingWorkBlock := w.pendingWorkBlock.Load()
			if pendingWorkBlock == currentBlock.Number.Uint64()+1 {
				// Next block is already being worked on, reset the timer.
				veblopTimer.Reset(veblopTimeout)
				continue
			}

			if !hasPendingTasks && time.Now().Unix()-int64(currentBlock.Time) >= int64(veblopTimeout.Seconds()) {
				timestamp = time.Now().Unix()
				w.pendingWorkBlock.Store(currentBlock.Number.Uint64() + 1)
				commit(false, commitInterruptNewHead)
				// veblopTimer is already reset by commit() so we don't need to reset it here.
			} else {
				veblopTimer.Reset(veblopTimeout)
			}

		case <-timer.C:
			// Recommit disabled due to the current low block period (no need to capture more txs on the block already built)
			continue

		case interval := <-w.resubmitIntervalCh:
			// Adjust resubmit interval explicitly by user.
			if interval < minRecommitInterval {
				log.Warn("Sanitizing miner recommit interval", "provided", interval, "updated", minRecommitInterval)
				interval = minRecommitInterval
			}

			log.Info("Miner recommit interval update", "from", minRecommit, "to", interval)
			minRecommit, recommit = interval, interval

			if w.resubmitHook != nil {
				w.resubmitHook(minRecommit, recommit)
			}

		case adjust := <-w.resubmitAdjustCh:
			// Adjust resubmit interval by feedback.
			if adjust.inc {
				before := recommit
				target := float64(recommit.Nanoseconds()) / adjust.ratio
				recommit = recalcRecommit(minRecommit, recommit, target, true)
				log.Trace("Increase miner recommit interval", "from", before, "to", recommit)
			} else {
				before := recommit
				recommit = recalcRecommit(minRecommit, recommit, float64(minRecommit.Nanoseconds()), false)
				log.Trace("Decrease miner recommit interval", "from", before, "to", recommit)
			}

			if w.resubmitHook != nil {
				w.resubmitHook(minRecommit, recommit)
			}

		case <-w.exitCh:
			return
		}
	}
}

// mainLoop is responsible for generating and submitting sealing work based on
// the received event. It can support two modes: automatically generate task and
// submit it or return task according to given parameters for various proposes.
// nolint:gocognit, contextcheck
func (w *worker) mainLoop() {
	defer w.wg.Done()
	defer w.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	slowTxWindowTicker := time.NewTicker(slowTxWindowPeriod)
	defer slowTxWindowTicker.Stop()
	defer func() {
		w.currentMu.Lock()
		if w.current != nil {
			w.current.discard()
		}
		w.currentMu.Unlock()
	}()

	bor, isBor := w.engine.(*bor.Bor)
	devFakeAuthor := isBor && bor != nil && bor.DevFakeAuthor
	for {
		select {
		case req := <-w.newWorkCh:
			if w.chainConfig.ChainID.Cmp(params.BorMainnetChainConfig.ChainID) == 0 || w.chainConfig.ChainID.Cmp(params.MumbaiChainConfig.ChainID) == 0 || w.chainConfig.ChainID.Cmp(params.AmoyChainConfig.ChainID) == 0 {
				if w.eth.PeerCount() > 0 || devFakeAuthor {
					//nolint:contextcheck
					w.commitWork(req.interrupt, req.noempty, req.timestamp)
				}
			} else {
				//nolint:contextcheck
				w.commitWork(req.interrupt, req.noempty, req.timestamp)
			}

		case req := <-w.getWorkCh:
			req.result <- w.generateWork(req.params, false)

		case ev := <-w.txsCh:
			// Apply transactions to the pending state if we're not sealing
			//
			// Note all transactions received may not be continuous with transactions
			// already included in the current sealing block. These transactions will
			// be automatically eliminated.
			// nolint : nestif
			if !w.IsRunning() && w.current != nil {
				// If block is already full, abort
				if gp := w.current.gasPool; gp != nil && gp.Gas() < params.TxGas {
					continue
				}
				// If we don't have time to execute (i.e. we're past header timestamp), abort
				delay := time.Until(time.Unix(int64(w.current.header.Time), 0))
				if delay <= 0 {
					continue
				}
				txs := make(map[common.Address][]*txpool.LazyTransaction, len(ev.Txs))
				for _, tx := range ev.Txs {
					acc, _ := types.Sender(w.current.signer, tx)
					txs[acc] = append(txs[acc], &txpool.LazyTransaction{
						Pool:      w.eth.TxPool(), // We don't know where this came from, yolo resolve from everywhere
						Hash:      tx.Hash(),
						Tx:        nil, // Do *not* set this! We need to resolve it later to pull blobs in
						Time:      tx.Time(),
						GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
						GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
						Gas:       tx.Gas(),
						BlobGas:   tx.BlobGas(),
					})
				}

				stopFn := func() {}
				if w.interruptCommitFlag {
					stopFn = createInterruptTimer(
						w.current.header.Number.Uint64(),
						w.current.header.GetActualTime(),
						&w.interruptBlockBuilding,
						&w.interruptFlagSetAt,
					)
				}

				plainTxs := newTransactionsByPriceAndNonce(w.current.signer, txs, w.current.header.BaseFee, &w.interruptBlockBuilding) // Mixed bag of everrything, yolo
				blobTxs := newTransactionsByPriceAndNonce(w.current.signer, nil, w.current.header.BaseFee, &w.interruptBlockBuilding)  // Empty bag, don't bother optimising

				tcount := w.current.tcount

				w.commitTransactions(w.current, plainTxs, blobTxs, nil)
				stopFn()

				// Only update the snapshot if any new transactons were added
				// to the pending block
				if tcount != w.current.tcount {
					w.updateSnapshot(w.current)
				}
			} else {
				// Special case, if the consensus engine is 0 period clique(dev mode),
				// submit sealing work here since all empty submission will be rejected
				// by clique. Of course the advance sealing(empty submission) is disabled.
				if w.chainConfig.Clique != nil && w.chainConfig.Clique.Period == 0 {
					w.commitWork(nil, true, time.Now().Unix())
				}
			}

			w.newTxs.Add(int32(len(ev.Txs)))

		case tickAt := <-slowTxWindowTicker.C:
			if w.IsRunning() {
				w.flushSlowTxWindow(tickAt)
			} else {
				// Avoid carrying stale data across non-producer windows.
				w.slowTxTracker.Reset()
			}

		// System stopped
		case <-w.exitCh:
			return
		case <-w.txsSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		}
	}
}

// taskLoop is a standalone goroutine to fetch sealing task from the generator and
// push them to consensus engine.
func (w *worker) taskLoop() {
	defer w.wg.Done()

	var (
		stopCh chan struct{}
		prev   common.Hash
	)

	// interrupt aborts the in-flight sealing task.
	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}

	for {
		select {
		case task := <-w.taskCh:
			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}
			// Reject duplicate sealing work due to resubmitting.
			sealHash := w.engine.SealHash(task.block.Header())
			if sealHash == prev {
				continue
			}
			// Interrupt previous sealing operation
			interrupt()

			stopCh, prev = make(chan struct{}), sealHash

			if w.skipSealHook != nil && w.skipSealHook(task) {
				continue
			}

			w.pendingMu.Lock()
			w.pendingTasks[sealHash] = task
			w.pendingMu.Unlock()

			if err := w.engine.Seal(w.chain, task.block, task.state.Witness(), w.resultCh, stopCh); err != nil {
				log.Warn("Block sealing failed", "err", err)
				w.pendingMu.Lock()
				delete(w.pendingTasks, sealHash)
				w.pendingMu.Unlock()
			}
		case <-w.exitCh:
			interrupt()
			return
		}
	}
}

// resultLoop is a standalone goroutine to handle sealing result submitting
// and flush relative data to the database.
func (w *worker) resultLoop() {
	defer w.wg.Done()

	for {
		select {
		case newSealedBlockEvent := <-w.resultCh:

			// Short circuit when receiving empty result.
			if newSealedBlockEvent == nil {
				continue
			}
			block := newSealedBlockEvent.Block
			witness := newSealedBlockEvent.Witness
			if block == nil {
				continue
			}

			// Short circuit when receiving duplicate result caused by resubmitting.
			if w.chain.HasBlock(block.Hash(), block.NumberU64()) {
				continue
			}

			// Skip if the sealed block is behind current head (stale block from before reorg)
			currentBlock := w.chain.CurrentBlock()
			if currentBlock != nil && block.NumberU64() <= currentBlock.Number.Uint64() {
				log.Info("Skipping stale sealed block", "sealed", block.NumberU64(), "current", currentBlock.Number.Uint64())
				continue
			}

			oldBlock := w.chain.GetBlockByNumber(block.NumberU64())
			if oldBlock != nil {
				oldBlockAuthor, _ := w.chain.Engine().Author(oldBlock.Header())
				newBlockAuthor, _ := w.chain.Engine().Author(block.Header())

				if oldBlockAuthor == newBlockAuthor {
					log.Info("same block ", "height", block.NumberU64())
					continue
				}
			}

			var (
				sealhash = w.engine.SealHash(block.Header())
				hash     = block.Hash()
			)

			w.pendingMu.RLock()
			task, exist := w.pendingTasks[sealhash]
			w.pendingMu.RUnlock()

			if !exist {
				log.Error("Block found but no relative pending task", "number", block.Number(), "sealhash", sealhash, "hash", hash)
				continue
			}
			// Different block could share same sealhash, deep copy here to prevent write-write conflict.
			var (
				receipts = make([]*types.Receipt, len(task.receipts))
				logs     []*types.Log
				err      error
			)

			for i, taskReceipt := range task.receipts {
				receipt := new(types.Receipt)
				receipts[i] = receipt
				*receipt = *taskReceipt

				// add block location fields
				receipt.BlockHash = hash
				receipt.BlockNumber = block.Number()
				receipt.TransactionIndex = uint(i)

				// Update the block hash in all logs since it is now available and not when the
				// receipt/log of individual transactions were created.
				receipt.Logs = make([]*types.Log, len(taskReceipt.Logs))

				for i, taskLog := range taskReceipt.Logs {
					log := new(types.Log)
					receipt.Logs[i] = log
					*log = *taskLog
					log.BlockHash = hash
				}

				logs = append(logs, receipt.Logs...)
			}

			if witness != nil {
				witness.SetHeader(block.Header())
			}

			// Execution metrics: emitted before write because these values are final after
			// FinalizeAndAssemble and do not depend on write success — matching the import path
			// which emits read/update/hash/execution/bor metrics before writeBlockAndSetHead.
			// Emitting here avoids losing these observations on a rare write failure.
			if metrics.Enabled() {
				workerAccountReadTimer.Update(task.state.AccountReads)
				workerStorageReadTimer.Update(task.state.StorageReads)
				workerSnapshotAccountReadTimer.Update(task.state.SnapshotAccountReads)
				workerSnapshotStorageReadTimer.Update(task.state.SnapshotStorageReads)
				workerAccountUpdateTimer.Update(task.state.AccountUpdates)
				workerStorageUpdateTimer.Update(task.state.StorageUpdates)
				workerAccountHashTimer.Update(task.state.AccountHashes)
				workerStorageHashTimer.Update(task.state.StorageHashes)
				workerBorConsensusTimer.Update(task.state.BorConsensusTime)
				trieRead := task.state.SnapshotAccountReads + task.state.AccountReads +
					task.state.SnapshotStorageReads + task.state.StorageReads
				// productionElapsed covers fillTx + FinalizeAndAssemble; subtract trie reads,
				// Bor consensus time, and IntermediateRoot time to isolate pure EVM execution time.
				// Mirrors the import path formula in blockchain.go (writeBlockAndSetHead),
				// where ptime already excludes vtime (IntermediateRoot) via explicit subtraction.
				// Clamped to zero to avoid negative histogram samples from measurement jitter.
				execTime := task.productionElapsed - trieRead - task.state.BorConsensusTime - task.intermediateRootTime
				if execTime < 0 {
					execTime = 0
				}
				workerBlockExecutionTimer.Update(execTime)
			}

			// Commit block and state to database.
			writeStart := time.Now()
			_, err = w.chain.WriteBlockAndSetHead(block, receipts, logs, task.state, true)
			writeElapsed := time.Since(writeStart)
			writeBlockAndSetHeadTimer.Update(writeElapsed)

			if err != nil {
				log.Error("Failed writing block to chain", "err", err)
				// Error writing block to chain, delete the pending task.
				w.pendingMu.Lock()
				delete(w.pendingTasks, sealhash)
				w.pendingMu.Unlock()
				continue
			}

			// Commit metrics: emitted only after a successful write because these values are
			// populated by WriteBlockAndSetHead → CommitWithUpdate. Emitting on failure would
			// record zeroes or stale data — matching the import path which also gates commit
			// metrics after a successful writeBlockAndSetHead.
			if metrics.Enabled() {
				workerAccountCommitTimer.Update(task.state.AccountCommits)
				workerStorageCommitTimer.Update(task.state.StorageCommits)
				workerSnapshotCommitTimer.Update(task.state.SnapshotCommits)
				workerTriedbCommitTimer.Update(task.state.TrieDBCommits)
				workerWitnessCollectionTimer.Update(task.state.WitnessCollection)

				// MGas/s: denominator includes both production and write time, matching blockchain.go
				// which measures elapsed after writeBlockAndSetHead returns
				// (gas * 1000 / elapsed_nanoseconds stores milli-gas/ns = MGas/s as a Duration value).
				if total := task.productionElapsed + writeElapsed; total > 0 {
					workerMgaspsTimer.Update(time.Duration(float64(block.GasUsed()) * 1000 / float64(total)))
				}
			}

			log.Info("Successfully sealed new block", "number", block.Number(), "sealhash", sealhash, "hash", hash,
				"elapsed", common.PrettyDuration(time.Since(task.createdAt)))

			// Broadcast the block and announce chain insertion event
			w.mux.Post(core.NewMinedBlockEvent{Block: block, Witness: witness, SealedAt: time.Now()})

			sealedBlocksCounter.Inc(1)

			if block.Transactions().Len() == 0 {
				sealedEmptyBlocksCounter.Inc(1)
			}

			// Clear all pending tasks for blocks at or below the sealed block number.
			// These tasks are now obsolete since the chain has progressed past them.
			w.clearPending(block.NumberU64())

		case <-w.exitCh:
			return
		}
	}
}

// makeEnv creates a new environment for the sealing block.
func (w *worker) makeEnv(header *types.Header, coinbase common.Address, witness bool, genParams *generateParams) (*environment, error) {
	var state *state.StateDB

	// If statedb is not provided (e.g., from getSealingBlock path), create it
	if genParams.statedb == nil {
		parent := w.chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
		if parent == nil {
			return nil, fmt.Errorf("parent block not found")
		}
		var err error
		state, err = w.chain.StateAt(parent.Root)
		if err != nil {
			return nil, err
		}
	} else {
		// Use the provided statedb (from commitWork with dual readers)
		state = genParams.statedb
	}

	if witness {
		bundle, err := stateless.NewWitness(header, w.chain)
		if err != nil {
			return nil, err
		}
		state.StartPrefetcher("miner", bundle, nil)
	} else {
		// todo: @anshalshukla - check if witness is required
		state.StartPrefetcher("miner", nil, nil)
	}

	// Note the passed coinbase may be different with header.Coinbase.
	env := &environment{
		signer:         types.MakeSigner(w.chainConfig, header.Number, header.Time),
		state:          state,
		size:           uint64(header.Size()),
		coinbase:       coinbase,
		header:         header,
		witness:        state.Witness(),
		evm:            vm.NewEVM(core.NewEVMBlockContext(header, w.chain, &coinbase), state, w.chainConfig, vm.Config{}),
		prefetchReader: genParams.prefetchReader,
		processReader:  genParams.processReader,
	}
	env.evm.SetInterrupt(&w.interruptBlockBuilding)

	// Keep track of transactions which return errors so they can be removed
	env.tcount = 0
	env.mvReadMapList = []map[blockstm.Key]blockstm.ReadDescriptor{}

	return env, nil
}

// updateSnapshot updates pending snapshot block, receipts and state.
func (w *worker) updateSnapshot(env *environment) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = types.NewBlock(
		env.header,
		&types.Body{
			Transactions: env.txs,
		},
		env.receipts,
		trie.NewStackTrie(nil),
	)

	w.snapshotReceipts = copyReceipts(env.receipts)
	w.snapshotState = env.state.Copy()
}

func (w *worker) commitTransaction(env *environment, tx *types.Transaction) ([]*types.Log, error) {
	var (
		snap = env.state.Snapshot()
		gp   = env.gasPool.Gas()
	)

	receipt, err := core.ApplyTransaction(env.evm, env.gasPool, env.state, env.header, tx, &env.header.GasUsed)
	if err != nil {
		env.state.RevertToSnapshot(snap)
		env.gasPool.SetGas(gp)

		return nil, err
	}
	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)
	env.tcount++

	return receipt.Logs, nil
}

func (w *worker) commitTransactions(env *environment, plainTxs, blobTxs *transactionsByPriceAndNonce, interrupt *atomic.Int32) error {
	defer func(t0 time.Time) {
		commitTransactionsTimer.Update(time.Since(t0))
	}(time.Now())

	gasLimit := env.header.GasLimit
	if env.gasPool == nil {
		env.gasPool = new(core.GasPool).AddGas(gasLimit)
	}

	var coalescedLogs []*types.Log

	var deps map[int]map[int]bool

	var depsBuilder *blockstm.DepsBuilder
	var chDeps chan blockstm.TxReadWriteSet

	var depsWg sync.WaitGroup
	var once sync.Once

	EnableMVHashMap := w.chainConfig.IsCancun(env.header.Number)

	// create and add empty mvHashMap in statedb
	if EnableMVHashMap && w.IsRunning() {
		depsBuilder = blockstm.NewDepsBuilder()
		chDeps = make(chan blockstm.TxReadWriteSet)

		// Make sure we safely close the channel in case of interrupt
		defer once.Do(func() {
			close(chDeps)
		})

		depsWg.Add(1)

		go func(chDeps chan blockstm.TxReadWriteSet) {
			for t := range chDeps {
				if err := depsBuilder.AddTransaction(t.Index, t.ReadList, t.WriteList); err != nil {
					// Non-sequential index indicates a systematic bug, not a transient error.
					// Drain the channel so the sender never blocks, then stop processing.
					log.Error("Failed to build tx dependency metadata, dropping DAG hint", "tx", t.Index, "err", err)
					for range chDeps {
					}
					break
				}
			}
			depsWg.Done()
		}(chDeps)
	}

	var lastTxHash common.Hash

	var (
		lastCommitStart        time.Time      // start of the most recent commitTransaction call
		lastTxIndex            int            // index of the last attempted tx (for interrupt context)
		lastTxSender           common.Address // sender of the last attempted tx (for interrupt context)
		flagToTxInterruptDelay time.Duration  // delay from setting interrupt flag to tx interruption
		hasTxInterruptDelay    bool
	)
	lastTxIndex = -1

mainloop:
	for {
		// Check interruption signal and abort building if it's fired.
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				return signalToErr(signal)
			}
		}

		if EnableMVHashMap && w.IsRunning() {
			env.state.AddEmptyMVHashMap()
		}

		// Check for the flag to interrupt block building on timeout.
		if w.interruptBlockBuilding.Load() {
			txCommitInterruptCounter.Inc(1)
			logCtx := []interface{}{
				"number", env.header.Number.Uint64(),
				"headerTime", common.PrettyTime(time.Unix(int64(env.header.Time), 0)),
			}
			if flagSetAt := w.interruptFlagSetAt.Load(); flagSetAt > 0 {
				flagSetTime := time.Unix(0, flagSetAt)
				logCtx = append(logCtx, "flagSetAt", common.PrettyTime(flagSetTime))
				logCtx = append(logCtx, "flagToAbortDelay", common.PrettyDuration(time.Since(flagSetTime)))
			}
			if hasTxInterruptDelay {
				logCtx = append(logCtx, "flagToTxInterruptDelay", common.PrettyDuration(flagToTxInterruptDelay))
			}
			if !lastCommitStart.IsZero() {
				logCtx = append(logCtx, "txHash", lastTxHash.Hex())
				logCtx = append(logCtx, "txIndex", lastTxIndex)
				logCtx = append(logCtx, "sender", lastTxSender)
				logCtx = append(logCtx, "txElapsed", common.PrettyDuration(time.Since(lastCommitStart)))
			}

			if w.IsRunning() {
				log.Info("Block building interrupted due to timeout, aborting new transaction commits", logCtx...)
			} else {
				log.Debug("Block building interrupted due to timeout, aborting new transaction commits", logCtx...)
			}

			break mainloop
		}

		// If we don't have enough gas for any further transactions then we're done.
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}
		// If we don't have enough blob space for any further blob transactions,
		// skip that list altogether
		if !blobTxs.Empty() && env.blobs >= eip4844.MaxBlobsPerBlock(w.chainConfig, env.header.Time) {
			log.Trace("Not enough blob space for further blob transactions")
			blobTxs.Clear()
			// Fall though to pick up any plain txs
		}
		// Retrieve the next transaction and abort if all done.

		var (
			ltx *txpool.LazyTransaction
			txs *transactionsByPriceAndNonce
		)
		pltx, ptip := plainTxs.Peek()
		bltx, btip := blobTxs.Peek()

		switch {
		case pltx == nil:
			txs, ltx = blobTxs, bltx
		case bltx == nil:
			txs, ltx = plainTxs, pltx
		default:
			if ptip.Lt(btip) {
				txs, ltx = blobTxs, bltx
			} else {
				txs, ltx = plainTxs, pltx
			}
		}
		if ltx == nil {
			break
		}
		// If we don't have enough space for the next transaction, skip the account.
		if env.gasPool.Gas() < ltx.Gas {
			log.Trace("Not enough gas left for transaction", "hash", ltx.Hash, "left", env.gasPool.Gas(), "needed", ltx.Gas)
			txs.Pop()
			continue
		}

		// Transaction seems to fit, pull it up from the pool
		tx := ltx.Resolve()
		if tx == nil {
			log.Trace("Ignoring evicted transaction", "hash", ltx.Hash)
			txs.Pop()
			continue
		}

		// Make sure all transactions after osaka have cell proofs
		if w.chainConfig.IsOsaka(env.header.Number) {
			if sidecar := tx.BlobTxSidecar(); sidecar != nil {
				if sidecar.Version == 0 {
					log.Info("Including blob tx with v0 sidecar, recomputing proofs", "hash", ltx.Hash)
					sidecar.Proofs = make([]kzg4844.Proof, 0, len(sidecar.Blobs)*kzg4844.CellProofsPerBlob)
					for _, blob := range sidecar.Blobs {
						cellProofs, err := kzg4844.ComputeCellProofs(&blob)
						if err != nil {
							panic(err)
						}
						sidecar.Proofs = append(sidecar.Proofs, cellProofs...)
					}
				}
			}
		}
		// if inclusion of the transaction would put the block size over the
		// maximum we allow, don't add any more txs to the payload.
		if !env.txFitsSize(tx) {
			break
		}
		// Error may be ignored here. The error has already been checked
		// during transaction acceptance in the transaction pool.
		from, _ := types.Sender(env.signer, tx)

		// not prioritising conditional transaction, yet.
		//nolint:nestif
		if options := tx.GetOptions(); options != nil {
			if err := env.header.ValidateBlockNumberOptionsPIP15(options.BlockNumberMin, options.BlockNumberMax); err != nil {
				log.Trace("Dropping conditional transaction", "from", from, "hash", tx.Hash(), "reason", err)
				txs.Pop()

				continue
			}

			if err := env.header.ValidateTimestampOptionsPIP15(options.TimestampMin, options.TimestampMax); err != nil {
				log.Trace("Dropping conditional transaction", "from", from, "hash", tx.Hash(), "reason", err)
				txs.Pop()

				continue
			}

			if err := env.state.ValidateKnownAccounts(options.KnownAccounts); err != nil {
				log.Trace("Dropping conditional transaction", "from", from, "hash", tx.Hash(), "reason", err)
				txs.Pop()

				continue
			}
		}

		// Check whether the tx is replay protected. If we're not in the EIP155 hf
		// phase, start ignoring the sender until we do.
		if tx.Protected() && !w.chainConfig.IsEIP155(env.header.Number) {
			log.Trace("Ignoring replay protected transaction", "hash", ltx.Hash, "eip155", w.chainConfig.EIP155Block)
			txs.Pop()
			continue
		}
		// Start executing the transaction
		lastCommitStart = time.Now()
		lastTxHash = tx.Hash()
		lastTxIndex = env.tcount
		lastTxSender = from
		env.state.SetTxContext(tx.Hash(), env.tcount)

		logs, err := w.commitTransaction(env, tx)
		txDuration := time.Since(lastCommitStart)

		// Set mock delay (if any) between transactions for tests
		time.Sleep(time.Duration(w.mockTxDelay) * time.Millisecond)

		switch {
		case errors.Is(err, core.ErrNonceTooLow):
			// New head notification data race between the transaction pool and miner, shift
			log.Trace("Skipping transaction with low nonce", "hash", ltx.Hash, "sender", from, "nonce", tx.Nonce())
			txs.Shift()

		case errors.Is(err, nil):
			// Everything ok, collect the logs and shift in the next transaction from the same account
			coalescedLogs = append(coalescedLogs, logs...)
			if metrics.Enabled() {
				txApplyDurationTimer.Update(txDuration)
			}
			if w.IsRunning() {
				w.slowTxTracker.Add(txTimingEntry{hash: tx.Hash(), duration: txDuration})
			}

			if EnableMVHashMap && w.IsRunning() {
				env.mvReadMapList = append(env.mvReadMapList, env.state.MVReadMap())

				if env.tcount > len(env.mvReadMapList) {
					log.Warn("blockstm - env.tcount > len(env.mvReadMapList)", "env.tcount", env.tcount, "len(mvReadMapList)", len(env.mvReadMapList))
					return errors.New("transaction count exceeds dependency list length")
				}

				temp := blockstm.TxReadWriteSet{
					Index:     env.tcount - 1,
					ReadList:  env.state.MVReadList(),
					WriteList: env.state.MVFullWriteList(),
				}

				// Send with timeout to prevent deadlock
				select {
				case chDeps <- temp:
					// Successfully sent
				case <-time.After(1 * time.Second):
					// Timeout after 1 second - channel is blocked
					log.Error("Transaction dependency channel blocked, aborting block building",
						"txIndex", env.tcount-1,
						"blockNumber", env.header.Number.Uint64())
					once.Do(func() {
						close(chDeps)
					})
					return errors.New("dependency channel timeout")
				}
			}

			txs.Shift()

		case errors.Is(err, vm.ErrInterrupt):
			// Timeout interrupt surfaced from EVM execution for this tx.
			if !hasTxInterruptDelay {
				if flagSetAt := w.interruptFlagSetAt.Load(); flagSetAt > 0 {
					flagToTxInterruptDelay = time.Since(time.Unix(0, flagSetAt))
					hasTxInterruptDelay = true
				}
			}
			log.Debug("Transaction interrupted due to timeout", "hash", ltx.Hash, "err", err)
			txs.Pop()

		default:
			// Transaction is regarded as invalid, drop all consecutive transactions from
			// the same sender because of `nonce-too-high` clause.
			log.Debug("Transaction failed, account skipped", "hash", ltx.Hash, "err", err)
			txs.Pop()
		}

		if EnableMVHashMap && w.IsRunning() {
			env.state.ClearReadMap()
			env.state.ClearWriteMap()
		}
	}

	// nolint:nestif
	if EnableMVHashMap && w.IsRunning() {
		once.Do(func() {
			close(chDeps)
		})
		depsWg.Wait()

		deps = depsBuilder.GetDeps()
		if deps == nil {
			log.Warn("Failed to build tx dependency DAG, skipping metadata", "number", env.header.Number)
		}

		var blockExtraData types.BlockExtraData

		tempVanity := env.header.Extra[:types.ExtraVanityLength]
		tempSeal := env.header.Extra[len(env.header.Extra)-types.ExtraSealLength:]

		// Always decode header extra data before overwriting TxDependency.
		if err := rlp.DecodeBytes(env.header.Extra[types.ExtraVanityLength:len(env.header.Extra)-types.ExtraSealLength], &blockExtraData); err != nil {
			log.Error("error while decoding block extra data", "err", err)
			return err
		}

		// deps is nil when DepsBuilder errored, and non-nil empty when no transactions were added.
		if deps != nil && len(env.mvReadMapList) > 0 {
			tempDeps := make([][]uint64, len(env.mvReadMapList))

			for j := range deps[0] {
				tempDeps[0] = append(tempDeps[0], uint64(j))
			}

			delayFlag := true

			for i := 1; i <= len(env.mvReadMapList)-1; i++ {
				reads := env.mvReadMapList[i]

				// Coinbase and burn-contract balance reads create an implicit ordering not captured by the DAG.
				_, ok1 := reads[blockstm.NewSubpathKey(env.coinbase, state.BalancePath)]
				_, ok2 := reads[blockstm.NewSubpathKey(common.HexToAddress(w.chainConfig.Bor.CalculateBurntContract(env.header.Number.Uint64())), state.BalancePath)]
				if ok1 || ok2 {
					delayFlag = false
					break
				}

				for j := range deps[i] {
					tempDeps[i] = append(tempDeps[i], uint64(j))
				}
			}

			if delayFlag {
				blockExtraData.TxDependency = tempDeps
			} else {
				blockExtraData.TxDependency = nil
			}
		} else {
			blockExtraData.TxDependency = nil
		}

		blockExtraDataBytes, err := rlp.EncodeToBytes(blockExtraData)
		if err != nil {
			log.Error("error while encoding block extra data: %v", err)
			return err
		}

		env.header.Extra = []byte{}
		env.header.Extra = append(tempVanity, blockExtraDataBytes...)
		env.header.Extra = append(env.header.Extra, tempSeal...)
	}

	if !w.IsRunning() && len(coalescedLogs) > 0 {
		// We don't push the pendingLogsEvent while we are sealing. The reason is that
		// when we are sealing, the worker will regenerate a sealing block every 3 seconds.
		// In order to avoid pushing the repeated pendingLog, we disable the pending log pushing.
		// make a copy, the state caches the logs and these logs get "upgraded" from pending to mined
		// logs by filling in the block hash when the block was mined by the local miner. This can
		// cause a race condition if a log was "upgraded" before the PendingLogsEvent is processed.
		cpy := make([]*types.Log, len(coalescedLogs))
		for i, l := range coalescedLogs {
			cpy[i] = new(types.Log)
			*cpy[i] = *l
		}

		w.pendingLogsFeed.Send(cpy)
	}

	return nil
}

// generateParams wraps various of settings for generating sealing task.
type generateParams struct {
	timestamp          uint64                // The timestamp for sealing task
	forceTime          bool                  // Flag whether the given timestamp is immutable or not
	parentHash         common.Hash           // Parent block hash, empty means the latest chain head
	coinbase           common.Address        // The fee recipient address for including transaction
	random             common.Hash           // The randomness generated by beacon chain, empty before the merge
	withdrawals        types.Withdrawals     // List of withdrawals to include in block.
	beaconRoot         *common.Hash          // The beacon root (cancun field).
	noTxs              bool                  // Flag whether an empty block without any transaction is expected
	statedb            *state.StateDB        // The statedb to use for block generation
	prefetchReader     state.ReaderWithStats // The prefetch reader to use for statistics
	processReader      state.ReaderWithStats // The process reader to use for statistics
	prefetchedTxHashes *sync.Map             // Map of successfully prefetched transaction hashes
	productionStart    time.Time             // Start of full-block building (after optional empty pre-seal); used for productionElapsed
}

// makeHeader creates a new block header for sealing.
func (w *worker) makeHeader(genParams *generateParams, waitOnPrepare bool) (*types.Header, common.Address, error) {
	// Find the parent block for sealing task
	parent := w.chain.CurrentBlock()

	if genParams.parentHash != (common.Hash{}) {
		block := w.chain.GetBlockByHash(genParams.parentHash)
		if block == nil {
			return nil, common.Address{}, fmt.Errorf("missing parent")
		}

		parent = block.Header()
	}
	// Sanity check the timestamp correctness, recap the timestamp
	// to parent+1 if the mutation is allowed.
	timestamp := genParams.timestamp
	if parent.Time >= timestamp {
		if genParams.forceTime {
			return nil, common.Address{}, fmt.Errorf("invalid timestamp, parent %d given %d", parent.Time, timestamp)
		}

		timestamp = parent.Time + 1
	}

	var coinbase common.Address
	newBlockNumber := new(big.Int).Add(parent.Number, common.Big1)
	if w.chainConfig.Bor != nil && w.chainConfig.Bor.IsRio(newBlockNumber) {
		coinbase = common.HexToAddress(w.chainConfig.Bor.CalculateCoinbase(newBlockNumber.Uint64()))

		// In case of coinbase is not set post Rio, use the default coinbase
		if coinbase == (common.Address{}) {
			coinbase = genParams.coinbase
		}
	} else {
		coinbase = genParams.coinbase
	}

	// Calculate desired gas limit (may be dynamically adjusted based on base fee)
	desiredGasLimit := w.calculateDesiredGasLimit(parent)

	// Construct the sealing block header.
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     newBlockNumber,
		GasLimit:   core.CalcGasLimit(parent.GasLimit, desiredGasLimit),
		Time:       timestamp,
		Coinbase:   coinbase,
	}
	// Set the extra field.
	if len(w.extra) != 0 {
		header.Extra = w.extra
	}
	// Set the randomness field from the beacon chain if it's available.
	if genParams.random != (common.Hash{}) {
		header.MixDigest = genParams.random
	}
	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	if w.chainConfig.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(w.chainConfig, parent)
		if !w.chainConfig.IsLondon(parent.Number) {
			parentGasLimit := parent.GasLimit * w.chainConfig.ElasticityMultiplier()
			header.GasLimit = core.CalcGasLimit(parentGasLimit, desiredGasLimit)
		}
	}

	header.BlobGasUsed = nil
	header.ExcessBlobGas = nil
	header.ParentBeaconRoot = nil

	// Run the consensus preparation with the default or customized consensus engine.
	if err := w.engine.Prepare(w.chain, header, waitOnPrepare); err != nil {
		switch err.(type) {
		case *bor.UnauthorizedSignerError:
			log.Debug("Failed to prepare header for sealing", "err", err)
		default:
			log.Error("Failed to prepare header for sealing", "err", err)
		}

		return nil, common.Address{}, err
	}

	return header, coinbase, nil
}

// prepareWork constructs the sealing task according to the given parameters,
// either based on the last chain head or specified parent. In this function
// the pending transactions are not filled yet, only the empty task returned.
func (w *worker) prepareWork(genParams *generateParams, witness bool) (*environment, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	header, coinbase, err := w.makeHeader(genParams, true)
	if err != nil {
		return nil, err
	}

	// Could potentially happen if starting to mine in an odd state.
	// Note genParams.coinbase can be different with header.Coinbase
	// since clique algorithm can modify the coinbase field in header.
	env, err := w.makeEnv(header, coinbase, witness, genParams)
	if err != nil {
		log.Error("Failed to create sealing context", "err", err)
		return nil, err
	}
	if header.ParentBeaconRoot != nil {
		context := core.NewEVMBlockContext(header, w.chain, nil)
		vmenv := vm.NewEVM(context, env.state, w.chainConfig, vm.Config{})
		core.ProcessBeaconBlockRoot(*header.ParentBeaconRoot, vmenv)
	}
	if w.chainConfig.IsPrague(header.Number) {
		// EIP-2935
		context := core.NewEVMBlockContext(header, w.chain, nil)
		vmenv := vm.NewEVM(context, env.state, w.chainConfig, vm.Config{})
		core.ProcessParentBlockHash(header.ParentHash, vmenv)
	}
	return env, nil
}

// buildDefaultFilter creates a pending transaction filter based on chain configuration
// and current tip/base fee settings.
func (w *worker) buildDefaultFilter(BaseFee *big.Int, Number *big.Int) txpool.PendingFilter {
	w.mu.RLock()
	tip := w.tip
	w.mu.RUnlock()

	// Retrieve the pending transactions pre-filtered by the 1559/4844 dynamic fees
	filter := txpool.PendingFilter{
		MinTip: uint256.MustFromBig(tip.ToBig()),
	}

	if BaseFee != nil {
		filter.BaseFee = uint256.MustFromBig(BaseFee)
	}

	isOsaka := w.chainConfig.IsOsaka(Number)
	isMadhugiri := w.chainConfig.Bor != nil && w.chainConfig.Bor.IsMadhugiri(Number)
	// Verify tx gas limit does not exceed EIP-7825 cap.
	if isOsaka || isMadhugiri {
		filter.GasLimitCap = params.MaxTxGas
	}

	return filter
}

// fillTransactions retrieves the pending transactions from the txpool and fills them
// into the given sealing block. The transaction selection and ordering strategy can
// be customized with the plugin in the future.

//
//nolint:gocognit
func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment) error {
	w.mu.RLock()
	prio := w.prio
	w.mu.RUnlock()

	filter := w.buildDefaultFilter(env.header.BaseFee, env.header.Number)

	filter.BlobTxs = false
	pendingPlainTxs := w.eth.TxPool().Pending(filter, &w.interruptBlockBuilding)

	filter.BlobTxs = true
	if w.chainConfig.IsOsaka(env.header.Number) {
		filter.BlobVersion = types.BlobSidecarVersion1
	} else {
		filter.BlobVersion = types.BlobSidecarVersion0
	}
	pendingBlobTxs := w.eth.TxPool().Pending(filter, &w.interruptBlockBuilding)

	// Split the pending transactions into locals and remotes.
	prioPlainTxs, normalPlainTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingPlainTxs
	prioBlobTxs, normalBlobTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingBlobTxs

	for _, account := range prio {
		if txs := normalPlainTxs[account]; len(txs) > 0 {
			delete(normalPlainTxs, account)
			prioPlainTxs[account] = txs
		}
		if txs := normalBlobTxs[account]; len(txs) > 0 {
			delete(normalBlobTxs, account)
			prioBlobTxs[account] = txs
		}
	}

	// Fill the block with all available pending transactions.
	if len(prioPlainTxs) > 0 || len(prioBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.signer, prioPlainTxs, env.header.BaseFee, &w.interruptBlockBuilding)
		blobTxs := newTransactionsByPriceAndNonce(env.signer, prioBlobTxs, env.header.BaseFee, &w.interruptBlockBuilding)
		if err := w.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
			return err
		}
	}
	if len(normalPlainTxs) > 0 || len(normalBlobTxs) > 0 {
		heapInitTime := time.Now()
		plainTxs := newTransactionsByPriceAndNonce(env.signer, normalPlainTxs, env.header.BaseFee, &w.interruptBlockBuilding)
		blobTxs := newTransactionsByPriceAndNonce(env.signer, normalBlobTxs, env.header.BaseFee, &w.interruptBlockBuilding)
		txHeapInitTimer.Update(time.Since(heapInitTime))

		if err := w.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
			return err
		}
	}

	return nil
}

// generateWork generates a sealing block based on the given parameters.
func (w *worker) generateWork(params *generateParams, witness bool) *newPayloadResult {
	work, err := w.prepareWork(params, witness)
	if err != nil {
		return &newPayloadResult{err: err}
	}
	defer work.discard()

	if !params.noTxs {
		interrupt := new(atomic.Int32)

		timer := time.AfterFunc(w.newpayloadTimeout, func() {
			interrupt.Store(commitInterruptTimeout)
		})
		defer timer.Stop()

		err := w.fillTransactions(interrupt, work)
		if errors.Is(err, errBlockInterruptedByTimeout) {
			log.Warn("Block building is interrupted", "allowance", common.PrettyDuration(w.newpayloadTimeout))
		}
	}

	body := types.Body{Transactions: work.txs, Withdrawals: params.withdrawals}
	allLogs := make([]*types.Log, 0)
	for _, r := range work.receipts {
		allLogs = append(allLogs, r.Logs...)
	}

	// Polygon/bor: EIP-6110, EIP-7002, and EIP-7251 are not supported
	// Collect consensus-layer requests if Prague is enabled and bor consensus is not active.
	var requests [][]byte
	if w.chainConfig.IsPrague(work.header.Number) && w.chainConfig.Bor == nil {
		// EIP-6110 deposits
		err := core.ParseDepositLogs(&requests, allLogs, w.chainConfig)
		if err != nil {
			return &newPayloadResult{err: err}
		}
		// create EVM for system calls
		blockContext := core.NewEVMBlockContext(work.header, w.chain, &work.header.Coinbase)
		vmenv := vm.NewEVM(blockContext, work.state, w.chainConfig, vm.Config{})
		// EIP-7002 withdrawals
		core.ProcessWithdrawalQueue(&requests, vmenv)
		// EIP-7251 consolidations
		core.ProcessConsolidationQueue(&requests, vmenv)
	}
	if requests != nil {
		reqHash := types.CalcRequestsHash(requests)
		work.header.RequestsHash = &reqHash
	}

	var block *types.Block
	block, work.receipts, _, err = w.engine.FinalizeAndAssemble(w.chain, work.header, work.state, &body, work.receipts)

	if err != nil {
		return &newPayloadResult{err: err}
	}
	return &newPayloadResult{
		block:    block,
		fees:     totalFees(block, work.receipts),
		sidecars: work.sidecars,
		stateDB:  work.state,
		receipts: work.receipts,
		requests: requests,
	}
}

// commitWork generates several new sealing tasks based on the parent block
// and submit them to the sealer.
func (w *worker) commitWork(interrupt *atomic.Int32, noempty bool, timestamp int64) {
	// Abort committing if node is still syncing
	if w.syncing.Load() {
		return
	}

	// Clear the pending work block number when commitWork completes (success or failure).
	defer func() {
		w.pendingWorkBlock.Store(0)
	}()

	// Set the coinbase if the worker is running or it's required
	var coinbase common.Address
	if w.IsRunning() {
		coinbase = w.etherbase()
		if coinbase == (common.Address{}) {
			log.Error("Refusing to mine without etherbase")
			return
		}
	}

	// Find the parent block for sealing task
	parent := w.chain.CurrentBlock()

	// Retrieve the parent state to execute on top, with separate readers for stats tracking.
	state, throwaway, prefetchReader, processReader, err := w.chain.StateAtWithReaders(parent.Root)
	if err != nil {
		return
	}

	genParams := generateParams{
		timestamp:          uint64(timestamp),
		coinbase:           coinbase,
		parentHash:         parent.Hash(),
		statedb:            state,
		prefetchReader:     prefetchReader,
		processReader:      processReader,
		prefetchedTxHashes: &sync.Map{},
	}

	var interruptPrefetch atomic.Bool
	newBlockNumber := new(big.Int).Add(parent.Number, common.Big1)
	if w.config.EnablePrefetch && w.chainConfig.Bor != nil && w.chainConfig.Bor.IsGiugliano(newBlockNumber) {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("Prefetch goroutine panicked", "err", r, "stack", string(debug.Stack()))
					prefetchPanicMeter.Mark(1)
				}
			}()
			w.prefetchFromPool(parent, throwaway, &genParams, &interruptPrefetch)
			// Goroutine exits naturally after prefetch completes.
			// Go's GC keeps throwaway StateDB alive while this goroutine references it.
			// When the goroutine exits, the reference is released and GC can collect it.
		}()
	}

	w.buildAndCommitBlock(interrupt, noempty, &genParams, &interruptPrefetch)
}

// buildAndCommitBlock prepares work, fills transactions, and commits the block for sealing.
func (w *worker) buildAndCommitBlock(interrupt *atomic.Int32, noempty bool, genParams *generateParams, interruptPrefetch *atomic.Bool) {
	work, err := w.prepareWork(genParams, w.makeWitness)
	if err != nil {
		return
	}

	// Starts accounting time after prepareWork, since it includes the wait we have on Prepare phase of Bor
	start := time.Now()
	interruptPrefetch.Store(true)

	stopFn := func() {}
	defer func() {
		stopFn()
	}()

	if !noempty && w.interruptCommitFlag {
		// Start the timer for block building
		stopFn = createInterruptTimer(
			work.header.Number.Uint64(),
			work.header.GetActualTime(),
			&w.interruptBlockBuilding,
			&w.interruptFlagSetAt,
		)
	}

	// Create an empty block based on temporary copied state for
	// sealing in advance without waiting block execution finished.
	// If the block is a veblop block, we will never try to create a commit for an empty block.
	var isRio bool
	if w.chainConfig.Bor != nil {
		isRio = w.chainConfig.Bor.IsRio(work.header.Number)
	}
	if !noempty && !w.noempty.Load() && !isRio {
		emptyWork := work.copy()
		emptyWork.state.ResetPrefetcher()
		_ = w.commit(emptyWork, nil, false, start, genParams)
	}
	// Mark the start of full-block building. Set after the optional empty pre-seal commit so that
	// productionElapsed for the full block does not include empty-block overhead.
	genParams.productionStart = time.Now()
	// Fill pending transactions from the txpool into the block.
	err = w.fillTransactions(interrupt, work)

	switch {
	case err == nil:
		// The entire block is filled, decrease resubmit interval in case
		// of current interval is larger than the user-specified one.
		w.adjustResubmitInterval(&intervalAdjust{inc: false})

	case errors.Is(err, errBlockInterruptedByRecommit):
		// Notify resubmit loop to increase resubmitting interval if the
		// interruption is due to frequent commits.
		gaslimit := work.header.GasLimit

		ratio := float64(gaslimit-work.gasPool.Gas()) / float64(gaslimit)
		if ratio < 0.1 {
			ratio = 0.1
		}
		w.adjustResubmitInterval(&intervalAdjust{
			ratio: ratio,
			inc:   true,
		})

	case errors.Is(err, errBlockInterruptedByNewHead):
		// If the block building is interrupted by newhead event, discard it
		// totally. Committing the interrupted block introduces unnecessary
		// delay, and possibly causes miner to mine on the previous head,
		// which could result in higher uncle rate.
		work.discard()
		return
	}
	// Submit the generated block for consensus sealing.
	_ = w.commit(work.copy(), w.fullTaskHook, true, start, genParams)

	// Swap out the old work with the new one, terminating any leftover
	// prefetcher processes in the mean time and starting a new one.
	w.currentMu.Lock()
	if w.current != nil {
		w.current.discard()
	}
	w.current = work
	w.currentMu.Unlock()
}

func (w *worker) prefetchFromPool(parent *types.Header, throwaway *state.StateDB, genParams *generateParams, interruptPrefetch *atomic.Bool) {
	const minLoopInterval = 100 * time.Millisecond

	baseFee := eip1559.CalcBaseFee(w.chainConfig, parent)
	number := new(big.Int).Add(parent.Number, common.Big1)
	filter := w.buildDefaultFilter(baseFee, number)
	filter.BlobTxs = false

	// Acquire read lock to safely access w.extra in makeHeader
	w.mu.RLock()
	header, _, err := w.makeHeader(genParams, false)
	w.mu.RUnlock()

	if err != nil {
		return
	}
	signer := types.MakeSigner(w.chainConfig, header.Number, header.Time)
	prefetcher := core.NewStatePrefetcher(w.chainConfig, w.chain.HeaderChain())

	// Initialize total gas pool with configured percentage of header gas limit
	gasLimitPercent := w.config.PrefetchGasLimitPercent
	if gasLimitPercent == 0 {
		gasLimitPercent = 100 // Default to 100% if not configured
	}
	// Defensive cap at 150% to prevent misconfiguration DoS
	if gasLimitPercent > 150 {
		log.Warn("Prefetch gas limit percent exceeds maximum, capping at 150%", "configured", gasLimitPercent)
		gasLimitPercent = 150
	}
	totalGasLimit := header.GasLimit * gasLimitPercent / 100
	totalGasPool := new(core.GasPool).AddGas(totalGasLimit)

	txsAlreadyPrefetched := make(map[common.Hash]struct{})
	loopIteration := 0

	for {
		if interruptPrefetch.Load() {
			return
		}

		// Check if we've exhausted the total gas pool
		if totalGasPool.Gas() == 0 {
			return
		}

		loopStart := time.Now()
		loopIteration++

		// Use the remaining gas from totalGasPool, but cap at header.GasLimit per loop
		remainingGas := totalGasPool.Gas()
		loopGasLimit := header.GasLimit
		if remainingGas < loopGasLimit {
			loopGasLimit = remainingGas
		}
		gaspool := new(core.GasPool).AddGas(loopGasLimit)

		pendingTxs := w.eth.TxPool().Pending(filter, interruptPrefetch)
		txs := newTransactionsByPriceAndNonce(signer, pendingTxs, header.BaseFee, interruptPrefetch)

		transactions := make([]*types.Transaction, 0)
		skippedAlreadyPrefetched := 0
		skippedInsufficientGas := 0
		skippedNilTx := 0

		for {
			ltx, _ := txs.Peek()
			if ltx == nil {
				break
			}
			if gaspool.Gas() < ltx.Gas {
				txs.Pop()
				skippedInsufficientGas++
				continue
			}
			if _, exists := txsAlreadyPrefetched[ltx.Hash]; exists {
				txs.Shift()
				skippedAlreadyPrefetched++
				continue
			}

			tx := ltx.Resolve()
			if tx == nil {
				txs.Pop()
				skippedNilTx++
				continue
			}

			transactions = append(transactions, tx)
			gaspool.SubGas(tx.Gas())
			txs.Shift()
		}

		block := types.NewBlock(header, &types.Body{Transactions: transactions}, nil, trie.NewStackTrie(nil))
		result := prefetcher.Prefetch(block, throwaway, vm.Config{}, true, interruptPrefetch)

		// Use the actual gas used from prefetch result and mark successful transactions
		if result != nil {
			totalGasPool.SubGas(result.TotalGasUsed)
			for _, txHash := range result.SuccessfulTxs {
				txsAlreadyPrefetched[txHash] = struct{}{}
				// Store in shared map for coverage metrics
				if genParams.prefetchedTxHashes != nil {
					genParams.prefetchedTxHashes.Store(txHash, struct{}{})
				}
			}
		}
		// Calculate elapsed time and wait if necessary to ensure minimum 100ms interval
		// Check interrupt flag every 10ms during wait for responsive shutdown
		elapsed := time.Since(loopStart)
		if elapsed < minLoopInterval {
			checkInterval := 10 * time.Millisecond

			for remaining := minLoopInterval - elapsed; remaining > 0; remaining = minLoopInterval - time.Since(loopStart) {
				if interruptPrefetch.Load() {
					return
				}

				sleepDuration := checkInterval
				if remaining < checkInterval {
					sleepDuration = remaining
				}
				time.Sleep(sleepDuration)
			}
		}
	}
}

// createInterruptTimer creates and starts a timer based on the header's timestamp for block building
// and toggles the flag when the timer expires.
func createInterruptTimer(number uint64, actualTimestamp time.Time, interruptBlockBuilding *atomic.Bool, interruptFlagSetAt *atomic.Int64) func() {
	delay := time.Until(actualTimestamp)

	// Reduce the timeout by 500ms to give some buffer for state root computation
	if delay > 1*time.Second {
		delay -= 500 * time.Millisecond
	}

	interruptCtx, cancel := context.WithTimeout(context.Background(), delay)

	// Reset the flag when timer starts for building a new block.
	interruptBlockBuilding.Store(false)
	interruptFlagSetAt.Store(0)

	go func() {
		// Wait for timeout
		<-interruptCtx.Done()

		// Toggle the flag to indicate commit transactions loop and EVM interpreter loop
		// to stop block building.
		if interruptCtx.Err() != context.Canceled {
			interruptFlagSetAt.Store(time.Now().UnixNano())
		}
		interruptBlockBuilding.Store(true)

		if interruptCtx.Err() != context.Canceled {
			cancel()
		}
	}()

	return cancel
}

// commit runs any post-transaction state modifications, assembles the final block
// and commits new work if consensus engine is running.
// Note the assumption is held that the mutation is allowed to the passed env, do
// the deep copy first.
func (w *worker) commit(env *environment, interval func(), update bool, start time.Time, genParams *generateParams) error {
	// Track total block building time and report metrics at the end of the commit cycle.
	defer func() {
		// Update total commit timer (matches the "elapsed" time in log)
		commitTimer.Update(time.Since(start))

		// Report cache hit/miss metrics (matches behavior in blockchain.go for import path)
		if metrics.Enabled() && env.prefetchReader != nil && env.processReader != nil {
			// Report prefetch reader stats
			prefetchStats := env.prefetchReader.GetStats()
			accountCacheHitPrefetchMeter.Mark(prefetchStats.AccountHit)
			accountCacheMissPrefetchMeter.Mark(prefetchStats.AccountMiss)
			storageCacheHitPrefetchMeter.Mark(prefetchStats.StorageHit)
			storageCacheMissPrefetchMeter.Mark(prefetchStats.StorageMiss)

			// Report process reader stats
			processStats := env.processReader.GetStats()
			accountCacheHitMeter.Mark(processStats.AccountHit)
			accountCacheMissMeter.Mark(processStats.AccountMiss)
			storageCacheHitMeter.Mark(processStats.StorageHit)
			storageCacheMissMeter.Mark(processStats.StorageMiss)

			// Report additional prefetch attribution metrics
			prefetchAttribStats := env.prefetchReader.GetPrefetchStats()
			accountInsertPrefetchMeter.Mark(prefetchAttribStats.AccountInsert)
			storageInsertPrefetchMeter.Mark(prefetchAttribStats.StorageInsert)

			processAttribStats := env.processReader.GetPrefetchStats()
			accountHitFromPrefetchMeter.Mark(processAttribStats.AccountHitFromPrefetch)
			storageHitFromPrefetchMeter.Mark(processAttribStats.StorageHitFromPrefetch)
			accountHitFromPrefetchUniqueMeter.Mark(processAttribStats.AccountHitFromPrefetchUnique)

			// Report prefetch coverage percentage
			if len(env.txs) > 0 && genParams != nil && genParams.prefetchedTxHashes != nil {
				prefetchedCount := 0

				// Count how many block transactions were prefetched
				for _, tx := range env.txs {
					if _, ok := genParams.prefetchedTxHashes.Load(tx.Hash()); ok {
						prefetchedCount++
					}
				}

				// Calculate miss rate (0-100): higher = worse
				missRate := int64((len(env.txs) - prefetchedCount) * 100 / len(env.txs))
				prefetchMissRateHistogram.Update(missRate)
			}
		}
	}()

	if w.IsRunning() {
		if interval != nil {
			interval()
		}
		// Create a local environment copy, avoid the data race with snapshot state.
		// https://github.com/ethereum/go-ethereum/issues/24299
		env := env.copy()
		// Withdrawals are set to nil here, because this is only called in PoW.
		var block *types.Block
		var err error

		// Track time for FinalizeAndAssemble (state root calculation + block assembly)
		finalizeStart := time.Now()
		var commitTime time.Duration
		block, env.receipts, commitTime, err = w.engine.FinalizeAndAssemble(w.chain, env.header, env.state, &types.Body{
			Transactions: env.txs,
		}, env.receipts)
		finalizeDuration := time.Since(finalizeStart)
		finalizeAndAssembleTimer.Update(finalizeDuration)
		intermediateRootTimer.Update(commitTime)

		if err != nil {
			return err
		}

		select {
		case w.taskCh <- &task{receipts: env.receipts, state: env.state, block: block, createdAt: time.Now(), productionElapsed: time.Since(firstNonZeroTime(productionStartFrom(genParams), start)), intermediateRootTime: commitTime}:
			fees := totalFees(block, env.receipts)
			feesInEther := new(big.Float).Quo(new(big.Float).SetInt(fees), big.NewFloat(params.Ether))
			log.Info("Commit new sealing work", "number", block.Number(), "sealhash", w.engine.SealHash(block.Header()),
				"txs", env.tcount, "gas", block.GasUsed(), "fees", feesInEther,
				"elapsed", common.PrettyDuration(time.Since(start)), "finalize", common.PrettyDuration(finalizeDuration))

		case <-w.exitCh:
			log.Info("Worker has exited")
		}
	}

	if update {
		w.updateSnapshot(env)
	}

	return nil
}

// getSealingBlock generates the sealing block based on the given parameters.
// The generation result will be passed back via the given channel no matter
// the generation itself succeeds or not.
func (w *worker) getSealingBlock(params *generateParams) *newPayloadResult {
	ctx := tracing.WithTracer(context.Background(), otel.GetTracerProvider().Tracer("getSealingBlock"))

	req := &getWorkReq{
		params: params,
		result: make(chan *newPayloadResult, 1),
		ctx:    ctx,
	}
	select {
	case w.getWorkCh <- req:
		return <-req.result
	case <-w.exitCh:
		return &newPayloadResult{err: errors.New("miner closed")}
	}
}

// adjustResubmitInterval adjusts the resubmit interval.
func (w *worker) adjustResubmitInterval(message *intervalAdjust) {
	select {
	case w.resubmitAdjustCh <- message:
	default:
		log.Warn("the resubmitAdjustCh is full, discard the message")
	}
}

// clearPending cleans the stale pending tasks.
func (w *worker) clearPending(number uint64) {
	w.pendingMu.Lock()
	for h, t := range w.pendingTasks {
		if t.block.NumberU64()+staleThreshold <= number {
			delete(w.pendingTasks, h)
		}
	}
	w.pendingMu.Unlock()
}

// copyReceipts makes a deep copy of the given receipts.
func copyReceipts(receipts []*types.Receipt) []*types.Receipt {
	result := make([]*types.Receipt, len(receipts))

	for i, l := range receipts {
		cpy := *l
		result[i] = &cpy
	}

	return result
}

// totalFees computes total consumed miner fees in Wei. Block transactions and receipts have to have the same order.
func totalFees(block *types.Block, receipts []*types.Receipt) *big.Int {
	feesWei := new(big.Int)

	for i, tx := range block.Transactions() {
		minerFee, _ := tx.EffectiveGasTip(block.BaseFee())
		feesWei.Add(feesWei, new(big.Int).Mul(new(big.Int).SetUint64(receipts[i].GasUsed), minerFee))
	}

	return feesWei
}

// signalToErr converts the interruption signal to a concrete error type for return.
// The given signal must be a valid interruption signal.
func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	case commitInterruptTimeout:
		return errBlockInterruptedByTimeout
	default:
		panic(fmt.Errorf("undefined signal %d", signal))
	}
}
