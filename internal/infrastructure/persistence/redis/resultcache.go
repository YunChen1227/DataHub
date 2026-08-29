package redis

import (
	"context"
	"encoding/json"
	"time"

	"github.com/datahub/relay/internal/domain/cache"
	goredis "github.com/redis/go-redis/v9"
)

// defaultCacheTimeout 是缓存读写的独立超时。缓存是旁路：宁可当作未命中多打一次上游，
// 也不能让 Redis 抖动把下游请求拖慢。取值远小于上游超时 (4s) 且远大于内网正常 RTT
// (0.3~1ms)，留足抖动余量又不至于成为延迟来源。
const defaultCacheTimeout = 150 * time.Millisecond

// ResultCache implements port.ResultCache on Redis (自然月结果缓存)。
//
// 与配额计数器共用同一个域内 Redis 逻辑库，靠 key 前缀区分 (qc:* 对 quota:*)。
// 运维前提：实例的 maxmemory-policy 必须是 volatile-lru——缓存 key 都带 TTL、配额
// 计数器不带 TTL，这样内存吃紧时淘汰压力只会落在缓存上，计数器绝不会被淘汰。
type ResultCache struct {
	rdb     *goredis.Client
	timeout time.Duration
}

// NewResultCacheOn 复用 Quota 已建立的连接池：v8/v9 同属 v8v9 域共用一个 Quota，
// 缓存也就共用同一个池，不会为缓存再开一份连接。timeout ≤0 时取缺省 150ms。
func NewResultCacheOn(q *Quota, timeout time.Duration) *ResultCache {
	if timeout <= 0 {
		timeout = defaultCacheTimeout
	}
	return &ResultCache{rdb: q.rdb, timeout: timeout}
}

// Get 读取缓存条目。未命中返回 (nil, nil)；连接/超时/反序列化失败返回 error，由
// 调用方降级为未命中。反序列化失败也当错误上抛——那说明 key 被别的东西占用或
// Entry 结构变更，需要日志暴露而不是静默吞掉。
func (c *ResultCache) Get(ctx context.Context, key string) (*cache.Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err == goredis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var e cache.Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Set 写入条目并设置 TTL。由异步记账器在响应写回后调用，不在关键路径上。
func (c *ResultCache) Set(ctx context.Context, key string, e *cache.Entry, ttl time.Duration) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.rdb.Set(ctx, key, payload, ttl).Err()
}
