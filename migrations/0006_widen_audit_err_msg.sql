-- 0006: 放宽 audit_log.err_msg (VARCHAR(256) → VARCHAR(1024))
-- 聚合失败等场景的摘要仍可能较长；0005 若已应用则本迁移单独补 err_msg 列宽。

ALTER TABLE audit_log
    ALTER COLUMN err_msg TYPE VARCHAR(1024);
