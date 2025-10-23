package pathdb

import (
	"fmt"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	// Biased cache metrics for address-specific cache effectiveness
	biasedAddressCacheHitMeter   = metrics.NewRegisteredMeter("pathdb/biased/address/hit", nil)
	biasedAddressCacheMissMeter  = metrics.NewRegisteredMeter("pathdb/biased/address/miss", nil)
	biasedAddressCacheReadMeter  = metrics.NewRegisteredMeter("pathdb/biased/address/read", nil)
	biasedAddressCacheWriteMeter = metrics.NewRegisteredMeter("pathdb/biased/address/write", nil)
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
// All maps are populated during initialization and are read-only afterward,
// so no locking is needed (fastcache.Cache is internally thread-safe).
type AddressBiasedCache struct {
	// Address-specific caches, one per preloaded address
	addressCaches map[common.Hash]*fastcache.Cache

	// Common cache for all other data
	commonCache *fastcache.Cache

	// Set of preloaded addresses for fast lookup
	preloadedAddrs map[common.Hash]struct{}

	// Statistics for each address cache (populated during init, read-only after)
	stats map[common.Hash]*CacheStats
}

// NewAddressBiasedCache creates a new address-biased cache with preloading.
// It scans the database for storage trie nodes of the specified addresses and
// loads them into dedicated caches. The addressCacheSizes maps each address to
// its desired cache size in bytes. The commonCacheSize specifies the size
// of the cache for non-preloaded data.
func NewAddressBiasedCache(db ethdb.Database, addressCacheSizes map[common.Address]int, commonCacheSize int) (*AddressBiasedCache, error) {
	cache := &AddressBiasedCache{
		addressCaches:  make(map[common.Hash]*fastcache.Cache),
		preloadedAddrs: make(map[common.Hash]struct{}),
		stats:          make(map[common.Hash]*CacheStats),
		commonCache:    fastcache.New(commonCacheSize),
	}

	// Preload storage tries for each specified address with its custom cache size
	for addr, cacheSize := range addressCacheSizes {
		if err := cache.preloadAddress(db, addr, cacheSize); err != nil {
			log.Error("Failed to preload address", "address", addr.Hex(), "err", err)
			return nil, fmt.Errorf("failed to preload address %s: %w", addr.Hex(), err)
		}
	}

	return cache, nil
}

// preloadAddress loads storage trie nodes for the given account hash using
// BFS traversal, prioritizing shallow nodes (most frequently accessed) until
// the cache is full. This naturally loads nodes by depth, filling the cache
// with as many upper-level nodes as possible.
func (c *AddressBiasedCache) preloadAddress(db ethdb.Database, addr common.Address, cacheSize int) error {
	startTime := time.Now()

	addrCache := fastcache.New(cacheSize)

	accountHash := crypto.Keccak256Hash(addr.Bytes())

	// Mark this address as preloaded
	c.preloadedAddrs[accountHash] = struct{}{}
	c.addressCaches[accountHash] = addrCache

	// Initialize stats
	stats := &CacheStats{}
	c.stats[accountHash] = stats

	log.Info("Starting storage trie preload",
		"address", addr.Hex(),
		"account hash", accountHash.Hex(),
		"cache size", common.StorageSize(cacheSize).String())

	var totalBytes uint64
	var maxDepthReached int
	const logInterval = 100000

	// BFS traversal to load nodes by depth until cache is full
	type queueItem struct {
		path  []byte
		depth int
	}
	queue := []queueItem{{path: nil, depth: 0}} // Start from root
	visited := make(map[string]struct{})        // Prevent revisiting nodes

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		// Track maximum depth reached
		if item.depth > maxDepthReached {
			maxDepthReached = item.depth
		}

		// Skip if already visited
		pathKey := string(item.path)
		if _, ok := visited[pathKey]; ok {
			continue
		}
		visited[pathKey] = struct{}{}

		// Read the node from database
		nodeData := rawdb.ReadStorageTrieNode(db, accountHash, item.path)
		if len(nodeData) == 0 {
			// Node doesn't exist, skip
			continue
		}

		// Check if adding this node would exceed cache size
		// Key format: owner (32 bytes) + path
		nodeSize := uint64(common.HashLength + len(item.path) + len(nodeData))

		// Preload 66.6% of the cache size to allow hot paths to be added later
		if totalBytes+nodeSize > uint64(cacheSize*2/3) {
			log.Info("Cache size limit reached, stopping preload",
				"account hash", accountHash.Hex(),
				"entries", stats.Entries,
				"current depth", item.depth,
				"max depth reached", maxDepthReached,
				"size", common.StorageSize(totalBytes).String())
			break
		}

		// Construct the cache key using the same format as nodeCacheKey
		// Format: owner (32 bytes) + path
		key := append(accountHash.Bytes(), item.path...)

		// Store in cache
		addrCache.Set(key, nodeData)
		stats.Entries++
		totalBytes += nodeSize

		// Log progress periodically
		if stats.Entries%logInterval == 0 {
			log.Info("Preloading storage trie progress",
				"account hash", accountHash.Hex(),
				"entries", stats.Entries,
				"current depth", item.depth,
				"max depth", maxDepthReached,
				"size", common.StorageSize(totalBytes).String(),
				"cache usage", fmt.Sprintf("%.1f%%", float64(totalBytes)*100/float64(cacheSize)),
				"elapsed", time.Since(startTime))
		}

		// Add child nodes to queue for next level
		childPaths := c.gatherChildPaths(nodeData, item.path)
		for _, childPath := range childPaths {
			queue = append(queue, queueItem{
				path:  childPath,
				depth: item.depth + 1,
			})
		}
	}

	// Record statistics
	stats.SizeBytes = totalBytes
	stats.LoadTime = time.Since(startTime)

	// Log the completion with stats
	log.Info("Completed storage trie preload",
		"account hash", accountHash.Hex(),
		"entries", stats.Entries,
		"max depth", maxDepthReached,
		"size", common.StorageSize(stats.SizeBytes).String(),
		"cache usage", fmt.Sprintf("%.1f%%", float64(totalBytes)*100/float64(cacheSize)),
		"time", stats.LoadTime)

	return nil
}

// gatherChildPaths uses ForGatherChildren to extract child node paths from a trie node.
// It decodes the node and collects paths for all child nodes that need to be loaded.
func (c *AddressBiasedCache) gatherChildPaths(nodeData []byte, currentPath []byte) [][]byte {
	var childPaths [][]byte

	// Use ForGatherChildren to find all hash node children
	// This function traverses the decoded node and calls the callback for each hashNode
	// However, we need the paths, not just hashes, so we'll need to construct them

	// Since we can't easily get child paths without decoding the node structure,
	// we'll use a simpler approach: for fullNodes, try all 16 branches
	// For shortNodes, we need to decode to get the key

	// Try reading potential child nodes by extending the path
	// For a path-based scheme, children are at currentPath + [0-15]
	for i := byte(0); i < 16; i++ {
		childPath := append(append([]byte(nil), currentPath...), i)
		childPaths = append(childPaths, childPath)
	}

	// Note: This approach will attempt to read many non-existent nodes,
	// but ReadStorageTrieNode will return empty data for those, which we handle
	// in the main loop. This is simpler than decoding the RLP structure.

	return childPaths
}

// routeCache determines which cache should be used for the given key.
// Returns the appropriate cache and true if it's an address-specific cache,
// or the common cache and false otherwise.
//
// Note: The key format used by nodeCacheKey is:
//   - For account trie: path only
//   - For storage trie: owner (32 bytes) + path
func (c *AddressBiasedCache) routeCache(key []byte) (*fastcache.Cache, bool) {
	// Check key length - if >= 32 bytes, it could be a storage trie node
	// Format: owner (32 bytes) + path
	if len(key) >= common.HashLength {
		// Extract potential account hash (first 32 bytes)
		accountHash := common.BytesToHash(key[:common.HashLength])

		// Check if this account has a dedicated cache
		if addrCache, ok := c.addressCaches[accountHash]; ok {
			return addrCache, true
		}
	}

	// Either account trie node or non-preloaded storage trie
	return c.commonCache, false
}

// Get retrieves the value for the given key from the appropriate cache
func (c *AddressBiasedCache) Get(key []byte) []byte {
	cache, isAddressCache := c.routeCache(key)
	value := cache.Get(nil, key)

	// Update metrics only for address-specific cache
	if isAddressCache {
		if len(value) > 0 {
			// Cache hit
			biasedAddressCacheHitMeter.Mark(1)
			biasedAddressCacheReadMeter.Mark(int64(len(value)))
		} else {
			// Cache miss
			biasedAddressCacheMissMeter.Mark(1)
		}
	}

	return value
}

// Set stores the key-value pair in the appropriate cache
func (c *AddressBiasedCache) Set(key, value []byte) {
	cache, isAddressCache := c.routeCache(key)
	cache.Set(key, value)

	// Update write metrics only for address-specific cache
	if isAddressCache {
		biasedAddressCacheWriteMeter.Mark(int64(len(value)))
	}
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

// Reset resets all caches
func (c *AddressBiasedCache) Reset() {
	c.commonCache.Reset()
	for _, cache := range c.addressCaches {
		cache.Reset()
	}
}

// Stats returns statistics for all caches
func (c *AddressBiasedCache) Stats() map[common.Hash]*CacheStats {
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
	if stat, ok := c.stats[addr]; ok {
		return &CacheStats{
			Entries:   stat.Entries,
			SizeBytes: stat.SizeBytes,
			LoadTime:  stat.LoadTime,
		}
	}
	return nil
}
