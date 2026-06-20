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

// Durable is the PostgreSQL mirror the quota repo reads 成功查得数 from and
// write-throughs mutations to (implemented by persistence/postgres.Store).
type Durable interface {
	ServiceUsedCount(ctx context.Context, licenseID string) (svcUsed int64, err error)
	AddServiceUsed(ctx context.Context, licenseID string, delta int64) error
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

func kSvcUsed(lid string) string { return "quota:" + lid + ":svc_used" }

// ensure lazily seeds the Redis 成功查得数 counter from the durable PG mirror
// (SETNX so a flushed Redis is rehydrated and concurrent processes don't clobber).
func (q *Quota) ensure(ctx context.Context, licenseID string) error {
	if _, ok := q.seeded.Load(licenseID); ok {
		return nil
	}
	svcUsed, err := q.pg.ServiceUsedCount(ctx, licenseID)
	if err != nil {
		return err
	}
	if err := q.rdb.SetNX(ctx, kSvcUsed(licenseID), svcUsed, 0).Err(); err != nil {
		return err
	}
	q.seeded.Store(licenseID, struct{}{})
	return nil
}

// ServiceUsed returns the cumulative 成功查得数 (Redis counter, PG-mirrored).
func (q *Quota) ServiceUsed(ctx context.Context, licenseID string) (int64, error) {
	if err := q.ensure(ctx, licenseID); err != nil {
		return 0, err
	}
	used, err := q.rdb.Get(ctx, kSvcUsed(licenseID)).Int64()
	if err == goredis.Nil {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	return used, nil
}

// IncServiceUsed increments 成功查得数 by 1 (Redis) and mirrors to PG.
func (q *Quota) IncServiceUsed(ctx context.Context, licenseID string) error {
	if err := q.ensure(ctx, licenseID); err != nil {
		return err
	}
	if err := q.rdb.Incr(ctx, kSvcUsed(licenseID)).Err(); err != nil {
		return err
	}
	return q.pg.AddServiceUsed(ctx, licenseID, 1)
}
