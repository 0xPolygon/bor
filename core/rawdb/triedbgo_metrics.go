// Copyright 2024 The go-ethereum Authors
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

package rawdb

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"

	tdb "github.com/cffls/triedb-go/triedb-go"
)

// TrieDB-go Prometheus metrics
var (
	// Read metrics
	triedbgoAccountReadTimer = metrics.NewRegisteredResettingTimer("triedbgo/account/read", nil)
	triedbgoStorageReadTimer = metrics.NewRegisteredResettingTimer("triedbgo/storage/read", nil)

	// Write metrics
	triedbgoAccountWriteTimer = metrics.NewRegisteredResettingTimer("triedbgo/account/write", nil)
	triedbgoStorageWriteTimer = metrics.NewRegisteredResettingTimer("triedbgo/storage/write", nil)

	// Commit metrics
	triedbgoCommitTimer = metrics.NewRegisteredResettingTimer("triedbgo/commit", nil)

	// Root computation metrics
	triedbgoRootComputeTimer = metrics.NewRegisteredResettingTimer("triedbgo/root/compute", nil)

	// Overlay metrics
	triedbgoOverlayInsertTimer = metrics.NewRegisteredResettingTimer("triedbgo/overlay/insert", nil)
	triedbgoOverlayFreezeTimer = metrics.NewRegisteredResettingTimer("triedbgo/overlay/freeze", nil)

	// Operation counters (cumulative totals from Rust)
	// Note: Named differently from timers to avoid collision with ResettingTimer's auto-generated _count
	triedbgoAccountReadCounter   = metrics.NewRegisteredCounter("triedbgo/ops/account/reads", nil)
	triedbgoStorageReadCounter   = metrics.NewRegisteredCounter("triedbgo/ops/storage/reads", nil)
	triedbgoAccountWriteCounter  = metrics.NewRegisteredCounter("triedbgo/ops/account/writes", nil)
	triedbgoStorageWriteCounter  = metrics.NewRegisteredCounter("triedbgo/ops/storage/writes", nil)
	triedbgoCommitCounter        = metrics.NewRegisteredCounter("triedbgo/ops/commits", nil)
	triedbgoRootComputeCounter   = metrics.NewRegisteredCounter("triedbgo/ops/root/computes", nil)
	triedbgoOverlayInsertCounter = metrics.NewRegisteredCounter("triedbgo/ops/overlay/inserts", nil)
	triedbgoOverlayFreezeCounter = metrics.NewRegisteredCounter("triedbgo/ops/overlay/freezes", nil)

	// Gauge metrics for average latency (these don't reset between scrapes)
	// These show the average latency from the most recent collection interval
	triedbgoAccountReadAvgGauge   = metrics.NewRegisteredGauge("triedbgo/avg/account/read/ns", nil)
	triedbgoStorageReadAvgGauge   = metrics.NewRegisteredGauge("triedbgo/avg/storage/read/ns", nil)
	triedbgoCommitAvgGauge        = metrics.NewRegisteredGauge("triedbgo/avg/commit/ns", nil)
	triedbgoRootComputeAvgGauge   = metrics.NewRegisteredGauge("triedbgo/avg/root/compute/ns", nil)
)

// triedbgoMetricsCollector handles periodic collection of triedb-go metrics
type triedbgoMetricsCollector struct {
	mu       sync.Mutex
	running  bool
	stopChan chan struct{}

	// Previous values for delta calculation
	prevAccountReadCount   int64
	prevStorageReadCount   int64
	prevAccountWriteCount  int64
	prevStorageWriteCount  int64
	prevCommitCount        int64
	prevRootComputeCount   int64
	prevOverlayInsertCount int64
	prevOverlayFreezeCount int64

	prevAccountReadNs   int64
	prevStorageReadNs   int64
	prevAccountWriteNs  int64
	prevStorageWriteNs  int64
	prevCommitNs        int64
	prevRootComputeNs   int64
	prevOverlayInsertNs int64
	prevOverlayFreezeNs int64
}

var globalTrieDBGoMetricsCollector = &triedbgoMetricsCollector{}

// StartTrieDBGoMetricsCollection starts periodic collection of triedb-go metrics
func StartTrieDBGoMetricsCollection(interval time.Duration) {
	globalTrieDBGoMetricsCollector.Start(interval)
}

// StopTrieDBGoMetricsCollection stops the metrics collection
func StopTrieDBGoMetricsCollection() {
	globalTrieDBGoMetricsCollector.Stop()
}

// CollectTrieDBGoMetrics collects metrics once (can be called manually)
func CollectTrieDBGoMetrics() {
	globalTrieDBGoMetricsCollector.Collect()
}

func (c *triedbgoMetricsCollector) Start(interval time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return
	}

	c.running = true
	c.stopChan = make(chan struct{})

	// Reset triedb-go metrics at start
	tdb.ResetMetrics()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.Collect()
			case <-c.stopChan:
				return
			}
		}
	}()

	log.Info("Started triedb-go metrics collection", "interval", interval)
}

func (c *triedbgoMetricsCollector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return
	}

	close(c.stopChan)
	c.running = false
	log.Info("Stopped triedb-go metrics collection")
}

func (c *triedbgoMetricsCollector) Collect() {
	m, err := tdb.GetMetrics()
	if err != nil {
		log.Debug("Failed to get triedb-go metrics", "err", err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Calculate deltas for timing metrics
	accountReadCountDelta := int64(m.AccountReadCount) - c.prevAccountReadCount
	storageReadCountDelta := int64(m.StorageReadCount) - c.prevStorageReadCount
	accountWriteCountDelta := int64(m.AccountWriteCount) - c.prevAccountWriteCount
	storageWriteCountDelta := int64(m.StorageWriteCount) - c.prevStorageWriteCount
	commitCountDelta := int64(m.CommitCount) - c.prevCommitCount
	rootComputeCountDelta := int64(m.RootComputeCount) - c.prevRootComputeCount
	overlayInsertCountDelta := int64(m.OverlayInsertCount) - c.prevOverlayInsertCount
	overlayFreezeCountDelta := int64(m.OverlayFreezeCount) - c.prevOverlayFreezeCount

	accountReadNsDelta := int64(m.AccountReadNs) - c.prevAccountReadNs
	storageReadNsDelta := int64(m.StorageReadNs) - c.prevStorageReadNs
	accountWriteNsDelta := int64(m.AccountWriteNs) - c.prevAccountWriteNs
	storageWriteNsDelta := int64(m.StorageWriteNs) - c.prevStorageWriteNs
	commitNsDelta := int64(m.CommitNs) - c.prevCommitNs
	rootComputeNsDelta := int64(m.RootComputeNs) - c.prevRootComputeNs
	overlayInsertNsDelta := int64(m.OverlayInsertNs) - c.prevOverlayInsertNs
	overlayFreezeNsDelta := int64(m.OverlayFreezeNs) - c.prevOverlayFreezeNs

	// Update timers and gauges with average time per operation in this period
	if accountReadCountDelta > 0 {
		avgNs := accountReadNsDelta / accountReadCountDelta
		triedbgoAccountReadTimer.Update(time.Duration(avgNs))
		triedbgoAccountReadCounter.Inc(accountReadCountDelta)
		triedbgoAccountReadAvgGauge.Update(avgNs) // Gauge persists between scrapes
	}
	if storageReadCountDelta > 0 {
		avgNs := storageReadNsDelta / storageReadCountDelta
		triedbgoStorageReadTimer.Update(time.Duration(avgNs))
		triedbgoStorageReadCounter.Inc(storageReadCountDelta)
		triedbgoStorageReadAvgGauge.Update(avgNs) // Gauge persists between scrapes
	}
	if accountWriteCountDelta > 0 {
		avgNs := accountWriteNsDelta / accountWriteCountDelta
		triedbgoAccountWriteTimer.Update(time.Duration(avgNs))
		triedbgoAccountWriteCounter.Inc(accountWriteCountDelta)
	}
	if storageWriteCountDelta > 0 {
		avgNs := storageWriteNsDelta / storageWriteCountDelta
		triedbgoStorageWriteTimer.Update(time.Duration(avgNs))
		triedbgoStorageWriteCounter.Inc(storageWriteCountDelta)
	}
	if commitCountDelta > 0 {
		avgNs := commitNsDelta / commitCountDelta
		triedbgoCommitTimer.Update(time.Duration(avgNs))
		triedbgoCommitCounter.Inc(commitCountDelta)
		triedbgoCommitAvgGauge.Update(avgNs) // Gauge persists between scrapes
	}
	if rootComputeCountDelta > 0 {
		avgNs := rootComputeNsDelta / rootComputeCountDelta
		triedbgoRootComputeTimer.Update(time.Duration(avgNs))
		triedbgoRootComputeCounter.Inc(rootComputeCountDelta)
		triedbgoRootComputeAvgGauge.Update(avgNs) // Gauge persists between scrapes
	}
	if overlayInsertCountDelta > 0 {
		avgNs := overlayInsertNsDelta / overlayInsertCountDelta
		triedbgoOverlayInsertTimer.Update(time.Duration(avgNs))
		triedbgoOverlayInsertCounter.Inc(overlayInsertCountDelta)
	}
	if overlayFreezeCountDelta > 0 {
		avgNs := overlayFreezeNsDelta / overlayFreezeCountDelta
		triedbgoOverlayFreezeTimer.Update(time.Duration(avgNs))
		triedbgoOverlayFreezeCounter.Inc(overlayFreezeCountDelta)
	}

	// Update previous values
	c.prevAccountReadCount = int64(m.AccountReadCount)
	c.prevStorageReadCount = int64(m.StorageReadCount)
	c.prevAccountWriteCount = int64(m.AccountWriteCount)
	c.prevStorageWriteCount = int64(m.StorageWriteCount)
	c.prevCommitCount = int64(m.CommitCount)
	c.prevRootComputeCount = int64(m.RootComputeCount)
	c.prevOverlayInsertCount = int64(m.OverlayInsertCount)
	c.prevOverlayFreezeCount = int64(m.OverlayFreezeCount)

	c.prevAccountReadNs = int64(m.AccountReadNs)
	c.prevStorageReadNs = int64(m.StorageReadNs)
	c.prevAccountWriteNs = int64(m.AccountWriteNs)
	c.prevStorageWriteNs = int64(m.StorageWriteNs)
	c.prevCommitNs = int64(m.CommitNs)
	c.prevRootComputeNs = int64(m.RootComputeNs)
	c.prevOverlayInsertNs = int64(m.OverlayInsertNs)
	c.prevOverlayFreezeNs = int64(m.OverlayFreezeNs)
}
