//go:build ignore

// 00_connectivity: 直连 relay 配置里每个版本独立的 PostgreSQL + Redis 并 PING，
// 确认本机连得上你在阿里云购买的实例（与 relay 使用同一份连接信息）。
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
	rec := harness.NewRecorder("00_connectivity", "各版本独立 PostgreSQL + Redis 连通性")
	defer rec.Finish()

	pgs, rds, err := harness.LoadStorageConfigs()
	if err != nil {
		rec.Fail("读取存储配置", "成功解析 config", "", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if len(pgs) == 0 {
		rec.Skip("PostgreSQL PING", "PING 成功", "未配置 database（memory 模式）")
	}
	for _, pg := range pgs {
		name := "PostgreSQL[" + pg.Version + "] " + pg.Name
		if pool, err := pgxpool.New(ctx, pg.DSN()); err != nil {
			rec.Fail(name+" 连接", "可建立连接池", pg.Host, err.Error())
		} else {
			var n int
			err := pool.QueryRow(ctx, "SELECT 1").Scan(&n)
			pool.Close()
			if err != nil {
				rec.Fail(name+" PING", "PING 成功", pg.Host, err.Error())
			} else {
				rec.Check(name+" PING", "PING 成功且 SELECT 1=1", n == 1, pg.Host)
			}
		}
	}

	if len(rds) == 0 {
		rec.Skip("Redis PING", "PING 成功", "未配置 redis（memory 模式）")
	}
	for _, rd := range rds {
		name := "Redis[" + rd.Version + "] db" + itoa(rd.DB)
		rdb := goredis.NewClient(&goredis.Options{Addr: rd.Addr, Username: rd.Username, Password: rd.Password, DB: rd.DB})
		if err := rdb.Ping(ctx).Err(); err != nil {
			rec.Fail(name+" PING", "PING 成功", rd.Addr, err.Error())
		} else {
			rec.Pass(name+" PING", "PING 成功", rd.Addr)
		}
		rdb.Close()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
