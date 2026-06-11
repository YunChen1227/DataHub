# DataHub — 经济能力查询转接服务（Go）

接口转接网关：对外提供经济能力查询 API（对齐《伽马分层分_定制版》PDF：`POST /enol/api/v1/doCheck`、
信封 `appId/sign/apiKey/encryptionType/body` + **MD5 加签**、响应 `code/msg/seqNo/data{busiCode,busiMsg,result{score}}`），
在此基础上提供 **License 鉴权** 与 **双维度配额（计费）**；对内调用上游数据源 `income_cls`（MD5 签名）。
设计见 [`docs/DESIGN.md`](docs/DESIGN.md)，架构图见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 目录结构（六边形分层）

```
cmd/relay/                 # 入口：装配各层 + 启动 HTTP/后台任务
internal/
├── api/                   # 接入层：requestId/clientIP 中间件、信封/签名提取、handler、admin API + JWT 中间件 + SPA 托管
├── application/           # 编排层：QueryOrchestrator（主流程 + 审计写入 + 每用户 IP 校验）
├── domain/                # 领域层（无框架依赖）
│   ├── model/             #   核心类型（共享，零依赖；含 admin/审计/用户视图）
│   ├── port/              #   出站接口（仓储/上游/密钥/admin/审计/IP 等"端口"）
│   ├── auth/              #   License 鉴权 + appId/apiKey 校验 + MD5 加签验签
│   ├── quota/             #   双维度配额：预留→结算
│   ├── billing/           #   计费判定表 + 状态机
│   ├── parse/             #   参数校验/规范化
│   ├── mapping/           #   上游结果→客户响应
│   └── admin/             #   管理后台：登录/用户CRUD/配额/密钥轮换/IP 白名单
├── infrastructure/        # 适配器
│   ├── upstream/          #   上游 HTTP 客户端 + MD5 签名 + 幂等复查
│   ├── persistence/memory #   开发用内存实现（含 admin/审计/全局IP；生产换 Redis+Lua / 关系库）
│   └── secret/            #   密钥提供者（按 licenseId 动态读取；生产换 KMS/Vault）
├── job/                   # 异步复查 worker + 对账兜底任务
└── common/                # errs(错误码) / reqid / appctx / jwt / ipfilter / mask
web/admin/                 # 管理后台 React + Vite SPA（DESIGN §16）
migrations/                # 建表 DDL（PostgreSQL）：0001 业务 / 0002 管理后台
```

依赖箭头始终指向内层：`api → application → domain ← infrastructure`。

## 运行（开发）

> 需要先安装 Go 1.22+（本机当前未检测到 `go`）。

```bash
go run ./cmd/relay
# 健康检查
curl http://localhost:8080/healthz
```

开发态使用内存适配器并预置一个 demo license：`appId=y89098io`，`secret=demo-app-secret`，
`apiKey=gama_ctmz_layer_score`（固定产品编号），两个维度各 100000 额度。可用环境变量覆盖配置（见 `cmd/relay/config.go`）。

请求示例（MD5 加签见 DESIGN §8.1：对 `body` 非空业务参数按 ASCII 升序拼接后追加 `secret` 再 MD5）：

```bash
curl -X POST http://localhost:8080/enol/api/v1/doCheck \
  -H 'Content-Type: application/json' \
  -d '{
    "encryptionType": 1,
    "appId": "y89098io",
    "sign": "<MD5(idCard...mobile...name...tradeNo...secret)>",
    "apiKey": "gama_ctmz_layer_score",
    "body": {
      "name": "张XX",
      "idCard": "330xxxxxxxx4312",
      "mobile": "138xxxx1009",
      "tradeNo": "025b8f36fc72dce"
    }
  }'
```

成功响应：`{"code":0,"msg":"请求成功","seqNo":"<requestId>","data":{"busiCode":10,"busiMsg":"success","result":{"score":"7"}}}`。

## 管理后台（Admin Console，DESIGN §16）

面向我方运营的内部控制台：① 查看用户操作记录与上下游日志；② 增删用户、配置配额；③ 生成/轮换鉴权 `appId+secret`；④ 配置全局/每用户 IP 白名单。

- **后端 API**：`/admin/api/**`（除 `/admin/api/login` 外均需 `Authorization: Bearer <JWT>`）。
- **初始管理员**：环境变量 `ADMIN_BOOTSTRAP_USER`/`ADMIN_BOOTSTRAP_PASS` 引导（开发默认 `admin` / `admin12345`）。
  其它：`ADMIN_JWT_SECRET`、`ADMIN_TOKEN_TTL`（默认 8h）、`ADMIN_SPA_DIR`（默认 `web/admin/dist`）。

前端（React + Vite SPA）：

```bash
cd web/admin
npm install
# 开发模式（:5173，自动代理 /admin/api → :8080）
npm run dev          # 打开 http://localhost:5173/admin/
# 或构建静态产物，由 Go 服务在 /admin/ 托管
npm run build        # 产物输出到 web/admin/dist；访问 http://localhost:8080/admin/
```

> 安全：`secret` 仅创建/轮换时一次性返回；审计入参（name/idCard/mobile）一律脱敏存储；管理后台与 SPA 应仅限内网访问。开发期密码用加盐 SHA-256，生产应换 bcrypt/argon2。

## 实现现状（骨架）

- ✅ 完整分层骨架、端口/适配器、PDF 信封（`appId/sign/apiKey/body` + MD5 加签）、busiCode 业务码、状态机、配额预留/结算、requestId 追踪（响应 `seqNo`）、建表 DDL。
- ✅ 管理后台：管理员登录（JWT）、用户 CRUD + 配额、`appId/secret` 生成与轮换、审计日志（成功调用/查得数据/上下游 code/uid + 脱敏入参）、全局 + 每用户 IP 白名单、React+Vite SPA。
- 🚧 待联调确认（DESIGN §15.1）：
  - `UpstreamClient.Requery` 上游单笔复查接口（§15.1.3）——当前返回 `Reachable=false`，记录留待对账。
  - `ReconciliationJob.tick` 对账单拉取与补计/冲正逻辑（§7.6）。
  - 生产持久化：将 `persistence/memory` 换成 Redis+Lua（配额原子）+ 关系库（台账）。
  - MD5 加签拼接顺序/hex 大小写已按 PDF 固定（ASCII 升序、小写 hex、`tradeNo` 参与，§8.1）；`encryptionType` 密文支持待定（§15.1.1）。
```
