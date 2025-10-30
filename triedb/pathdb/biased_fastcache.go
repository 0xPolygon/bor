package pathdb

import (
	stdcontext "context"
	"fmt"
	"sync"
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

// AddressBiasedCache is a wrapper around fastcache that maintains separate
// caches for specific addresses and a common cache for everything else.
// It preloads storage trie nodes for specified addresses into dedicated caches.
type AddressBiasedCache struct {
	// Address-specific caches, one per preloaded address
	addressCaches sync.Map // map[common.Hash]*fastcache.Cache

	// Common cache for all other data
	commonCache *fastcache.Cache

	// Set of preloaded addresses for fast lookup
	preloadedAddrs sync.Map // map[common.Hash]struct{}

	// RW mutex to protect cache operations and prevent race conditions
	// between async preloading and concurrent reads/writes
	mu sync.RWMutex

	// Context for canceling preload operations
	ctx    stdcontext.Context
	cancel stdcontext.CancelFunc
	wg     sync.WaitGroup // Wait for all preloads to finish
}

// NewAddressBiasedCache creates a new address-biased cache with preloading.
// It scans the database for storage trie nodes of the specified addresses and
// loads them into dedicated caches. The addressCacheSizes maps each address to
// its desired cache size in bytes. The commonCacheSize specifies the size
// of the cache for non-preloaded data.
// Preloading happens asynchronously in the background.
func NewAddressBiasedCache(db ethdb.Database, addressCacheSizes map[common.Address]int, commonCacheSize int) (*AddressBiasedCache, error) {
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	cache := &AddressBiasedCache{
		commonCache: fastcache.New(commonCacheSize),
		ctx:         ctx,
		cancel:      cancel,
	}

	// Initialize caches synchronously, but preload asynchronously
	for addr, cacheSize := range addressCacheSizes {
		cache.initAddressCache(addr, cacheSize)

		// Start async preloading
		cache.wg.Add(1)
		go cache.preloadAddressAsync(db, addr, cacheSize)
	}

	return cache, nil
}

// initAddressCache initializes the cache structures for an address synchronously
func (c *AddressBiasedCache) initAddressCache(addr common.Address, cacheSize int) {
	accountHash := crypto.Keccak256Hash(addr.Bytes())
	addrCache := fastcache.New(cacheSize)

	// Mark this address as preloaded
	c.preloadedAddrs.Store(accountHash, struct{}{})
	c.addressCaches.Store(accountHash, addrCache)
}

// preloadAddressAsync loads storage trie nodes for the given account hash using
// BFS traversal, prioritizing shallow nodes (most frequently accessed) until
// the cache is full. This naturally loads nodes by depth, filling the cache
// with as many upper-level nodes as possible. This function runs asynchronously.
func (c *AddressBiasedCache) preloadAddressAsync(db ethdb.Database, addr common.Address, cacheSize int) {
	defer c.wg.Done()
	startTime := time.Now()

	accountHash := crypto.Keccak256Hash(addr.Bytes())

	// Get the address cache
	cacheValue, ok := c.addressCaches.Load(accountHash)
	if !ok {
		log.Error("Address cache not found during preload", "address", addr.Hex())
		return
	}
	addrCache := cacheValue.(*fastcache.Cache)

	// Local stats for logging progress
	var entriesLoaded int
	var totalBytesLoaded uint64

	log.Info("Starting storage trie preload",
		"address", addr.Hex(),
		"account hash", accountHash.Hex(),
		"cache size", common.StorageSize(cacheSize).String())

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
		// Check for shutdown signal periodically
		select {
		case <-c.ctx.Done():
			log.Info("Preload interrupted by shutdown",
				"account hash", accountHash.Hex(),
				"entries", entriesLoaded,
				"max depth", maxDepthReached,
				"size", common.StorageSize(totalBytesLoaded).String(),
				"elapsed", time.Since(startTime))
			return
		default:
		}

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
		if totalBytesLoaded+nodeSize > uint64(cacheSize*2/3) {
			log.Info("Cache size limit reached, stopping preload",
				"account hash", accountHash.Hex(),
				"entries", entriesLoaded,
				"current depth", item.depth,
				"max depth reached", maxDepthReached,
				"size", common.StorageSize(totalBytesLoaded).String())
			break
		}

		// Construct the cache key using the same format as nodeCacheKey
		// Format: owner (32 bytes) + path
		key := append(accountHash.Bytes(), item.path...)

		// Atomically check-and-set with mutex protection to prevent race conditions.
		// We must hold the lock across both the check and the set to guarantee that
		// no concurrent write from the main execution path can occur between them.
		c.mu.Lock()
		if addrCache.Has(key) {
			// Key already exists, skip to avoid overwriting potentially newer data
			c.mu.Unlock()
			continue
		}

		// Store in cache while holding the lock
		addrCache.Set(key, nodeData)

		// Update counters while still holding the lock to prevent races
		entriesLoaded++
		totalBytesLoaded += nodeSize

		c.mu.Unlock()

		// Log progress periodically
		if entriesLoaded%logInterval == 0 {
			log.Info("Preloading storage trie progress",
				"account hash", accountHash.Hex(),
				"entries", entriesLoaded,
				"current depth", item.depth,
				"max depth", maxDepthReached,
				"size", common.StorageSize(totalBytesLoaded).String(),
				"cache usage", fmt.Sprintf("%.1f%%", float64(totalBytesLoaded)*100/float64(cacheSize)),
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

	// Log the completion
	loadTime := time.Since(startTime)
	log.Info("Completed storage trie preload",
		"account hash", accountHash.Hex(),
		"entries", entriesLoaded,
		"max depth", maxDepthReached,
		"size", common.StorageSize(totalBytesLoaded).String(),
		"cache usage", fmt.Sprintf("%.1f%%", float64(totalBytesLoaded)*100/float64(cacheSize)),
		"time", loadTime)
}

// gatherChildPaths uses ForGatherChildren to extract child node paths from a trie node.
// It decodes the node and collects paths for all child nodes that need to be loaded.
func (c *AddressBiasedCache) gatherChildPaths(nodeData []byte, currentPath []byte) [][]byte {
	var childPaths [][]byte
	for i := byte(0); i < 16; i++ {
		childPath := append(append([]byte(nil), currentPath...), i)
		childPaths = append(childPaths, childPath)
	}

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
	if len(key) >= common.HashLength {
		accountHash := common.BytesToHash(key[:common.HashLength])
		if cache, ok := c.addressCaches.Load(accountHash); ok {
			return cache.(*fastcache.Cache), true
		}
	}

	return c.commonCache, false
}

// Get retrieves the value for the given key from the appropriate cache
func (c *AddressBiasedCache) Get(key []byte) []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cache, isAddressCache := c.routeCache(key)
	value := cache.Get(nil, key)

	if isAddressCache {
		if len(value) > 0 {
			biasedAddressCacheHitMeter.Mark(1)
			biasedAddressCacheReadMeter.Mark(int64(len(value)))
		} else {
			biasedAddressCacheMissMeter.Mark(1)
		}
	}

	return value
}

// Set stores the key-value pair in the appropriate cache
func (c *AddressBiasedCache) Set(key, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cache, isAddressCache := c.routeCache(key)
	cache.Set(key, value)

	if isAddressCache {
		biasedAddressCacheWriteMeter.Mark(int64(len(value)))
	}
}

// Has checks if the key exists in the appropriate cache
func (c *AddressBiasedCache) Has(key []byte) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cache, _ := c.routeCache(key)
	return cache.Has(key)
}

// Del removes the key from the appropriate cache
func (c *AddressBiasedCache) Del(key []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cache, _ := c.routeCache(key)
	cache.Del(key)
}

// Reset resets all caches
func (c *AddressBiasedCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.commonCache.Reset()
	c.addressCaches.Range(func(key, value any) bool {
		cache := value.(*fastcache.Cache)
		cache.Reset()
		return true
	})
}

// Close cancels all background preload operations and waits for them to finish.
// This ensures graceful shutdown and prevents goroutines from blocking application termination.
func (c *AddressBiasedCache) Close() {
	if c.cancel != nil {
		c.cancel()  // Signal all goroutines to stop
		c.wg.Wait() // Wait for them to finish
	}
}
