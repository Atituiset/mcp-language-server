package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type CacheItem struct {
	Value     interface{}
	Timestamp time.Time
	TTL       time.Duration
	// Files records the workspace files a cached value depends on, for
	// per-file invalidation (nil = unknown, cleared on any change).
	Files []string
}

func (c *CacheItem) IsExpired() bool {
	if c.TTL == 0 {
		return false
	}
	return time.Since(c.Timestamp) > c.TTL
}

// Cache is a thread-safe TTL cache with a file-dependency reverse index so
// per-file invalidation costs O(dependents) instead of scanning every entry.
type Cache struct {
	items  map[string]*CacheItem
	byFile map[string]map[string]bool // file -> keys depending on it
	// unknownDeps holds keys with no dependency info; they are
	// conservatively dropped on any file change.
	unknownDeps map[string]bool
	mu          sync.RWMutex
	ttl         time.Duration
}

func NewCache(defaultTTL time.Duration) *Cache {
	return &Cache{
		items:       make(map[string]*CacheItem),
		byFile:      make(map[string]map[string]bool),
		unknownDeps: make(map[string]bool),
		ttl:         defaultTTL,
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
	c.set(key, value, ttl, nil)
}

// SetWithFiles is Set plus the file dependency set used by DeleteByFile.
func (c *Cache) SetWithFiles(key string, value interface{}, ttl time.Duration, files []string) {
	c.set(key, value, ttl, files)
}

func (c *Cache) set(key string, value interface{}, ttl time.Duration, files []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ttl == 0 {
		ttl = c.ttl
	}

	c.unindexLocked(key)
	c.items[key] = &CacheItem{
		Value:     value,
		Timestamp: time.Now(),
		TTL:       ttl,
		Files:     files,
	}
	if len(files) == 0 {
		c.unknownDeps[key] = true
	} else {
		for _, f := range files {
			if c.byFile[f] == nil {
				c.byFile[f] = make(map[string]bool)
			}
			c.byFile[f][key] = true
		}
	}

	// Opportunistic sweep so expired entries cannot accumulate unbounded.
	c.cleanupLocked()
}

// unindexLocked removes key from the reverse indexes (used before replacing
// or deleting an entry).
func (c *Cache) unindexLocked(key string) {
	old, exists := c.items[key]
	if !exists {
		return
	}
	delete(c.unknownDeps, key)
	for _, f := range old.Files {
		if keys := c.byFile[f]; keys != nil {
			delete(keys, key)
			if len(keys) == 0 {
				delete(c.byFile, f)
			}
		}
	}
}

// DeleteByFile removes entries whose file dependency set contains path.
// Entries without dependency info are removed as well (conservative).
func (c *Cache) DeleteByFile(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.byFile[path] {
		c.unindexLocked(key)
		delete(c.items, key)
	}
	for key := range c.unknownDeps {
		delete(c.items, key)
		delete(c.unknownDeps, key)
	}
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unindexLocked(key)
	delete(c.items, key)
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*CacheItem)
	c.byFile = make(map[string]map[string]bool)
	c.unknownDeps = make(map[string]bool)
}

// Cleanup removes expired entries.
func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked()
}

func (c *Cache) cleanupLocked() {
	for key, item := range c.items {
		if item.IsExpired() {
			c.unindexLocked(key)
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

func SearchCacheKey(query, strategy, filePath, language, intent string) string {
	return generateKey(query, strategy, filePath, language, intent)
}
