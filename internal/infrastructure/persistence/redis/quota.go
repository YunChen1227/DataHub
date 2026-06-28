// Package redis is the 成功查得数 counter adapter (DESIGN §7.5). v0.6 起取消额度
// 限制与维度②上游计数：Redis 仅保存 svc_used 计数器，write-through 到 durable
// PostgreSQL 镜像，Redis flush/restart 后按 key miss 重新 seed。
package redis

import (
	"context"
	"fmt"
	"sync"

	goredis "github.com/redis/go-redis/v9"
)

// Durable is the PostgreSQL mirror the quota repo reads per-route 计数 from and
// write-throughs mutations to (implemented by persistence/postgres.Store)。
type Durable interface {
	ServiceUsedCount(ctx context.Context, licenseID, route string) (svcUsed int64, err error)
	AddServiceUsed(ctx context.Context, licenseID, route string, delta int64) error
	TotalCallsCount(ctx context.Context, licenseID, route string) (calls int64, err error)
	AddTotalCalls(ctx context.Context, licenseID, route string, delta int64) error
}

// Options configures the Redis connection.
type Options struct {
	Addr     string
	Username string
	Password string
	DB       int
	PoolSize int
}

// Quota implements port.QuotaRepository on Redis + a durable PG mirror.
type Quota struct {
	rdb    *goredis.Client
	pg     Durable
	seeded sync.Map // licenseID -> struct{} (process-local seed guard)
}

// New dials Redis and verifies connectivity.
func New(ctx context.Context, opts Options, pg Durable) (*Quota, error) {
	rdb := goredis.NewClient(&goredis.Options{
		Addr:     opts.Addr,
		Username: opts.Username,
		Password: opts.Password,
		DB:       opts.DB,
		PoolSize: opts.PoolSize,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &Quota{rdb: rdb, pg: pg}, nil
}

// Close releases the Redis client.
func (q *Quota) Close() { _ = q.rdb.Close() }

// 计数 key 按 (license, route) 独立：共享 license 的 v8/v9 互不干扰。
func kSvcUsed(lid, route string) string  { return "quota:" + lid + ":" + route + ":svc_used" }
func kCallTotal(lid, route string) string { return "quota:" + lid + ":" + route + ":call_total" }

func seedKey(lid, route string) string { return lid + "|" + route }

// ensure lazily seeds both Redis 计数器 (成功查得数 + 调用次数) from the durable PG
// mirror (SETNX so a flushed Redis is rehydrated and concurrent processes don't clobber)。
func (q *Quota) ensure(ctx context.Context, licenseID, route string) error {
	if _, ok := q.seeded.Load(seedKey(licenseID, route)); ok {
		return nil
	}
	svcUsed, err := q.pg.ServiceUsedCount(ctx, licenseID, route)
	if err != nil {
		return err
	}
	if err := q.rdb.SetNX(ctx, kSvcUsed(licenseID, route), svcUsed, 0).Err(); err != nil {
		return err
	}
	calls, err := q.pg.TotalCallsCount(ctx, licenseID, route)
	if err != nil {
		return err
	}
	if err := q.rdb.SetNX(ctx, kCallTotal(licenseID, route), calls, 0).Err(); err != nil {
		return err
	}
	q.seeded.Store(seedKey(licenseID, route), struct{}{})
	return nil
}

func (q *Quota) getCounter(ctx context.Context, key string) (int64, error) {
	v, err := q.rdb.Get(ctx, key).Int64()
	if err == goredis.Nil {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	return v, nil
}

// ServiceUsed returns the cumulative 成功查得数 for (license, route) (Redis, PG-mirrored).
func (q *Quota) ServiceUsed(ctx context.Context, licenseID, route string) (int64, error) {
	if err := q.ensure(ctx, licenseID, route); err != nil {
		return 0, err
	}
	return q.getCounter(ctx, kSvcUsed(licenseID, route))
}

// IncServiceUsed increments 成功查得数 by 1 for (license, route) (Redis) and mirrors to PG.
func (q *Quota) IncServiceUsed(ctx context.Context, licenseID, route string) error {
	if err := q.ensure(ctx, licenseID, route); err != nil {
		return err
	}
	if err := q.rdb.Incr(ctx, kSvcUsed(licenseID, route)).Err(); err != nil {
		return err
	}
	return q.pg.AddServiceUsed(ctx, licenseID, route, 1)
}

// TotalCalls returns the cumulative 调用次数 for (license, route) (Redis, PG-mirrored).
func (q *Quota) TotalCalls(ctx context.Context, licenseID, route string) (int64, error) {
	if err := q.ensure(ctx, licenseID, route); err != nil {
		return 0, err
	}
	return q.getCounter(ctx, kCallTotal(licenseID, route))
}

// IncTotalCalls increments 调用次数 by 1 for (license, route) (Redis) and mirrors to PG.
func (q *Quota) IncTotalCalls(ctx context.Context, licenseID, route string) error {
	if err := q.ensure(ctx, licenseID, route); err != nil {
		return err
	}
	if err := q.rdb.Incr(ctx, kCallTotal(licenseID, route)).Err(); err != nil {
		return err
	}
	return q.pg.AddTotalCalls(ctx, licenseID, route, 1)
}
