-- 管理后台 — schema (DESIGN §16.5)
-- 方言：PostgreSQL（MySQL 可将 BIGSERIAL→BIGINT AUTO_INCREMENT, TIMESTAMPTZ→DATETIME）
-- v0.7：移除全局 IP 白名单表（IP 准入交由阿里云 ECS 安全组）。

-- §16.1 管理员账号
CREATE TABLE admin_user (
    id            BIGSERIAL   PRIMARY KEY,
    username      VARCHAR(64) NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,           -- 加盐哈希；生产应换 bcrypt/argon2
    role          VARCHAR(16) NOT NULL DEFAULT 'ADMIN',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- §16.3/§16.5 审计日志（追加写）。client_ip 仅作来源记录，非准入控制。
CREATE TABLE audit_log (
    id               BIGSERIAL   PRIMARY KEY,
    request_id       VARCHAR(64) NOT NULL,         -- 全链路追踪 ID (= head.logId)
    app_key          VARCHAR(64) NOT NULL,
    trade_no         VARCHAR(64),
    reqid            VARCHAR(32),
    client_ip        VARCHAR(64),
    called_upstream  BOOLEAN     NOT NULL DEFAULT FALSE, -- 是否成功调用上游
    found_data       BOOLEAN     NOT NULL DEFAULT FALSE, -- 是否查得数据 (busiCode 10)
    busi_code        INT,
    busi_msg         VARCHAR(128),
    upstream_code    VARCHAR(8),
    upstream_uid     VARCHAR(64),
    upstream_logid   VARCHAR(64),
    billed           BOOLEAN     NOT NULL DEFAULT FALSE, -- 是否计维度①（成功查得数）
    latency_ms       BIGINT,
    name_mask        VARCHAR(64),                  -- 脱敏入参 (§11.5/§16.6)
    id_card_mask     VARCHAR(32),
    mobile_mask      VARCHAR(20),
    err_msg          VARCHAR(256),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_request_id ON audit_log (request_id);
CREATE INDEX idx_audit_app_key    ON audit_log (app_key);
CREATE INDEX idx_audit_busi_code  ON audit_log (busi_code);
