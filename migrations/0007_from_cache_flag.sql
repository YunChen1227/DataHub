-- 0007: 标记「自然月结果缓存」命中的台账/审计行 (from_cache)
--
-- 缓存命中时 called_upstream=false 但 billed=true（照常给客户计费，只是没花上游的钱），
-- 没有这一列的话后台看起来像是「没调上游却收了钱」的脏数据。加上后：
--   • 本月命中率 = 一句 SQL（count(from_cache) / count(*)）；
--   • 向上游对账时可一眼排除掉这些没有上游订单号 (upstream_uid) 的行。
--
-- 仅 x1/v8/v9 会产生 from_cache=true 的行；其余路由未启用缓存，恒为 false。

ALTER TABLE billing_ledger
    ADD COLUMN IF NOT EXISTS from_cache BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE audit_log
    ADD COLUMN IF NOT EXISTS from_cache BOOLEAN NOT NULL DEFAULT FALSE;
