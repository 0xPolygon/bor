package cache

import (
	"errors"
	"sync"
	"time"
)

type CacheItem[T any] struct {
	Value      T
	Expiration int64
}

type Cache[T any] struct {
	items map[string]CacheItem[T]
	mu    sync.RWMutex
	ttl   time.Duration
}

func NewCache[T any](defaultTTL time.Duration) *Cache[T] {
	return &Cache[T]{
		items: make(map[string]CacheItem[T]),
		ttl:   defaultTTL,
	}
}

func (c *Cache[T]) Set(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = CacheItem[T]{
		Value:      value,
		Expiration: time.Now().Add(c.ttl).UnixNano(),
	}
}

func (c *Cache[T]) Get(key string) (T, error) {
	c.mu.RLock()
	item, found := c.items[key]
	c.mu.RUnlock()

	var zero T

	if !found {
		return zero, errors.New("item not found")
	}

	if time.Now().UnixNano() > item.Expiration {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return zero, errors.New("item expired")
	}

	return item.Value, nil
}
