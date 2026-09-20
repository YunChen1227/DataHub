-- 0008：支持管理后台「按年/月/日」用量统计与明细时间过滤 (admin §16.4)
-- 方言：PostgreSQL
-- 统计/明细按 (version, app_key) 圈定范围后再按 created_at 分桶或过滤，故加一条
-- 覆盖该访问模式的复合索引；另加单列 created_at 索引兜底纯时间范围扫描。
CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_log (created_at);
CREATE INDEX IF NOT EXISTS idx_audit_ver_appkey_created ON audit_log (version, app_key, created_at);
