package legacypool

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	filteredAddrsGauge    = metrics.NewRegisteredGauge("txpool/filtered/addresses", nil)
	filteredReloadMeter   = metrics.NewRegisteredMeter("txpool/filtered/reload", nil)
	filteredReloadedGauge = metrics.NewRegisteredGauge("txpool/filtered/reloaded", nil)
	filteredEvictMeter    = metrics.NewRegisteredMeter("txpool/filtered/evict", nil)
)

var errEmptyList = errors.New("no entries")

// LoadFilteredAddresses reads a newline-separated list of addresses from
// filePath. An empty path yields no list. A file that is missing, has no
// entries or has an invalid entry is an error, so a broken file stops the node
// instead of running it with the filter lifted.
func LoadFilteredAddresses(filePath string) (map[common.Address]struct{}, error) {
	if filePath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read filtered addresses file: %w", err)
	}
	addrs, err := parseFilteredAddresses(data)
	if err != nil {
		return nil, fmt.Errorf("invalid filtered addresses file %s: %w", filePath, err)
	}
	log.Info("Loaded filtered addresses", "count", len(addrs), "file", filePath)

	return addrs, nil
}

func parseFilteredAddresses(data []byte) (map[common.Address]struct{}, error) {
	addrs := make(map[common.Address]struct{})
	for i, line := range strings.Split(string(data), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		if !common.IsHexAddress(entry) {
			return nil, fmt.Errorf("invalid address %q on line %d", entry, i+1)
		}
		addrs[common.HexToAddress(entry)] = struct{}{}
	}
	if len(addrs) == 0 {
		return nil, errEmptyList
	}

	return addrs, nil
}

// filterReloader tracks the previous read of the filtered addresses file, so
// an unchanged file is not parsed again and removals wait for two reads in a
// row to agree.
type filterReloader struct {
	path        string
	last        []byte
	lastAddrs   map[common.Address]struct{} // last, parsed
	sweepNeeded bool
}

func (pool *LegacyPool) filterReloadLoop(r *filterReloader, interval time.Duration) {
	defer pool.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-pool.reorgShutdownCh:
			return
		case <-ticker.C:
			pool.reloadFilteredAddresses(r)
		}
	}
}

// reloadFilteredAddresses applies the file's current contents to the pool. If
// the file can't be read or doesn't parse, the current list stays in force: a
// deleted, emptied or corrupted file must not lift the filter.
func (pool *LegacyPool) reloadFilteredAddresses(r *filterReloader) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		r.last, r.lastAddrs = nil, nil
		log.Warn("Failed to read filtered addresses file, keeping current list", "file", r.path, "err", err)
		return
	}
	addrs := r.lastAddrs
	if addrs == nil || !bytes.Equal(data, r.last) {
		if addrs, err = parseFilteredAddresses(data); err != nil {
			r.last, r.lastAddrs = nil, nil
			log.Warn("Invalid filtered addresses file, keeping current list", "file", r.path, "err", err)
			return
		}
	}

	// A file rewritten in place can be read half-written and still parse.
	// Apply additions now, and removals only once two reads in a row agree.
	settled := r.lastAddrs != nil && maps.Equal(addrs, r.lastAddrs)
	r.last, r.lastAddrs = data, addrs
	changed := pool.setFilteredAddresses(addrs, !settled)

	// An async add validates against the list, drops the pool lock and
	// inserts later, so it can land a transaction the new list rejects just
	// after the sweep that followed the update. Sweep again one interval
	// later to catch it.
	if !changed && r.sweepNeeded {
		pool.sweepFiltered()
	}
	r.sweepNeeded = changed
}

func (pool *LegacyPool) sweepFiltered() {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pool.evictFiltered()
}

// setFilteredAddresses swaps in a new filtered list, kept a superset of the
// current one when keepCurrent is set, and evicts pooled transactions it now
// rejects. It reports whether the list changed.
func (pool *LegacyPool) setFilteredAddresses(addrs map[common.Address]struct{}, keepCurrent bool) bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	if keepCurrent {
		addrs = maps.Clone(addrs)
		maps.Copy(addrs, pool.filteredAddrs)
	}
	if maps.Equal(pool.filteredAddrs, addrs) {
		return false
	}
	previous := len(pool.filteredAddrs)
	pool.filteredAddrs = addrs
	evicted := pool.evictFiltered()

	filteredAddrsGauge.Update(int64(len(addrs)))
	filteredReloadMeter.Mark(1)
	filteredReloadedGauge.Update(time.Now().Unix())
	log.Info("Reloaded filtered addresses", "count", len(addrs), "previous", previous, "evicted", evicted)

	return true
}

// evictFiltered removes every pooled transaction the current list would
// reject at admission. The caller must hold pool.mu.
func (pool *LegacyPool) evictFiltered() int {
	if len(pool.filteredAddrs) == 0 {
		return 0
	}

	// Authorities come from the pool's index: tx.SetCodeAuthorities would
	// re-run ecrecover for every authorization while pool.mu is held.
	drop := make(map[common.Hash]struct{})
	for _, hash := range pool.all.authorizedBy(pool.isFiltered) {
		drop[hash] = struct{}{}
	}
	pool.all.Range(func(hash common.Hash, tx *types.Transaction) bool {
		if from, err := types.Sender(pool.signer, tx); err == nil && pool.isFiltered(from) {
			drop[hash] = struct{}{}
		}
		return true
	})
	for hash := range drop {
		pool.removeTx(hash, false, true)
	}
	pool.priced.Removed(len(drop))
	filteredEvictMeter.Mark(int64(len(drop)))

	return len(drop)
}
