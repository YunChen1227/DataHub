package memory

import (
	"context"
	"sync"
	"time"

	"github.com/datahub/relay/internal/domain/cache"
)

// cacheRow 是一条带绝对到期时刻的缓存条目。
type cacheRow struct {
	entry     cache.Entry
	expiresAt time.Time
}

// ResultCache is the in-process port.ResultCache for dev (storage.driver: memory)
// and e2e。语义与 Redis 版一致：过期即未命中。没有内存上限——单进程开发/测试场景
// 的条目数有限，生产必须换 Redis 版。
type ResultCache struct {
	mu   sync.Mutex
	rows map[string]cacheRow
	now  func() time.Time // 可注入，供测试推进时钟
}

// NewResultCache returns an empty in-process result cache.
func NewResultCache() *ResultCache {
	return &ResultCache{rows: make(map[string]cacheRow), now: time.Now}
}

// WithClock 替换时钟（测试用：验证过期而无需真实等待）。
func (c *ResultCache) WithClock(now func() time.Time) *ResultCache {
	if now != nil {
		c.now = now
	}
	return c
}

func (c *ResultCache) Get(_ context.Context, key string) (*cache.Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[key]
	if !ok {
		return nil, nil
	}
	if !c.now().Before(row.expiresAt) {
		delete(c.rows, key) // 惰性过期，与 Redis 行为一致
		return nil, nil
	}
	cp := row.entry
	return &cp, nil
}

func (c *ResultCache) Set(_ context.Context, key string, e *cache.Entry, ttl time.Duration) error {
	if e == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[key] = cacheRow{entry: *e, expiresAt: c.now().Add(ttl)}
	return nil
}

// Len 返回未过期条目数的近似值（测试/诊断用；不主动清理已过期项）。
func (c *ResultCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	now := c.now()
	for _, row := range c.rows {
		if now.Before(row.expiresAt) {
			n++
		}
	}
	return n
}
