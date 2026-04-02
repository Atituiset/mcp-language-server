package cache

import (
	"sync"
	"time"
)

// CacheItem 缓存项结构
type CacheItem struct {
	Value     interface{} // 缓存值
	Timestamp time.Time   // 创建时间
	TTL       time.Duration // 过期时间
}

// IsExpired 检查缓存是否过期
func (c *CacheItem) IsExpired() bool {
	if c.TTL == 0 {
		return false // TTL 为 0 表示永不过期
	}
	return time.Since(c.Timestamp) > c.TTL
}

// Cache 线程安全的内存缓存
type Cache struct {
	items map[string]*CacheItem
	mu    sync.RWMutex
	ttl   time.Duration // 默认 TTL
}

// NewCache 创建新的缓存实例
//
// 参数:
//   - defaultTTL: 默认过期时间，0 表示永不过期
//
// 返回: 缓存指针
func NewCache(defaultTTL time.Duration) *Cache {
	return &Cache{
		items: make(map[string]*CacheItem),
		ttl:   defaultTTL,
	}
}

// generateKey 生成缓存键
func generateKey(parts ...string) string {
	// 简单的键生成，实际可用更好的哈希
	result := ""
	for _, p := range parts {
		result += p + ":"
	}
	return result
}

// Get 获取缓存值
//
// 参数:
//   - key: 缓存键
//
// 返回: 缓存值和是否存在的布尔值
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

// Set 设置缓存值
//
// 参数:
//   - key: 缓存键
//   - value: 缓存值
//   - ttl: 过期时间，0 表示使用默认值
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

// Delete 删除缓存项
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Clear 清空所有缓存
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*CacheItem)
}

// Cleanup 删除所有过期缓存项
func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, item := range c.items {
		if item.IsExpired() {
			delete(c.items, key)
		}
	}
}

// Size 返回缓存项数量
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// SearchResultCache 专门用于搜索结果的缓存
type SearchResultCache struct {
	*Cache
}

// NewSearchResultCache 创建搜索结果缓存
func NewSearchResultCache(ttl time.Duration) *SearchResultCache {
	return &SearchResultCache{
		Cache: NewCache(ttl),
	}
}

// SearchCacheKey 生成搜索缓存键
func SearchCacheKey(query, strategy, filePath, language string) string {
	return generateKey(query, strategy, filePath, language)
}

// StructCacheKey 生成结构体查询缓存键
func StructCacheKey(structName, filePath, language string) string {
	return generateKey("struct", structName, filePath, language)
}

// CallHierarchyCacheKey 生成调用层级缓存键
func CallHierarchyCacheKey(filePath string, line, column, depth int) string {
	return generateKey("call", filePath, itoa(line), itoa(column), itoa(depth))
}

// itoa 简单的 int 转 string
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}
