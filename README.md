# DataHub — 经济能力查询转接服务（Go）

接口转接网关：对外提供经济能力查询 API（License 鉴权 + HMAC-SHA256 签名 + 双维度配额），
对内调用上游数据源 `income_cls`（MD5 签名）。设计见 [`docs/DESIGN.md`](docs/DESIGN.md)，
架构图见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 目录结构（六边形分层）

```
cmd/relay/                 # 入口：装配各层 + 启动 HTTP/后台任务
internal/
├── api/                   # 接入层：requestId 中间件、签名提取、handler、统一响应
├── application/           # 编排层：QueryOrchestrator（主流程，无业务规则）
├── domain/                # 领域层（无框架依赖）
│   ├── model/             #   核心类型（共享，零依赖）
│   ├── port/              #   出站接口（仓储/上游/密钥等"端口"）
│   ├── auth/              #   License 鉴权 + HMAC-SHA256 验签
│   ├── quota/             #   双维度配额：预留→结算
│   ├── billing/           #   计费判定表 + 状态机
│   ├── parse/             #   参数校验/规范化
│   └── mapping/           #   上游结果→客户响应
├── infrastructure/        # 适配器
│   ├── upstream/          #   上游 HTTP 客户端 + MD5 签名 + 幂等复查
│   ├── persistence/memory #   开发用内存实现（生产换 Redis+Lua / 关系库）
│   └── secret/            #   密钥提供者（生产换 KMS/Vault）
├── job/                   # 异步复查 worker + 对账兜底任务
└── common/                # errs(错误码) / reqid(生成) / appctx(追踪传播)
migrations/                # 建表 DDL（PostgreSQL）
```

依赖箭头始终指向内层：`api → application → domain ← infrastructure`。

## 运行（开发）

> 需要先安装 Go 1.22+（本机当前未检测到 `go`）。

```bash
go run ./cmd/relay
# 健康检查
curl http://localhost:8080/healthz
```

开发态使用内存适配器并预置一个 demo license：`appKey=AC1001`，`appSecret=demo-app-secret`，
两个维度各 100000 额度。可用环境变量覆盖配置（见 `cmd/relay/config.go`）。

## 实现现状（骨架）

- ✅ 完整分层骨架、端口/适配器、错误码、状态机、配额预留/结算、requestId 追踪、建表 DDL。
- 🚧 待联调确认（DESIGN §15）：
  - `UpstreamClient.Requery` 上游单笔复查接口（§15.3）——当前返回 `Reachable=false`，记录留待对账。
  - `ReconciliationJob.tick` 对账单拉取与补计/冲正逻辑（§7.6）。
  - 生产持久化：将 `persistence/memory` 换成 Redis+Lua（配额原子）+ 关系库（台账）。
  - HMAC 字段拼接顺序/编码、时间戳精度与容差窗口最终联调固定（§8.1 / §15.1）。
```
