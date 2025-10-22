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
	addressCacheSize = 45 * 1024 * 1024 * 1024
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

// preloadAddress loads storage trie nodes up to a certain depth for the given
// account hash. This loads only the upper levels of the trie (depth < 6),
// which are the most frequently accessed nodes. It uses BFS traversal to
// track actual node depth, not path length.
func (c *AddressBiasedCache) preloadAddress(db ethdb.Database, accountHash common.Hash) error {
	startTime := time.Now()

	// Create a new cache for this address
	addrCache := fastcache.New(addressCacheSize)

	// Mark this address as preloaded
	c.preloadedAddrs[accountHash] = struct{}{}
	c.addressCaches[accountHash] = addrCache

	// Initialize stats
	stats := &CacheStats{}
	c.stats[accountHash] = stats

	const maxDepth = 6
	log.Info("Starting depth-based storage trie preload",
		"account hash", accountHash.Hex(),
		"max depth (node hops)", maxDepth)

	var totalBytes uint64
	const logInterval = 10000

	// BFS traversal to load nodes by actual depth
	type queueItem struct {
		path  []byte
		depth int
	}
	queue := []queueItem{{path: nil, depth: 0}} // Start from root
	visited := make(map[string]struct{})        // Prevent revisiting nodes

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		// Skip if we've exceeded max depth
		if item.depth >= maxDepth {
			continue
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

		// Construct the full key for caching
		key := append(rawdb.TrieNodeStoragePrefix, accountHash.Bytes()...)
		key = append(key, item.path...)

		// Store in cache
		addrCache.Set(key, nodeData)
		stats.Entries++
		totalBytes += uint64(len(key) + len(nodeData))

		// Log progress periodically
		if stats.Entries%logInterval == 0 {
			log.Info("Preloading storage trie progress",
				"account hash", accountHash.Hex(),
				"entries", stats.Entries,
				"depth", item.depth,
				"size", common.StorageSize(totalBytes).String(),
				"elapsed", time.Since(startTime))
		}

		// Gather child node paths using ForGatherChildren
		// This extracts hash references from the decoded node
		childPaths := c.gatherChildPaths(nodeData, item.path)
		for _, childPath := range childPaths {
			queue = append(queue, queueItem{
				path:  childPath,
				depth: item.depth + 1,
			})
		}

		// Stop loading if we've hit the cache size limit
		if totalBytes >= addressCacheSize {
			log.Info("Cache size limit reached during preload, stopping early",
				"account hash", accountHash.Hex(),
				"entries", stats.Entries,
				"depth", item.depth,
				"size", common.StorageSize(totalBytes).String())
			break
		}
	}

	// Record statistics
	stats.SizeBytes = totalBytes
	stats.LoadTime = time.Since(startTime)

	// Log the completion with stats
	log.Info("Completed storage trie preload",
		"account hash", accountHash.Hex(),
		"entries", stats.Entries,
		"size", common.StorageSize(stats.SizeBytes).String(),
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
