-- v0.6 清理：彻底移除维度②（上游配额/上游调用计数），后台只保留"成功查得数"。
-- 前向不可逆：删除 UPSTREAM 配额行、quota.total/reserved 列与约束、
-- billing_ledger.counted_upstream 列。quota 表自此仅保留 dim='SERVICE' 行，
-- used_or_committed = 累计成功查得数。

-- 1. 删除维度② 配额行（仅保留 SERVICE）。
DELETE FROM quota WHERE dim = 'UPSTREAM';

-- 2. 去掉额度上限与预留列及其非负约束（额度限制已取消）。
ALTER TABLE quota DROP CONSTRAINT IF EXISTS quota_nonneg;
ALTER TABLE quota DROP COLUMN IF EXISTS total;
ALTER TABLE quota DROP COLUMN IF EXISTS reserved;
ALTER TABLE quota ADD CONSTRAINT quota_nonneg CHECK (used_or_committed >= 0);

-- 3. 台账不再区分维度②是否计数。
ALTER TABLE billing_ledger DROP COLUMN IF EXISTS counted_upstream;
