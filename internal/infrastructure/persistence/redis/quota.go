// Package redis is the atomic dual-dimension quota adapter (DESIGN §7.5 方案A).
// Redis holds the enforcement counters (svc_used / up_committed / up_reserved)
// and Lua guarantees atomic reserve/commit/release; every mutation is
// write-through to the durable PostgreSQL quota mirror so purchased totals and
// committed counts survive a Redis flush/restart (reseeded on key miss).
package redis

import (
	"context"
	"fmt"
	"sync"

	goredis "github.com/redis/go-redis/v9"
)

// Durable is the PostgreSQL mirror the quota repo reads totals from and
// write-throughs mutations to (implemented by persistence/postgres.Store).
type Durable interface {
	QuotaCounters(ctx context.Context, licenseID string) (svcTotal, svcUsed, upTotal, upCommitted, upReserved int64, err error)
	AddServiceUsed(ctx context.Context, licenseID string, delta int64) error
	AddUpstream(ctx context.Context, licenseID string, committedDelta, reservedDelta int64) error
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

// reserveScript atomically reserves 维度② capacity when total-committed-reserved>0.
// KEYS[1]=committed KEYS[2]=reserved ; ARGV[1]=total. Returns new reserved or -1.
var reserveScript = goredis.NewScript(`
local committed = tonumber(redis.call('GET', KEYS[1]) or '0')
local reserved  = tonumber(redis.call('GET', KEYS[2]) or '0')
if (tonumber(ARGV[1]) - committed - reserved) > 0 then
  return redis.call('INCR', KEYS[2])
end
return -1
`)

// commitScript moves a reservation to committed (reserved-- floor 0, committed++).
// KEYS[1]=reserved KEYS[2]=committed.
var commitScript = goredis.NewScript(`
local r = tonumber(redis.call('GET', KEYS[1]) or '0')
if r > 0 then redis.call('DECR', KEYS[1]) end
return redis.call('INCR', KEYS[2])
`)

// decrFloorScript decrements a counter without going below 0. KEYS[1]=key.
var decrFloorScript = goredis.NewScript(`
local v = tonumber(redis.call('GET', KEYS[1]) or '0')
if v > 0 then return redis.call('DECR', KEYS[1]) end
return 0
`)

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

func kSvcUsed(lid string) string     { return "quota:" + lid + ":svc_used" }
func kUpCommitted(lid string) string { return "quota:" + lid + ":up_committed" }
func kUpReserved(lid string) string  { return "quota:" + lid + ":up_reserved" }

// ensure lazily seeds the Redis counters from the durable PG mirror (SETNX so a
// flushed Redis is rehydrated and concurrent processes don't clobber).
func (q *Quota) ensure(ctx context.Context, licenseID string) error {
	if _, ok := q.seeded.Load(licenseID); ok {
		return nil
	}
	_, svcUsed, _, upCommitted, upReserved, err := q.pg.QuotaCounters(ctx, licenseID)
	if err != nil {
		return err
	}
	if err := q.rdb.SetNX(ctx, kSvcUsed(licenseID), svcUsed, 0).Err(); err != nil {
		return err
	}
	if err := q.rdb.SetNX(ctx, kUpCommitted(licenseID), upCommitted, 0).Err(); err != nil {
		return err
	}
	if err := q.rdb.SetNX(ctx, kUpReserved(licenseID), upReserved, 0).Err(); err != nil {
		return err
	}
	q.seeded.Store(licenseID, struct{}{})
	return nil
}

// ServiceQuota returns 维度① total (durable) + used (Redis enforcement counter).
func (q *Quota) ServiceQuota(ctx context.Context, licenseID string) (int64, int64, error) {
	if err := q.ensure(ctx, licenseID); err != nil {
		return 0, 0, err
	}
	svcTotal, _, _, _, _, err := q.pg.QuotaCounters(ctx, licenseID)
	if err != nil {
		return 0, 0, err
	}
	used, err := q.rdb.Get(ctx, kSvcUsed(licenseID)).Int64()
	if err == goredis.Nil {
		used = 0
	} else if err != nil {
		return 0, 0, err
	}
	return svcTotal, used, nil
}

// IncServiceUsed increments 维度① used by 1 (Redis) and mirrors to PG.
func (q *Quota) IncServiceUsed(ctx context.Context, licenseID string) error {
	if err := q.ensure(ctx, licenseID); err != nil {
		return err
	}
	if err := q.rdb.Incr(ctx, kSvcUsed(licenseID)).Err(); err != nil {
		return err
	}
	return q.pg.AddServiceUsed(ctx, licenseID, 1)
}

// TryReserveUpstream atomically reserves 维度② capacity when remaining>0.
func (q *Quota) TryReserveUpstream(ctx context.Context, licenseID string) (bool, error) {
	if err := q.ensure(ctx, licenseID); err != nil {
		return false, err
	}
	_, _, upTotal, _, _, err := q.pg.QuotaCounters(ctx, licenseID)
	if err != nil {
		return false, err
	}
	res, err := reserveScript.Run(ctx, q.rdb,
		[]string{kUpCommitted(licenseID), kUpReserved(licenseID)}, upTotal).Int64()
	if err != nil {
		return false, err
	}
	if res < 0 {
		return false, nil
	}
	if err := q.pg.AddUpstream(ctx, licenseID, 0, 1); err != nil {
		return false, err
	}
	return true, nil
}

// CommitUpstream moves a reservation to committed (reserved--, committed++).
func (q *Quota) CommitUpstream(ctx context.Context, licenseID string) error {
	if err := q.ensure(ctx, licenseID); err != nil {
		return err
	}
	if err := commitScript.Run(ctx, q.rdb,
		[]string{kUpReserved(licenseID), kUpCommitted(licenseID)}).Err(); err != nil {
		return err
	}
	return q.pg.AddUpstream(ctx, licenseID, 1, -1)
}

// ReleaseUpstream cancels a reservation (reserved--).
func (q *Quota) ReleaseUpstream(ctx context.Context, licenseID string) error {
	if err := q.ensure(ctx, licenseID); err != nil {
		return err
	}
	if err := decrFloorScript.Run(ctx, q.rdb, []string{kUpReserved(licenseID)}).Err(); err != nil {
		return err
	}
	return q.pg.AddUpstream(ctx, licenseID, 0, -1)
}
