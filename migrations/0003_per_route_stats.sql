-- 经济能力查询转接服务 — per-route 统计 + 共享 license 改造 (v0.8)
-- 方言：PostgreSQL
-- 背景：v8/v9 合并为 v8v9 域共用同一 license（同一物理库），但调用次数 / 成功查得数 /
--       操作日志按各自路由 (version) 独立统计。故计数与台账/审计均引入 route/version 维度。

-- quota：计数按 (license_id, route, dim) 独立。dim = SERVICE(成功查得数) | CALL(调用次数)。
ALTER TABLE quota ADD COLUMN IF NOT EXISTS route VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE quota DROP CONSTRAINT IF EXISTS quota_pkey;
ALTER TABLE quota ADD PRIMARY KEY (license_id, route, dim);

-- billing_ledger：version 标记产生台账的路由；幂等键改为 (app_key, version, reqid)，
-- 使共享 license 的 v8/v9 幂等互不影响。
ALTER TABLE billing_ledger ADD COLUMN IF NOT EXISTS version VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE billing_ledger DROP CONSTRAINT IF EXISTS uq_ledger_appkey_reqid;
ALTER TABLE billing_ledger ADD CONSTRAINT uq_ledger_appkey_version_reqid UNIQUE (app_key, version, reqid);

-- audit_log：version 标记路由，使共享 license 的 v8/v9 操作日志可分路由筛选。
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS version VARCHAR(16) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_audit_version ON audit_log (version);
