-- 经济能力查询转接服务 — 初始 schema (DESIGN §11)
-- 方言：PostgreSQL（MySQL 可将 BIGSERIAL→BIGINT AUTO_INCREMENT, TIMESTAMPTZ→DATETIME）
-- v0.7：license 增加 mobile / secret_created_at；移除每用户 IP 白名单
--       (IP 准入交由阿里云 ECS 安全组)。quota 仅保留 SERVICE 维度。

-- §11.1 license：总量买断，无周期重置字段
CREATE TABLE license (
    license_id        VARCHAR(64)  PRIMARY KEY,
    app_key           VARCHAR(64)  NOT NULL UNIQUE,     -- 网关分配给客户的公开标识 appKey
    app_secret_enc    VARCHAR(512) NOT NULL,            -- 客户 MD5 加签 appSecret，加密存储 (§8.1/§11.4)
    client_uuid       VARCHAR(64)  NOT NULL,            -- 用于 requestId 生成与对账
    name              VARCHAR(128) NOT NULL DEFAULT '', -- 商户展示名/备注 (§16.2)
    mobile            VARCHAR(32)  NOT NULL DEFAULT '', -- 联系手机号 (后台脱敏展示)
    status            VARCHAR(16)  NOT NULL DEFAULT 'ACTIVE', -- ACTIVE|SUSPENDED|EXPIRED
    valid_from        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    valid_to          TIMESTAMPTZ  NOT NULL,            -- 授权过期日期，不做周期重置
    secret_created_at TIMESTAMPTZ  NOT NULL DEFAULT now(), -- 当前密钥创建/轮换时间
    rate_limit        JSONB        NOT NULL DEFAULT '{}'::jsonb, -- QPS/并发
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- §11.2 quota：v0.6 起仅保留 SERVICE 维度，used_or_committed = 累计成功查得数。
CREATE TABLE quota (
    license_id        VARCHAR(64) NOT NULL REFERENCES license(license_id),
    dim               VARCHAR(16) NOT NULL,          -- 仅 SERVICE
    used_or_committed BIGINT      NOT NULL DEFAULT 0,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (license_id, dim),
    CONSTRAINT quota_nonneg CHECK (used_or_committed >= 0)
);

-- §11.3 billing_ledger：追加写，无 UNKNOWN 状态 (§7.3/决策4)
CREATE TABLE billing_ledger (
    id               BIGSERIAL   PRIMARY KEY,
    app_key          VARCHAR(64) NOT NULL,
    trade_no         VARCHAR(64),                    -- 预留（当前下游无 tradeNo）
    reqid            VARCHAR(32) NOT NULL,           -- 内部派生的上游幂等键 (≤20)
    request_id       VARCHAR(64) NOT NULL,           -- 全链路追踪 ID (§9, = head.logId)
    upstream_logid   VARCHAR(64),
    upstream_uid     VARCHAR(64),
    upstream_code    VARCHAR(8),
    busi_code        INT,                            -- 内部业务码 (便于按 10/1000 核对维度①)
    state            VARCHAR(16) NOT NULL,           -- PENDING|BILLED|UNBILLED
    counted_service  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at       TIMESTAMPTZ,
    CONSTRAINT uq_ledger_appkey_reqid UNIQUE (app_key, reqid)
);

CREATE INDEX idx_ledger_request_id ON billing_ledger (request_id);
CREATE INDEX idx_ledger_state      ON billing_ledger (state);
