//go:build ignore

// 00_connectivity: 直连 relay 配置里的线上 PostgreSQL + Redis 并 PING，确认本机
// 确实连得上你在阿里云购买的实例（与 relay 使用同一份连接信息）。
//
// Run: go run test/cases/00_connectivity.go
package main

import (
	"context"
	"time"

	"github.com/datahub/relay/test/harness"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
)

func main() {
	rec := harness.NewRecorder("00_connectivity", "线上 PostgreSQL + Redis 连通性")
	defer rec.Finish()

	pg, rd, err := harness.LoadStorageConfig()
	if err != nil {
		rec.Fail("读取存储配置", "成功解析 config", "", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// PostgreSQL (memory 模式无 database 段时跳过)
	if pg.Host == "" {
		rec.Skip("PostgreSQL PING", "PING 成功", "未配置 database（memory 模式）")
	} else if pool, err := pgxpool.New(ctx, pg.DSN()); err != nil {
		rec.Fail("PostgreSQL 连接", "可建立连接池", pg.Host, err.Error())
	} else {
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			rec.Fail("PostgreSQL PING", "PING 成功", pg.Host, err.Error())
		} else {
			var n int
			_ = pool.QueryRow(ctx, "SELECT 1").Scan(&n)
			rec.Check("PostgreSQL PING", "PING 成功且 SELECT 1=1", n == 1, pg.Host)
		}
	}

	// Redis (memory 模式无 redis 段时跳过)
	if rd.Addr == "" {
		rec.Skip("Redis PING", "PING 成功", "未配置 redis（memory 模式）")
	} else {
		rdb := goredis.NewClient(&goredis.Options{Addr: rd.Addr, Username: rd.Username, Password: rd.Password, DB: rd.DB})
		defer rdb.Close()
		if err := rdb.Ping(ctx).Err(); err != nil {
			rec.Fail("Redis PING", "PING 成功", rd.Addr, err.Error())
		} else {
			rec.Pass("Redis PING", "PING 成功", rd.Addr)
		}
	}
}
