-- 经济能力查询转接服务 — 初始 schema (DESIGN §11)
-- 方言：PostgreSQL（MySQL 可将 BIGSERIAL→BIGINT AUTO_INCREMENT, TIMESTAMPTZ→DATETIME）

-- §11.1 license：总量买断，无周期重置字段
CREATE TABLE license (
    license_id     VARCHAR(64)  PRIMARY KEY,
    app_key        VARCHAR(64)  NOT NULL UNIQUE,
    app_secret_enc VARCHAR(512) NOT NULL,            -- HMAC 密钥，加密存储 (§11.4)
    client_uuid    VARCHAR(64)  NOT NULL,            -- 用于 requestId 生成与对账
    status         VARCHAR(16)  NOT NULL DEFAULT 'ACTIVE', -- ACTIVE|SUSPENDED|EXPIRED
    valid_from     TIMESTAMPTZ  NOT NULL,
    valid_to       TIMESTAMPTZ  NOT NULL,            -- 仅作授权有效期，不做周期重置
    rate_limit     JSONB        NOT NULL DEFAULT '{}'::jsonb, -- QPS/并发
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- §11.2 quota：dim 区分两维度 (SERVICE=维度①, UPSTREAM=维度②)
--   维度① 剩余 = total - used_or_committed
--   维度② 剩余 = total - used_or_committed(committed) - reserved
CREATE TABLE quota (
    license_id        VARCHAR(64) NOT NULL REFERENCES license(license_id),
    dim               VARCHAR(16) NOT NULL,          -- SERVICE | UPSTREAM
    total             BIGINT      NOT NULL DEFAULT 0,
    used_or_committed BIGINT      NOT NULL DEFAULT 0,
    reserved          BIGINT      NOT NULL DEFAULT 0, -- 仅 UPSTREAM 使用
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (license_id, dim),
    CONSTRAINT quota_nonneg CHECK (used_or_committed >= 0 AND reserved >= 0)
);

-- §11.3 billing_ledger：追加写，无 UNKNOWN 状态 (§7.3/决策4)
CREATE TABLE billing_ledger (
    id               BIGSERIAL   PRIMARY KEY,
    app_key          VARCHAR(64) NOT NULL,
    reqid            VARCHAR(32) NOT NULL,           -- 客户幂等键
    request_id       VARCHAR(64) NOT NULL,           -- 全链路追踪 ID (§9)
    upstream_logid   VARCHAR(64),
    upstream_uid     VARCHAR(64),
    upstream_code    VARCHAR(8),
    state            VARCHAR(16) NOT NULL,           -- PENDING|BILLED|UNBILLED
    counted_service  BOOLEAN     NOT NULL DEFAULT FALSE,
    counted_upstream BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at       TIMESTAMPTZ,
    CONSTRAINT uq_ledger_appkey_reqid UNIQUE (app_key, reqid)
);

CREATE INDEX idx_ledger_request_id ON billing_ledger (request_id);
CREATE INDEX idx_ledger_state      ON billing_ledger (state);     -- 复查/对账扫描

-- 维度②"检查并预留"原子条件更新模板 (DESIGN §7.5 方案 B)：
--   UPDATE quota SET reserved = reserved + 1
--    WHERE license_id = $1 AND dim = 'UPSTREAM'
--      AND total - used_or_committed - reserved > 0;
--   -- 按受影响行数判断预留是否成功；为 0 即达上限。
