// Package cache wraps an LRU with TTL semantics for topic-analysis results.
package cache

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache is a fixed-size LRU where every entry also carries a TTL.
type TTLCache[K comparable, V any] struct {
	ttl  time.Duration
	lru  *lru.Cache[K, entry[V]]
	mu   sync.Mutex // guards lru access (lru is already concurrent but Get+Add must be coherent)
	now  func() time.Time
}

func New[K comparable, V any](size int, ttl time.Duration) (*TTLCache[K, V], error) {
	l, err := lru.New[K, entry[V]](size)
	if err != nil {
		return nil, err
	}
	return &TTLCache[K, V]{ttl: ttl, lru: l, now: time.Now}, nil
}

func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.lru.Get(key)
	if !ok {
		return zero, false
	}
	if c.now().After(v.expiresAt) {
		c.lru.Remove(key)
		return zero, false
	}
	return v.value, true
}

func (c *TTLCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Add(key, entry[V]{value: value, expiresAt: c.now().Add(c.ttl)})
}

func (c *TTLCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}
