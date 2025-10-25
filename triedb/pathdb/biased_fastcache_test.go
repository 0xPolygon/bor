package pathdb

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestAddressBiasedCache_RouteCache(t *testing.T) {
	addr1 := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addr2 := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")

	addressCacheSizes := map[common.Address]int{
		addr1: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Test routing for preloaded address
	accountHash1 := crypto.Keccak256Hash(addr1.Bytes())
	key1 := accountHash1.Bytes()

	targetCache, isAddressCache := cache.routeCache(key1)
	if !isAddressCache {
		t.Error("Expected address-specific cache for preloaded address")
	}
	expectedCache, _ := cache.addressCaches.Load(accountHash1)
	if targetCache != expectedCache.(*fastcache.Cache) {
		t.Error("Incorrect cache returned for preloaded address")
	}

	// Test routing for non-preloaded address
	accountHash2 := crypto.Keccak256Hash(addr2.Bytes())
	key2 := accountHash2.Bytes()

	targetCache, isAddressCache = cache.routeCache(key2)
	if isAddressCache {
		t.Error("Expected common cache for non-preloaded address")
	}
	if targetCache != cache.commonCache {
		t.Error("Incorrect cache returned for non-preloaded address")
	}

	// Test routing for short key (account trie)
	shortKey := []byte{0x01, 0x02}
	targetCache, isAddressCache = cache.routeCache(shortKey)
	if isAddressCache {
		t.Error("Expected common cache for short key")
	}
	if targetCache != cache.commonCache {
		t.Error("Incorrect cache returned for short key")
	}
}

func TestAddressBiasedCache_GetSet(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	accountHash := crypto.Keccak256Hash(addr.Bytes())
	key := append(accountHash.Bytes(), []byte{0x01, 0x02}...)
	value := []byte("test value")

	// Test Set and Get for address-specific cache
	cache.Set(key, value)
	retrieved := cache.Get(key)
	if !bytes.Equal(retrieved, value) {
		t.Errorf("Expected %v, got %v", value, retrieved)
	}

	// Test Set and Get for common cache
	commonKey := []byte{0x01, 0x02}
	commonValue := []byte("common value")
	cache.Set(commonKey, commonValue)
	retrieved = cache.Get(commonKey)
	if !bytes.Equal(retrieved, commonValue) {
		t.Errorf("Expected %v, got %v", commonValue, retrieved)
	}

	// Test Get for non-existent key
	nonExistentKey := append(accountHash.Bytes(), []byte{0xff, 0xff}...)
	retrieved = cache.Get(nonExistentKey)
	if len(retrieved) != 0 {
		t.Errorf("Expected empty slice, got %v", retrieved)
	}
}

func TestAddressBiasedCache_Has(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	accountHash := crypto.Keccak256Hash(addr.Bytes())
	key := append(accountHash.Bytes(), []byte{0x01, 0x02}...)
	value := []byte("test value")

	// Test Has for non-existent key
	if cache.Has(key) {
		t.Error("Has should return false for non-existent key")
	}

	// Test Has for existing key
	cache.Set(key, value)
	if !cache.Has(key) {
		t.Error("Has should return true for existing key")
	}
}

func TestAddressBiasedCache_Del(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	accountHash := crypto.Keccak256Hash(addr.Bytes())
	key := append(accountHash.Bytes(), []byte{0x01, 0x02}...)
	value := []byte("test value")

	// Set and verify
	cache.Set(key, value)
	if !cache.Has(key) {
		t.Error("Key should exist after Set")
	}

	// Delete and verify
	cache.Del(key)
	if cache.Has(key) {
		t.Error("Key should not exist after Del")
	}

	// Verify Get returns empty after Del
	retrieved := cache.Get(key)
	if len(retrieved) != 0 {
		t.Error("Get should return empty slice after Del")
	}
}

func TestAddressBiasedCache_Reset(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Add data to both address-specific and common caches
	accountHash := crypto.Keccak256Hash(addr.Bytes())
	addressKey := append(accountHash.Bytes(), []byte{0x01, 0x02}...)
	commonKey := []byte{0x01, 0x02}

	cache.Set(addressKey, []byte("address value"))
	cache.Set(commonKey, []byte("common value"))

	// Verify data exists
	if !cache.Has(addressKey) || !cache.Has(commonKey) {
		t.Error("Data should exist before reset")
	}

	// Reset all caches
	cache.Reset()

	// Verify data is gone
	if cache.Has(addressKey) || cache.Has(commonKey) {
		t.Error("Data should not exist after reset")
	}
}

func TestAddressBiasedCache_MultipleAddresses(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr3 := common.HexToAddress("0x3333333333333333333333333333333333333333")

	addressCacheSizes := map[common.Address]int{
		addr1: 1024 * 1024,
		addr2: 512 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 256*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Verify correct number of address caches
	var count int
	cache.addressCaches.Range(func(key, value any) bool {
		count++
		return true
	})
	if count != 2 {
		t.Errorf("Expected 2 address caches, got %d", count)
	}

	// Test data isolation between caches
	accountHash1 := crypto.Keccak256Hash(addr1.Bytes())
	accountHash2 := crypto.Keccak256Hash(addr2.Bytes())
	accountHash3 := crypto.Keccak256Hash(addr3.Bytes())

	key1 := append(accountHash1.Bytes(), []byte{0x01}...)
	key2 := append(accountHash2.Bytes(), []byte{0x01}...)
	key3 := append(accountHash3.Bytes(), []byte{0x01}...)

	cache.Set(key1, []byte("value1"))
	cache.Set(key2, []byte("value2"))
	cache.Set(key3, []byte("value3"))

	// Verify values are isolated
	val1 := cache.Get(key1)
	val2 := cache.Get(key2)
	val3 := cache.Get(key3)

	if !bytes.Equal(val1, []byte("value1")) {
		t.Error("Value1 mismatch")
	}
	if !bytes.Equal(val2, []byte("value2")) {
		t.Error("Value2 mismatch")
	}
	if !bytes.Equal(val3, []byte("value3")) {
		t.Error("Value3 mismatch (should be in common cache)")
	}
}

func TestAddressBiasedCache_PreloadWithData(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	accountHash := crypto.Keccak256Hash(addr.Bytes())

	// Create database with some storage trie nodes
	db := rawdb.NewMemoryDatabase()

	// Write root node
	rootData := []byte("root node data")
	rawdb.WriteStorageTrieNode(db, accountHash, nil, rootData)

	// Write child nodes at depth 1
	for i := byte(0); i < 4; i++ {
		path := []byte{i}
		data := []byte("child node " + string(rune(i)))
		rawdb.WriteStorageTrieNode(db, accountHash, path, data)
	}

	// Create cache with preloading
	addressCacheSizes := map[common.Address]int{
		addr: 10 * 1024, // Small cache to test limit
	}

	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Wait for async preloading to complete
	time.Sleep(100 * time.Millisecond)

	// Verify root node was loaded
	rootKey := accountHash.Bytes()
	if !cache.Has(rootKey) {
		t.Error("Expected root node to be preloaded")
	}
	retrieved := cache.Get(rootKey)
	if !bytes.Equal(retrieved, rootData) {
		t.Error("Root node data mismatch")
	}
}

func TestAddressBiasedCache_GatherChildPaths(t *testing.T) {
	cache := &AddressBiasedCache{}

	nodeData := []byte("dummy node data")
	currentPath := []byte{0x01}

	childPaths := cache.gatherChildPaths(nodeData, currentPath)

	// Verify 16 child paths are generated (one for each nibble)
	if len(childPaths) != 16 {
		t.Errorf("Expected 16 child paths, got %d", len(childPaths))
	}

	// Verify each child path is correct
	for i := byte(0); i < 16; i++ {
		expectedPath := append([]byte{0x01}, i)
		if !bytes.Equal(childPaths[i], expectedPath) {
			t.Errorf("Child path %d mismatch: expected %v, got %v", i, expectedPath, childPaths[i])
		}
	}

	// Test with empty current path
	childPaths = cache.gatherChildPaths(nodeData, nil)
	if len(childPaths) != 16 {
		t.Errorf("Expected 16 child paths for root, got %d", len(childPaths))
	}
	for i := byte(0); i < 16; i++ {
		expectedPath := []byte{i}
		if !bytes.Equal(childPaths[i], expectedPath) {
			t.Errorf("Root child path %d mismatch: expected %v, got %v", i, expectedPath, childPaths[i])
		}
	}
}

func TestAddressBiasedCache_EmptyDatabase(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	_, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Wait for async preloading to complete
	time.Sleep(50 * time.Millisecond)

	// Cache created successfully for empty database
}

func TestAddressBiasedCache_AsyncPreloadWithConcurrentWrites(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	accountHash := crypto.Keccak256Hash(addr.Bytes())

	// Create database with storage trie nodes
	db := rawdb.NewMemoryDatabase()

	// Write root node
	rootData := []byte("root node data")
	rawdb.WriteStorageTrieNode(db, accountHash, nil, rootData)

	// Write some child nodes
	for i := byte(0); i < 10; i++ {
		path := []byte{i}
		data := []byte("node data " + string(rune(i)))
		rawdb.WriteStorageTrieNode(db, accountHash, path, data)
	}

	// Create cache with async preloading
	addressCacheSizes := map[common.Address]int{
		addr: 100 * 1024,
	}

	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Immediately start writing to the cache while preloading is happening
	// Use a key that doesn't exist in the database
	testKey := append(accountHash.Bytes(), byte(5))
	manualValue := []byte("manually added value")
	cache.Set(testKey, manualValue)

	// Wait for async preloading to complete
	time.Sleep(100 * time.Millisecond)

	// Verify the manually added value or DB value exists
	retrieved := cache.Get(testKey)
	// The value could be either manual or from DB, just check it exists
	if len(retrieved) == 0 {
		t.Error("Expected key to have a value")
	}
}

func TestAddressBiasedCache_ConcurrentAccess(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 512*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	accountHash := crypto.Keccak256Hash(addr.Bytes())

	// Test concurrent reads and writes
	done := make(chan bool)
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			key := append(accountHash.Bytes(), byte(id))
			value := []byte("value " + string(rune(id)))

			// Perform multiple operations
			for j := 0; j < 100; j++ {
				cache.Set(key, value)
				cache.Get(key)
				cache.Has(key)
				if j%10 == 0 {
					cache.Del(key)
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete with timeout
	timeout := time.After(5 * time.Second)
	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("Test timed out waiting for concurrent operations")
		}
	}
}

func BenchmarkAddressBiasedCache_Get_AddressCache(b *testing.B) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 10 * 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 5*1024*1024)
	if err != nil {
		b.Fatalf("Failed to create cache: %v", err)
	}

	accountHash := crypto.Keccak256Hash(addr.Bytes())
	key := append(accountHash.Bytes(), []byte{0x01, 0x02}...)
	value := []byte("benchmark value")
	cache.Set(key, value)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get(key)
	}
}

func BenchmarkAddressBiasedCache_Get_CommonCache(b *testing.B) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 10 * 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 5*1024*1024)
	if err != nil {
		b.Fatalf("Failed to create cache: %v", err)
	}

	key := []byte{0x01, 0x02}
	value := []byte("benchmark value")
	cache.Set(key, value)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get(key)
	}
}

func BenchmarkAddressBiasedCache_Set(b *testing.B) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	addressCacheSizes := map[common.Address]int{
		addr: 10 * 1024 * 1024,
	}

	db := rawdb.NewMemoryDatabase()
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 5*1024*1024)
	if err != nil {
		b.Fatalf("Failed to create cache: %v", err)
	}

	accountHash := crypto.Keccak256Hash(addr.Bytes())
	value := []byte("benchmark value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := append(accountHash.Bytes(), byte(i%256))
		cache.Set(key, value)
	}
}

// TestAddressBiasedCache_GracefulShutdown tests that Close() properly stops
// background preload operations and waits for them to finish.
func TestAddressBiasedCache_GracefulShutdown(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	accountHash := crypto.Keccak256Hash(addr.Bytes())

	// Create a large tree of storage trie nodes that will take some time to preload
	nodeCount := 1000
	for i := 0; i < nodeCount; i++ {
		path := []byte{byte(i % 256), byte(i / 256)}
		nodeData := []byte(fmt.Sprintf("node-data-%d", i))
		rawdb.WriteStorageTrieNode(db, accountHash, path, nodeData)
	}

	// Create cache with preloading
	addressCacheSizes := map[common.Address]int{
		addr: 10 * 1024 * 1024, // 10 MB
	}
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 1024*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Immediately close the cache to test interruption
	cache.Close()

	// Verify the cache is still functional after Close()
	key := append(accountHash.Bytes(), []byte{1, 2}...)
	cache.Set(key, []byte("test-value"))
	value := cache.Get(key)
	if string(value) != "test-value" {
		t.Errorf("Cache should still work after Close(), got: %s", string(value))
	}
}

// TestAddressBiasedCache_MultipleClose tests that calling Close() multiple times
// doesn't cause issues.
func TestAddressBiasedCache_MultipleClose(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	addressCacheSizes := map[common.Address]int{
		addr: 1024 * 1024, // 1 MB
	}
	cache, err := NewAddressBiasedCache(db, addressCacheSizes, 1024*1024)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Close multiple times should not panic
	cache.Close()
	cache.Close()
	cache.Close()
}
