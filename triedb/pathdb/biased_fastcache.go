package pathdb

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// addressCacheSize is the fixed size for each address-specific cache (1GB)
	addressCacheSize = 1024 * 1024 * 1024
)

// CacheStats contains statistics about a cache instance
type CacheStats struct {
	Entries   int           // Number of entries loaded
	SizeBytes uint64        // Total size of data in bytes
	LoadTime  time.Duration // Time taken to load the cache
}

// AddressBiasedCache is a wrapper around fastcache that maintains separate
// caches for specific addresses and a common cache for everything else.
// It preloads storage trie nodes for specified addresses into dedicated caches.
type AddressBiasedCache struct {
	// Address-specific caches, one per preloaded address (1GB each)
	addressCaches map[common.Hash]*fastcache.Cache

	// Common cache for all other data
	commonCache *fastcache.Cache

	// Set of preloaded addresses for fast lookup
	preloadedAddrs map[common.Hash]struct{}

	// Statistics for each address cache
	stats map[common.Hash]*CacheStats

	// Mutex for thread-safe access
	lock sync.RWMutex
}

// NewAddressBiasedCache creates a new address-biased cache with preloading.
// It scans the database for storage trie nodes of the specified addresses and
// loads them into dedicated caches. The commonCacheSize specifies the size
// of the cache for non-preloaded data.
func NewAddressBiasedCache(db ethdb.Database, addresses []common.Hash, commonCacheSize int) (*AddressBiasedCache, error) {
	cache := &AddressBiasedCache{
		addressCaches:  make(map[common.Hash]*fastcache.Cache),
		preloadedAddrs: make(map[common.Hash]struct{}),
		stats:          make(map[common.Hash]*CacheStats),
		commonCache:    fastcache.New(commonCacheSize),
	}

	// Preload storage tries for each specified address
	for _, addr := range addresses {
		if err := cache.preloadAddress(db, addr); err != nil {
			log.Error("Failed to preload address", "address", addr.Hex(), "err", err)
			return nil, fmt.Errorf("failed to preload address %s: %w", addr.Hex(), err)
		}
	}

	return cache, nil
}

// preloadAddress scans the database for all storage trie nodes under the given
// account hash and loads them into a dedicated cache.
func (c *AddressBiasedCache) preloadAddress(db ethdb.Database, accountHash common.Hash) error {
	startTime := time.Now()

	// Create a new 1GB cache for this address
	addrCache := fastcache.New(addressCacheSize)

	// Mark this address as preloaded
	c.preloadedAddrs[accountHash] = struct{}{}
	c.addressCaches[accountHash] = addrCache

	// Initialize stats
	stats := &CacheStats{}
	c.stats[accountHash] = stats

	// Construct the prefix for this account's storage trie nodes
	// Format: TrieNodeStoragePrefix + accountHash
	prefix := rawdb.TrieNodeStoragePrefix
	keyPrefix := append(prefix, accountHash.Bytes()...)

	// Create an iterator for all keys with this prefix
	iter := db.NewIterator(keyPrefix, nil)
	defer iter.Release()

	// Iterate over all storage trie nodes for this account
	var totalBytes uint64
	const logInterval = 100000 // Log progress every 100k entries

	for iter.Next() {
		key := iter.Key()
		value := iter.Value()

		// Store in the address-specific cache
		addrCache.Set(key, value)

		stats.Entries++
		totalBytes += uint64(len(key) + len(value))

		// Log progress at regular intervals
		if stats.Entries%logInterval == 0 {
			log.Info("Preloading storage trie progress",
				"account hash", accountHash.Hex(),
				"entries", stats.Entries,
				"size", common.StorageSize(totalBytes).String(),
				"elapsed", time.Since(startTime))
		}

		// Stop loading if we've hit the cache size limit
		// This prevents unnecessary iteration and cache evictions
		if totalBytes >= addressCacheSize {
			log.Info("Cache size limit reached during preload, stopping early",
				"address", accountHash.Hex(),
				"entries", stats.Entries,
				"size", common.StorageSize(totalBytes).String())
			break
		}
	}

	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterator error: %w", err)
	}

	// Record statistics
	stats.SizeBytes = totalBytes
	stats.LoadTime = time.Since(startTime)

	// Log the completion with stats
	log.Info("Preloaded storage trie",
		"address", accountHash.Hex(),
		"entries", stats.Entries,
		"size", common.StorageSize(stats.SizeBytes).String(),
		"time", stats.LoadTime)

	return nil
}

// isPreloadedAddress checks if the given account hash is in the preloaded set
func (c *AddressBiasedCache) isPreloadedAddress(accountHash common.Hash) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	_, exists := c.preloadedAddrs[accountHash]
	return exists
}

// routeCache determines which cache should be used for the given key.
// Returns the appropriate cache and true if it's an address-specific cache,
// or the common cache and false otherwise.
func (c *AddressBiasedCache) routeCache(key []byte) (*fastcache.Cache, bool) {
	// Check if this is a storage trie node key
	if !bytes.HasPrefix(key, rawdb.TrieNodeStoragePrefix) {
		return c.commonCache, false
	}

	// Extract the account hash from the key
	ok, accountHash, _ := rawdb.ResolveStorageTrieNode(key)
	if !ok {
		return c.commonCache, false
	}

	// Check if this account has a dedicated cache
	if c.isPreloadedAddress(accountHash) {
		c.lock.RLock()
		addrCache := c.addressCaches[accountHash]
		c.lock.RUnlock()
		return addrCache, true
	}

	return c.commonCache, false
}

// Get retrieves the value for the given key from the appropriate cache
func (c *AddressBiasedCache) Get(key []byte) []byte {
	cache, _ := c.routeCache(key)
	return cache.Get(nil, key)
}

// Set stores the key-value pair in the appropriate cache
func (c *AddressBiasedCache) Set(key, value []byte) {
	cache, _ := c.routeCache(key)
	cache.Set(key, value)
}

// Has checks if the key exists in the appropriate cache
func (c *AddressBiasedCache) Has(key []byte) bool {
	cache, _ := c.routeCache(key)
	return cache.Has(key)
}

// Del removes the key from the appropriate cache
func (c *AddressBiasedCache) Del(key []byte) {
	cache, _ := c.routeCache(key)
	cache.Del(key)
}

// Reset resets the cache statistics
func (c *AddressBiasedCache) Reset() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.commonCache.Reset()
	for _, cache := range c.addressCaches {
		cache.Reset()
	}
}

// Stats returns statistics for all caches
func (c *AddressBiasedCache) Stats() map[common.Hash]*CacheStats {
	c.lock.RLock()
	defer c.lock.RUnlock()

	// Return a copy of the stats map
	result := make(map[common.Hash]*CacheStats)
	for addr, stat := range c.stats {
		result[addr] = &CacheStats{
			Entries:   stat.Entries,
			SizeBytes: stat.SizeBytes,
			LoadTime:  stat.LoadTime,
		}
	}

	return result
}

// GetStats returns the stats for a specific address cache, or nil if not found
func (c *AddressBiasedCache) GetStats(addr common.Hash) *CacheStats {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if stat, ok := c.stats[addr]; ok {
		return &CacheStats{
			Entries:   stat.Entries,
			SizeBytes: stat.SizeBytes,
			LoadTime:  stat.LoadTime,
		}
	}
	return nil
}
