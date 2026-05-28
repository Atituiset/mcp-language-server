package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

type CacheItem struct {
	Value     interface{}
	Timestamp time.Time
	TTL       time.Duration
}

func (c *CacheItem) IsExpired() bool {
	if c.TTL == 0 {
		return false
	}
	return time.Since(c.Timestamp) > c.TTL
}

type Cache struct {
	items map[string]*CacheItem
	mu    sync.RWMutex
	ttl   time.Duration
}

func NewCache(defaultTTL time.Duration) *Cache {
	return &Cache{
		items: make(map[string]*CacheItem),
		ttl:   defaultTTL,
	}
}

func generateKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	if item.IsExpired() {
		return nil, false
	}

	return item.Value, true
}

func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ttl == 0 {
		ttl = c.ttl
	}

	c.items[key] = &CacheItem{
		Value:     value,
		Timestamp: time.Now(),
		TTL:       ttl,
	}
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*CacheItem)
}

func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, item := range c.items {
		if item.IsExpired() {
			delete(c.items, key)
		}
	}
}

func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

type SearchResultCache struct {
	*Cache
}

func NewSearchResultCache(ttl time.Duration) *SearchResultCache {
	return &SearchResultCache{
		Cache: NewCache(ttl),
	}
}

func SearchCacheKey(query, strategy, filePath, language string) string {
	return generateKey(query, strategy, filePath, language)
}

func StructCacheKey(structName, filePath, language string) string {
	return generateKey("struct", structName, filePath, language)
}

func CallHierarchyCacheKey(filePath string, line, column, depth int) string {
	return generateKey("call", filePath, strconv.Itoa(line), strconv.Itoa(column), strconv.Itoa(depth))
}
