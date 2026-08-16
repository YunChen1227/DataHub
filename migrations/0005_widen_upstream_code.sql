-- 0005: 放宽 upstream_code 长度 (VARCHAR(8) → VARCHAR(64))
-- 部分上游平台码较长（如 E1009）、聚合失败码 aggregate_all_failed、产品状态码等
-- 均可超过 8 字符；旧列宽导致审计/台账 INSERT 失败 (SQLSTATE 22001)。

ALTER TABLE billing_ledger
    ALTER COLUMN upstream_code TYPE VARCHAR(64);

ALTER TABLE audit_log
    ALTER COLUMN upstream_code TYPE VARCHAR(64);
