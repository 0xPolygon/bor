// Copyright 2023 The go-ethereum Authors
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

// Package locals implements tracking for "local" transactions
package locals

import (
	"slices"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

var (
	recheckInterval = time.Minute
	localGauge      = metrics.GetOrRegisterGauge("txpool/local", nil)
)

// TxTracker is a struct used to track priority transactions; it will check from
// time to time if the main pool has forgotten about any of the transaction
// it is tracking, and if so, submit it again.
// This is used to track 'locals'.
// This struct does not care about transaction validity, price-bumps or account limits,
// but optimistically accepts transactions.
type TxTracker struct {
	all    map[common.Hash]*types.Transaction       // All tracked transactions
	byAddr map[common.Address]*legacypool.SortedMap // Transactions by address

	journal   *journal      // Journal of local transaction to back up to disk
	rejournal time.Duration // How often to rotate journal
	pool      trackedTxPool // The tx pool to interact with
	signer    types.Signer

	shutdownCh chan struct{}
	mu         sync.Mutex
	wg         sync.WaitGroup
}

type trackedTxPool interface {
	Add([]*types.Transaction, bool) []error
	Has(common.Hash) bool
	Nonce(common.Address) uint64
}

type recheckResult struct {
	nonces  map[common.Address]uint64
	missing []*types.Transaction
	numOk   int
}

// New creates a new TxTracker
func New(journalPath string, journalTime time.Duration, chainConfig *params.ChainConfig, next *txpool.TxPool) *TxTracker {
	pool := &TxTracker{
		all:        make(map[common.Hash]*types.Transaction),
		byAddr:     make(map[common.Address]*legacypool.SortedMap),
		signer:     types.LatestSigner(chainConfig),
		shutdownCh: make(chan struct{}),
		pool:       next,
	}
	if journalPath != "" {
		pool.journal = newTxJournal(journalPath)
		pool.rejournal = journalTime
	}
	return pool
}

// Track adds a transaction to the tracked set.
// Note: blob-type transactions are ignored.
func (tracker *TxTracker) Track(tx *types.Transaction) {
	tracker.TrackAll([]*types.Transaction{tx})
}

// TrackAll adds a list of transactions to the tracked set.
// Note: blob-type transactions are ignored.
func (tracker *TxTracker) TrackAll(txs []*types.Transaction) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	for _, tx := range txs {
		if tx.Type() == types.BlobTxType {
			continue
		}
		// If we're already tracking it, it's a no-op
		if _, ok := tracker.all[tx.Hash()]; ok {
			continue
		}
		// Theoretically, checking the error here is unnecessary since sender recovery
		// is already part of basic validation. However, retrieving the sender address
		// from the transaction cache is effectively a no-op if it was previously verified.
		// Therefore, the error is still checked just in case.
		addr, err := types.Sender(tracker.signer, tx)
		if err != nil {
			continue
		}
		tracker.all[tx.Hash()] = tx
		if tracker.byAddr[addr] == nil {
			tracker.byAddr[addr] = legacypool.NewSortedMap()
		}
		tracker.byAddr[addr].Put(tx)

		if tracker.journal != nil {
			_ = tracker.journal.insert(tx)
		}
	}
	localGauge.Update(int64(len(tracker.all)))
}

// recheck checks and returns any transactions that needs to be resubmitted.
func (tracker *TxTracker) recheck(journalCheck bool) []*types.Transaction {
	snapshot := tracker.snapshot()
	result := tracker.inspect(snapshot)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	numStales := 0
	for sender, nonce := range result.nonces {
		txs := tracker.byAddr[sender]
		stales := txs.Forward(nonce)
		for _, tx := range stales {
			delete(tracker.all, tx.Hash())
		}
		numStales += len(stales)
	}
	resubmits := make([]*types.Transaction, 0, len(result.missing))
	for _, tx := range result.missing {
		// A transaction can be removed and tracked again while the pool reads run.
		// Only act on the same tracking generation represented by the snapshot.
		if current, ok := tracker.all[tx.Hash()]; ok && current == tx {
			resubmits = append(resubmits, tx)
		}
	}

	if journalCheck { // rejournal
		rejournal := make(map[common.Address]types.Transactions)
		for _, tx := range tracker.all {
			addr, _ := types.Sender(tracker.signer, tx)
			rejournal[addr] = append(rejournal[addr], tx)
		}
		// Sort them
		for _, list := range rejournal {
			// cmp(a, b) should return a negative number when a < b,
			slices.SortFunc(list, func(a, b *types.Transaction) int {
				return int(a.Nonce() - b.Nonce())
			})
		}
		// Rejournal the tracker while holding the lock. No new transactions will
		// be added to the old journal during this period, preventing any potential
		// transaction loss.
		if tracker.journal != nil {
			if err := tracker.journal.rotate(rejournal); err != nil {
				log.Warn("Transaction journal rotation failed", "err", err)
			}
		}
	}
	localGauge.Update(int64(len(tracker.all)))
	log.Debug("Tx tracker status", "need-resubmit", len(resubmits), "stale", numStales, "ok", result.numOk)
	return resubmits
}

func (tracker *TxTracker) snapshot() map[common.Address]types.Transactions {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	snapshot := make(map[common.Address]types.Transactions, len(tracker.byAddr))
	for sender, txs := range tracker.byAddr {
		snapshot[sender] = txs.Flatten()
	}
	return snapshot
}

func (tracker *TxTracker) inspect(snapshot map[common.Address]types.Transactions) recheckResult {
	result := recheckResult{nonces: make(map[common.Address]uint64, len(snapshot))}
	for sender, txs := range snapshot {
		nonce := tracker.pool.Nonce(sender)
		result.nonces[sender] = nonce
		for _, tx := range txs {
			if tx.Nonce() < nonce {
				continue
			}
			if tracker.pool.Has(tx.Hash()) {
				result.numOk++
			} else {
				result.missing = append(result.missing, tx)
			}
		}
	}
	return result
}

// Start implements node.Lifecycle interface
// Start is called after all services have been constructed and the networking
// layer was also initialized to spawn any goroutines required by the service.
func (tracker *TxTracker) Start() error {
	tracker.wg.Add(1)
	go tracker.loop()
	return nil
}

// Stop implements node.Lifecycle interface
// Stop terminates all goroutines belonging to the service, blocking until they
// are all terminated.
func (tracker *TxTracker) Stop() error {
	close(tracker.shutdownCh)
	tracker.wg.Wait()
	return nil
}

func (tracker *TxTracker) loop() {
	defer tracker.wg.Done()

	if tracker.journal != nil {
		tracker.journal.load(func(transactions []*types.Transaction) []error {
			tracker.TrackAll(transactions)
			return nil
		})

		// Setup the writer for the upcoming transactions
		if err := tracker.journal.setupWriter(); err != nil {
			log.Error("Failed to setup the journal writer", "err", err)
			return
		}
		defer tracker.journal.close()
	}
	var (
		lastJournal = time.Now()
		timer       = time.NewTimer(10 * time.Second) // Do initial check after 10 seconds, do rechecks more seldom.
	)
	for {
		select {
		case <-tracker.shutdownCh:
			return
		case <-timer.C:
			var rejournal bool
			if tracker.journal != nil && time.Since(lastJournal) > tracker.rejournal {
				rejournal, lastJournal = true, time.Now()
				log.Debug("Rejournal the transaction tracker")
			}
			resubmits := tracker.recheck(rejournal)
			if len(resubmits) > 0 {
				tracker.pool.Add(resubmits, false)
			}
			timer.Reset(recheckInterval)
		}
	}
}
