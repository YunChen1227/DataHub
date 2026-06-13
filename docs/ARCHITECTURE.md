# 经济能力查询转接服务 — 架构图（ARCHITECTURE.md / 指导代码生成）

> 配套文档：业务/决策口径见 [`DESIGN.md`](./DESIGN.md)；下游契约见《接口文档 - 经济能力》；上游契约见 [`income_cls.md`](./income_cls.md) 与《伽马分层分_定制版》PDF。
> 本文目标：把设计落成**可直接指导代码生成**的结构图——包/模块边界、类与接口、调用链方法签名、状态机、数据模型、组件↔代码映射。

> **v0.4 拓扑**：下游对客户 = `POST /v1/openapi/zlx/querySrmxV9`（信封 `appKey/sign/encryptionType/body`，响应 `head/body`）。上游经 `upstream.Router` 路由到 **GamaClient（默认）/ IncomeClsClient**，由 `UPSTREAM_PROVIDER` 选择；两者实现同一 `UpstreamPort`，归一化为 `UpstreamResult`（`001`查得/`999`查无）。`head.errorCode` 由 `errs.ErrorCode(busiCode)` 映射。

---

## 0. 阅读指引

| 你想生成 | 看本文第几节 |
|---|---|
| 工程目录 / 包结构 | §1 分层与包结构 |
| 各层有哪些类/接口、方法签名 | §2 类与接口图 |
| 一次查询的代码调用链 | §3 调用链（带方法名） |
| 计费状态机 → BillingService 实现 | §4 计费状态机 |
| 建表 / ORM 实体 | §5 数据模型（ER） |
| 异步复查 + 对账兜底 | §6 异步与对账 |
| 组件落到哪个类/文件 | §7 组件↔代码映射表 |

---

## 1. 分层与包结构（Package / Module）

```mermaid
flowchart TB
    subgraph api["api 接入层 (controller/filter)"]
        Ctl["QuerySrmxV9Handler\nQuotaHandler"]
        Filter["RequestIdFilter\nSignatureFilter"]
    end

    subgraph app["application 编排层 (service/orchestrator)"]
        Orch["QueryOrchestrator\n(主流程编排)"]
    end

    subgraph domain["domain 领域层 (核心业务，无框架依赖)"]
        AuthSvc["AuthService\n(License/appKey/MD5签名校验)"]
        QuotaSvc["QuotaService\n(维度①②预留/结算)"]
        BillSvc["BillingService\n(计费判定/状态机)"]
        Parser["RequestParser\n(参数校验/规范化)"]
        Mapper["ResponseMapper\n(上游→客户 head/body)"]
    end

    subgraph infra["infrastructure 基础设施层"]
        UpClient["UpstreamClient\n(HTTP GET+MD5签名+复查)"]
        QuotaRepo["QuotaRepository\n(Redis+Lua / DB)"]
        LedgerRepo["LedgerRepository\n(台账追加写)"]
        LicenseRepo["LicenseRepository"]
        Secrets["SecretProvider\n(KMS/Vault)"]
    end

    subgraph job["job 异步/定时层"]
        RequeryWorker["RequeryWorker\n(异步幂等复查)"]
        ReconJob["ReconciliationJob\n(对账兜底)"]
    end

    Ctl --> Orch
    Filter -.-> Ctl
    Orch --> AuthSvc
    Orch --> QuotaSvc
    Orch --> Parser
    Orch --> UpClient
    Orch --> BillSvc
    Orch --> Mapper

    AuthSvc --> LicenseRepo
    AuthSvc --> Secrets
    QuotaSvc --> QuotaRepo
    QuotaSvc --> LedgerRepo
    BillSvc --> LedgerRepo
    UpClient --> Secrets

    RequeryWorker --> UpClient
    RequeryWorker --> BillSvc
    RequeryWorker --> QuotaSvc
    ReconJob --> UpClient
    ReconJob --> LedgerRepo
    ReconJob --> QuotaSvc
```

**包命名建议（语言无关，Java 示例）**

```
com.datahub.relay
├── api            // controller, filter, dto(request/response)
├── application    // QueryOrchestrator（事务/流程编排）
├── domain
│   ├── auth       // AuthService, SignatureVerifier
│   ├── quota      // QuotaService, 配额聚合
│   ├── billing    // BillingService, BillingDecisionTable, 状态机
│   ├── parse      // RequestParser
│   └── mapping    // ResponseMapper
├── infrastructure
│   ├── upstream   // UpstreamClient, UpstreamSigner(MD5)
│   ├── persistence// *Repository 实现 (Redis/MyBatis/JPA)
│   └── secret     // SecretProvider
├── job            // RequeryWorker, ReconciliationJob
└── common         // RequestId 生成, 错误码枚举, 日志/MDC, 异常
```

---

## 2. 类与接口图（Class Diagram）

```mermaid
classDiagram
    class QueryOrchestrator {
        +QueryResult handle(QueryCommand cmd, RequestContext ctx)
    }

    class AuthService {
        +LicenseView authenticate(SignedRequest req)
    }
    class SignatureVerifier {
        <<interface>>
        +boolean verify(SignedRequest req, String secret)
    }
    class Md5Verifier {
        +boolean verify(SignedRequest req, String secret)
    }

    class QuotaService {
        +void checkServiceQuota(licenseId) ServiceQuotaError
        +ReserveToken reserveUpstream(licenseId, reqid)
        +void settle(ReserveToken t, BillingDecision d)
    }
    class BillingService {
        +BillingDecision decide(UpstreamResult r)
        +BillingState onRequeryResult(reqid, RequeryResult rr)
    }
    class BillingDecisionTable {
        +boolean isCharged(String upstreamCode)
    }

    class UpstreamClient {
        +UpstreamResult query(UpstreamRequest req)
        +RequeryResult requery(String reqid)
    }
    class UpstreamSigner {
        +String sign(UpstreamRequest req, String account, String key)
    }

    class RequestParser {
        +UpstreamRequest parse(QueryCommand cmd)
    }
    class ResponseMapper {
        +QueryResult toClientResponse(UpstreamResult r, RequestContext ctx)
    }

    class LedgerRepository {
        <<interface>>
        +Ledger findByReqid(appId, reqid)
        +void append(Ledger l)
        +void updateState(id, BillingState s)
    }
    class QuotaRepository {
        <<interface>>
        +boolean tryReserve(licenseId)
        +void commit(licenseId)
        +void release(licenseId)
        +void incServiceUsed(licenseId)
    }

    QueryOrchestrator --> AuthService
    QueryOrchestrator --> QuotaService
    QueryOrchestrator --> RequestParser
    QueryOrchestrator --> UpstreamClient
    QueryOrchestrator --> BillingService
    QueryOrchestrator --> ResponseMapper
    AuthService --> SignatureVerifier
    SignatureVerifier <|.. Md5Verifier
    BillingService --> BillingDecisionTable
    BillingService --> LedgerRepository
    QuotaService --> QuotaRepository
    QuotaService --> LedgerRepository
    UpstreamClient --> UpstreamSigner
```

---

## 3. 调用链（带方法名的主流程）

> 对应 `DESIGN.md §4`，此处标注**类.方法()**，可直接据此生成实现。

```mermaid
sequenceDiagram
    participant F as RequestIdFilter / SignatureFilter
    participant O as QueryOrchestrator
    participant A as AuthService
    participant Q as QuotaService
    participant P as RequestParser
    participant U as UpstreamClient
    participant B as BillingService
    participant M as ResponseMapper

    F->>F: 生成 requestId(=seqNo), 放入 MDC/Context
    F->>O: handle(cmd, ctx)
    O->>A: authenticate(signedReq)
    Note right of A: 失败→抛 AuthException(busiCode 1003/1004/1005/1009)
    O->>Q: checkServiceQuota(licenseId)
    Note right of Q: 无余额→抛 QuotaException(busiCode 1001)
    O->>Q: token = reserveUpstream(licenseId, reqid)
    Note right of Q: 幂等命中BILLED→直接返回缓存结果
    O->>P: upReq = parse(cmd)  %% tradeNo→reqid
    O->>U: result = query(upReq)
    alt 收到业务响应
        U-->>O: UpstreamResult(code, range)
    else 超时/无响应
        U->>U: requery(reqid)  %% 幂等复查
        U-->>O: RequeryResult / 不可达→入异步队列
    end
    O->>B: decision = decide(result)  %% Charged + Returned(=busiCode 10)
    O->>Q: settle(token, decision)
    Note right of Q: BILLED→committed++<br/>仅 Returned(busiCode 10) 时 serviceUsed++<br/>UNBILLED→reserved--
    O->>M: resp = toClientResponse(result, ctx)
    M-->>F: DoCheckResponse(code/msg/seqNo/data)
```

---

## 4. 计费状态机（BillingService 实现依据）

> 对应 `DESIGN.md §7.3`。状态只有三态，`PENDING` 为中间态，**无 UNKNOWN 终态**。

```mermaid
stateDiagram-v2
    [*] --> PENDING: reserveUpstream() reserved++ + append(PENDING)
    PENDING --> BILLED: decide()=charged / requery=已扣费<br/>→ commit() committed++,reserved--；Returned(busiCode 10) 时 serviceUsed++
    PENDING --> UNBILLED: decide()=not_charged / requery=未扣费<br/>→ release() reserved--
    PENDING --> PENDING: 超时且复查不可达 → 入 RequeryWorker 队列
    note right of PENDING
      超期未结算 → ReconciliationJob
      以上游扣费记录强制裁决
    end note
    BILLED --> [*]
    UNBILLED --> [*]
```

> `Charged`（维度②，是否上游扣费）与 `Returned`（维度①，是否查得数据=busiCode 10）**可分离**：999 查无结果 `Charged=true, Returned=false`。

**判定表（`BillingDecisionTable`，应配置化，对应 §7.4）**

| 上游 code | isCharged(维度②) | busiCode | Returned(维度①) | 落地常量 |
|---|---|---|---|---|
| 001 | `true` | 10 | `true` | `CHARGED_CODES = {001, 999}` |
| 999 | `true` | 1000 | `false` | `RETURNED_CODES = {001}` |
| 003/002/004/012/013/005.. | `false` | 1007 | `false` | 其余一律 false + 触发告警 |

---

## 5. 数据模型（ER，对应 §11）

```mermaid
erDiagram
    LICENSE ||--|| QUOTA_SERVICE : has
    LICENSE ||--|| QUOTA_UPSTREAM : has
    LICENSE ||--o{ BILLING_LEDGER : owns

    LICENSE {
        string license_id PK
        string app_id UK
        string app_secret_enc
        string client_uuid
        string status "ACTIVE|SUSPENDED|EXPIRED"
        datetime valid_from
        datetime valid_to
        json rate_limit
    }
    QUOTA_SERVICE {
        string license_id FK
        long total
        long used
    }
    QUOTA_UPSTREAM {
        string license_id FK
        long total
        long committed
        long reserved
    }
    BILLING_LEDGER {
        long id PK
        string app_id
        string trade_no
        string reqid "UK(app_id,reqid)"
        string request_id "idx"
        string upstream_code
        int busi_code
        string upstream_uid
        string upstream_logid
        string state "PENDING|BILLED|UNBILLED"
        bool counted_service
        bool counted_upstream
        datetime created_at
        datetime settled_at
    }
```

- 维度①剩余 `= quota_service.total - used`
- 维度②剩余 `= quota_upstream.total - committed - reserved`
- 计数与预留必须**原子**（Redis+Lua 或 DB 条件更新），见 §7.5。

---

## 6. 异步复查与对账兜底（job 层）

```mermaid
flowchart LR
    subgraph RequeryWorker["RequeryWorker (异步队列消费)"]
        direction TB
        R1["取 PENDING 记录"] --> R2["UpstreamClient.requery(reqid)"]
        R2 --> R3{复查结论?}
        R3 -->|已扣费| RB["BillingService→BILLED<br/>QuotaService.commit()"]
        R3 -->|未扣费| RU["→UNBILLED, release()"]
        R3 -->|仍不可达| R4["重试/留待对账"]
    end

    subgraph ReconJob["ReconciliationJob (定时)"]
        direction TB
        C1["拉上游对账单"] --> C2["逐条比对 LedgerRepository"]
        C2 --> C3{差异?}
        C3 -->|上游扣费,本地未计| CB["强制 committed++ + 告警(漏计)"]
        C3 -->|本地计,上游无| CR["冲正 committed-- + 告警(空计)"]
        C3 -->|超期PENDING| CP["按上游记录裁决 + 清 reserved"]
    end
```

---

## 7. 组件 ↔ 代码模块映射表（生成代码时对号入座）

| DESIGN 章节 | 组件/职责 | 落地类（建议） | 关键依赖 |
|---|---|---|---|
| §3.1 网关 / §9 | 接入、requestId、签名入口 | `RequestIdFilter`, `SignatureFilter`, `QueryController`, `QuotaController` | `RequestIdGenerator`, `SignatureVerifier` |
| §8.1 | 客户侧 MD5 加签校验（appId + body 排序拼接 + secret） | `Md5Verifier`（实现 `SignatureVerifier`） | `LicenseRepository`, `SecretProvider` |
| §8.2 / §6 | 上游 MD5 签名 + 调用 | `UpstreamSigner`, `UpstreamClient` | HTTP 连接池/超时/熔断 |
| §7 / §6.3 | 双维度配额预留/结算 | `QuotaService`, `QuotaRepository` | Redis+Lua / DB 条件更新 |
| §7.3 / §7.4 | 计费判定与状态机 | `BillingService`, `BillingDecisionTable` | `LedgerRepository` |
| §5 / §6.1 | 参数校验、响应映射 | `RequestParser`, `ResponseMapper` | 错误码枚举 |
| §7.6 | 复查/对账兜底 | `RequeryWorker`, `ReconciliationJob` | `UpstreamClient`, `LedgerRepository` |
| §9 | 全链路追踪 | `RequestIdGenerator`, MDC/Context 注入 | 日志 pattern `[%X{requestId}]` |
| §5.3 | 网关错误码 | `GatewayErrorCode` 枚举 + 全局异常处理 | `@ControllerAdvice` / middleware |
| §11.4 | 密钥管理 | `SecretProvider`(KMS/Vault) | 加密列兜底 |

---

## 8. 业务码 → 异常 → 响应映射（生成全局异常处理依据）

> 对齐 PDF（§5.3）：业务态一律 `code=0`，成败在 `data.busiCode` 表达；仅**请求体无法解析 / 系统级异常**返回 `code=-1`（无 `data`）。HTTP 状态统一 `200`。

| busiCode | 含义 | 异常类（建议） | 计① | 计② | 触发点 |
|---|---|---|---|---|---|
| 10 | 查询成功【计费】 | —（正常流） | 是 | 是（上游扣费） | 上游 001 |
| 1000 | 数据未查得 | —（正常流） | 否 | 是（上游扣费） | 上游 999 |
| 1001 | 账户余额不足 | `ServiceQuotaExhaustedException` | 否 | 否 | `QuotaService.checkServiceQuota` |
| 1002 | 账户信息不存在 | `AccountNotFoundException` | 否 | 否 | `AuthService`（appId 查无 license） |
| 1003 | appId 异常 | `AppIdInvalidException` | 否 | 否 | `AuthService`（缺少/非法 appId） |
| 1004 | 产品编号异常 | `ProductInvalidException` | 否 | 否 | `AuthService`（apiKey ≠ 固定值） |
| 1005 | 账号信息异常 | `SignatureInvalidException` | 否 | 否 | `AuthService`（MD5 验签失败） |
| 1006 | 透支余额已达上限 | `UpstreamQuotaExhaustedException` | 否 | 否 | `QuotaService.reserveUpstream` |
| 1007 | 数据请求异常 | `ParamValidationException` / `UpstreamBusinessException` / `UpstreamNotExecutedException` | 否 | 否 | `RequestParser` / 判定表 `isCharged=false` / 复查确认未扣费 |
| 1009 | 服务尚未开通 | `LicenseInactiveException` | 否 | 否 | `AuthService`（license 停用/过期/未开通） |

| 全局 code | 含义 | 触发点 |
|---|---|---|
| 0 | 正常（含上述所有 busiCode 业务态） | 正常流 + 业务异常 |
| -1 | 响应异常 | 请求体不可解析 / 系统级未捕获异常 |

> 全局异常处理器把上述异常统一封装为 PDF 信封 `{code, msg, seqNo, data:{busiCode, busiMsg, result?}}`；`seqNo = requestId`（§9）。
```

---

## 9. 管理后台（Admin Console，对应 DESIGN §16）

### 9.1 模块与包结构（在原六边形分层上扩展）

```mermaid
flowchart TB
    subgraph spa["web/admin (React + Vite SPA)"]
        Pg["Login / Users / Audits / IpWhitelist 页面"]
        ApiCli["api.js (fetch + Bearer JWT)"]
    end

    subgraph api["api 接入层"]
        AdminCtl["AdminHandler\n(login / users / audits / ip-whitelist)"]
        AdminMw["AdminAuthMiddleware\n(JWT 校验)"]
        Static["SPA 静态托管 /admin"]
    end

    subgraph domain["domain 领域层"]
        AdminSvc["AdminService\n(登录/用户CRUD/配额/密钥轮换/IP)"]
        Cred["Credential\n(appId/secret 生成 + 密码哈希)"]
    end

    subgraph infra["infrastructure"]
        Store["memory.Store\n(+admin/audit/global-ip)"]
        DynSecret["StoreSecretProvider\n(动态读取用户 secret)"]
    end

    subgraph common["common"]
        JWT["jwt (HS256)"]
    end

    Pg --> ApiCli --> AdminCtl
    AdminMw -.-> AdminCtl
    AdminCtl --> AdminSvc
    AdminSvc --> Cred
    AdminSvc --> Store
    Cred --> JWT
    DynSecret --> Store
```

**包扩展**

```
internal/
├── api/            // +admin_handler.go, +admin_middleware.go（JWT 校验 / 静态托管）
├── domain/
│   └── admin/      // AdminService, Credential（appId/secret 生成、密码哈希）
├── common/
│   └── jwt/        // 最小 HS256 实现（零外部依赖）
└── infrastructure/
    ├── persistence/memory  // +admin_store.go（admin/audit/global-ip）
    └── secret              // +StoreSecretProvider（按 licenseId 读用户 secret）
web/admin/          // React + Vite 前端工程
```

### 9.2 审计写入链路（不侵入主流程口径）

```mermaid
sequenceDiagram
    participant F as RequestIdMiddleware (抓 clientIP)
    participant O as QueryOrchestrator
    participant AU as AuditRepository
    F->>O: handle(cmd) [ctx 带 requestId + clientIP]
    Note over O: 各分支(鉴权/配额/参数/上游/结算)结束前<br/>组装 AuditRecord（脱敏入参 + 上游code/uid + busiCode + 耗时）
    O->>AU: AppendAudit(rec)
    O-->>F: DoCheckResponse
```

> 审计与计费台账（§5 ER `BILLING_LEDGER`）以 `request_id` 关联；二者职责分离：台账管"钱"（计费状态），审计管"账"（可读操作记录 + 上下游日志）。

### 9.3 IP 白名单校验点

| 层级 | 位置 | 失败返回 |
|---|---|---|
| 全局白名单 | 业务入口（`doCheck`/`quota`）前置 | `code=-1`「IP 不在白名单」 |
| 每用户白名单 | `QueryOrchestrator` 鉴权后（已知 license） | `busiCode 1005 账号信息异常` |

### 9.4 新增数据模型（ER，对应 DESIGN §16.5）

```mermaid
erDiagram
    ADMIN_USER {
        long id PK
        string username UK
        string password_hash
        string role
        datetime created_at
    }
    AUDIT_LOG {
        long id PK
        string request_id "idx"
        string app_id "idx"
        string trade_no
        string reqid
        string client_ip
        bool called_upstream
        bool found_data
        int busi_code
        string busi_msg
        string upstream_code
        string upstream_uid
        string upstream_logid
        bool billed
        long latency_ms
        string name_mask
        string id_card_mask
        string mobile_mask
        string err_msg
        datetime created_at
    }
    LICENSE ||--o{ AUDIT_LOG : produces
```

> `LICENSE` 增加 `ip_whitelist text[]`（每用户白名单）；全局白名单存 `ip_whitelist_global`。

### 9.5 admin 组件 ↔ 代码映射

| DESIGN 章节 | 组件/职责 | 落地类/文件 |
|---|---|---|
| §16.1 | 管理员登录 / JWT | `AdminHandler.login`, `AdminAuthMiddleware`, `common/jwt` |
| §16.2 | 用户 CRUD / 配额 / 密钥 | `AdminService`, `Credential`, `memory.Store` |
| §16.3 | 审计查询 | `AdminService.ListAudits`, `AuditRepository`, `QueryOrchestrator`(写) |
| §16.4 | IP 白名单 | `AdminService`(global/per-user), 业务入口校验 |
| §16.0 | SPA | `web/admin`（Vite 构建产物托管于 `/admin`） |

