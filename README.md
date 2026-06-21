# DataHub — 经济能力查询转接服务（Go）

接口转接网关（当前服务版本 **x1**）：
- **对外（下游，x1）**：`POST /v1/openapi/zlx/querySrmxX1`，网关信封 `appKey/sign/encryptionType/body` + **MD5 加签**，
  响应 `head{errorCode,logId,time,errorMsg,timestamp} / body{code,msg,uid,reqid,verify,result{range}}`；在此基础上提供 **License 鉴权** 与 **成功查得数统计**（无额度限制）。
- **对外（下游，旧版 v9 兼容）**：`GET /yrzx/finan/net/10w/v9`（`docs/income_cls.md`：`account/key` 验签，响应 `code/msg/uid/result.range/verify`），供老客户使用；与 x1 **共用同一上游/鉴权(account=appKey、key=appSecret)/统计口径**，仅对外协议不同。

> **额度策略（v0.6+）**：已**取消额度限制**——不限制客户调用次数；系统仅**统计每个用户累计成功查得数据的次数**（上游 001 → busiCode 10）。维度②（上游配额/调用计数/对账作业）已在 v0.7 **彻底移除**。

> **IP 准入（v0.7）**：网关**不再**做全局/每用户 IP 白名单校验；来源 IP 仅写入审计日志。生产环境由**阿里云 ECS 安全组**等网络层控制访问。

- **对内（上游，唯一）**：**伽马分层分**（《伽马分层分_定制版》PDF：`POST /enol/api/v1/doCheck`，MD5 加签信封），产出"经济能力评分"。保留 `upstream.Router` 抽象以便未来扩展，当前仅注册伽马。

设计见 [`docs/DESIGN.md`](docs/DESIGN.md)，架构图见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 目录结构（六边形分层）

```
cmd/relay/                 # 入口：装配各层 + 启动 HTTP/后台任务
internal/
├── api/                   # 接入层：requestId/clientIP 中间件、信封/签名提取、handler、admin API + JWT 中间件 + SPA 托管
├── application/           # 编排层：QueryOrchestrator（主流程 + 审计写入）
├── domain/                # 领域层（无框架依赖）
│   ├── model/             #   核心类型（共享，零依赖；含 admin/审计/用户视图）
│   ├── port/              #   出站接口（仓储/上游/密钥/admin/审计等"端口"）
│   ├── auth/              #   License 鉴权 + appKey 校验 + MD5 加签验签
│   ├── quota/             #   成功查得数统计 + 台账 PENDING→BILLED/UNBILLED（无额度限制）
│   ├── billing/           #   计费判定表 + 状态机
│   ├── parse/             #   参数校验/规范化
│   ├── mapping/           #   上游结果→客户 head/body 响应 + errorCode
│   └── admin/             #   管理后台：登录/用户 CRUD/密钥轮换/审计查询
├── infrastructure/        # 适配器
│   ├── upstream/          #   上游路由 + 伽马客户端 + MD5 签名
│   ├── persistence/memory #   开发用内存实现（默认）
│   ├── persistence/postgres # 生产：license/台账/审计/管理员（PostgreSQL）
│   ├── persistence/redis  #   生产：成功查得数原子计数（Redis INCR + PG 镜像）
│   └── secret/            #   密钥提供者（按 licenseId 动态读取）
├── job/                   # 异步复查 worker（RequeryWorker；伽马 Requery 当前为 stub）
└── common/                # errs(错误码) / reqid / appctx / jwt / ipfilter(仅解析 IP) / mask
web/admin/                 # 管理后台 React + Vite SPA（DESIGN §16）
migrations/                # 建表 DDL（PostgreSQL）：0001 业务 / 0002 管理后台
scripts/                   # mock_gama、e2e、recreate_databases 等辅助脚本
test/                      # 固定测试套件（run.ps1 + cases/*.go）
```

依赖箭头始终指向内层：`api → application → domain ← infrastructure`。

## 运行（开发）

需要 Go 1.22+。

```bash
# 默认：无 config.yaml 时使用 memory 适配器
go run ./cmd/relay

# 推荐：本地内存 + mock 上游
CONFIG_FILE=config.local.mem.yaml go run ./cmd/relay

# 另开终端启动 mock 伽马（:9112）
go run ./scripts/mock_gama.go

# 健康检查
curl http://localhost:8080/healthz
```

开发态（memory 或 PG seed）预置 demo license：`appKey=y89098io`，`secret=demo-app-secret`（无额度限制，仅统计成功查得数）。
上游唯一为 **伽马**（`upstream.provider: gama`），需在配置文件中设置 `upstream.gama.baseURL`/`appId`/`appSecret`/`apiKey`（见 `config.example.yaml`）。

### 配置文件

通过环境变量 `CONFIG_FILE` 指定 YAML（默认读取仓库根目录 `config.yaml`，该文件已被 `.gitignore` 忽略）：

| 文件 | 用途 |
|---|---|
| `config.example.yaml` | 模板 |
| `config.local.mem.yaml` | 本地 memory + mock gama |
| `config.aliyun.e2e.yaml` | 阿里云 PG `dev_db` + Redis db0 + mock gama（e2e） |
| `config.aliyun.prod.yaml` | 阿里云 PG `prod_db` + Redis db1（生产） |

`storage.driver`：`memory`（默认）| `postgres`（PG + Redis）。

### 请求示例

下游 MD5 加签见 DESIGN §8.1：对 `body` 非空业务参数按 ASCII 升序拼接后追加 `secret` 再 MD5。

```bash
curl -X POST http://localhost:8080/v1/openapi/zlx/querySrmxX1 \
  -H 'Content-Type: application/json' \
  -d '{
    "encryptionType": 1,
    "appKey": "y89098io",
    "sign": "<MD5(idCard...mobile...name...secret)>",
    "body": {
      "mobile": "138xxxx1009",
      "idCard": "330xxxxxxxx4312",
      "name": "张三"
    }
  }'
```

成功响应：`{"head":{"errorCode":"0","logId":"<requestId>","time":81,"errorMsg":"success","timestamp":...},"body":{"code":"001","msg":"成功","uid":"...","reqid":"...","verify":"","result":{"range":"7"}}}`。
查无：`head.errorCode="0"` + `body.code="999"`；网关级错误（鉴权/参数）：只返回 `head`（`errorCode` 非 0 + `errorMsg`），无 `body`。

## 管理后台（Admin Console，DESIGN §16）

面向我方运营的内部控制台：① 查看用户操作记录与上下游日志、累计成功查得数；② 增删用户（无额度配置）；③ 生成/轮换鉴权 `appKey+secret`；④ 按 uuid(appKey)/名称/手机号检索用户与审计记录。

- **后端 API**：`/admin/api/**`（除 `/admin/api/login` 外均需 `Authorization: Bearer <JWT>`）。
- **初始管理员**：配置文件 `admin.bootstrapUser` / `admin.bootstrapPass`（**非**环境变量；e2e 默认 `admin` / `admin12345`）。
  其它：`admin.jwtSecret`、`admin.tokenTTL`（默认 8h）、`admin.spaDir`（默认 `web/admin/dist`）。
- **用户字段**：名称、手机号（列表脱敏展示）、密钥创建时间、授权过期日期（`validTo`）、累计成功查得数。
- **无 IP 白名单管理页**（v0.7 已移除）。

前端（React + Vite SPA）：

```bash
cd web/admin
npm install
# 开发模式（:5173，自动代理 /admin/api → :8080）
npm run dev          # 打开 http://localhost:5173/admin/
# 或构建静态产物，由 Go 服务在 /admin/ 托管
npm run build        # 产物输出到 web/admin/dist；访问 http://localhost:8080/admin/
```

> 安全：`secret` 仅创建/轮换时一次性返回；审计入参（name/idCard/mobile）一律脱敏存储；管理后台应仅限内网/受控网络访问（网络层由 ECS 安全组等控制）。开发期密码用加盐 SHA-256，生产应换 bcrypt/argon2。

## 实现现状

- ✅ 下游契约（x1：`/v1/openapi/zlx/querySrmxX1`、`appKey/sign/encryptionType/body` + MD5 加签、`head/body` 信封、`errorCode` 映射）。
- ✅ 旧版 v9 兼容（`GET /yrzx/finan/net/10w/v9`：`account/key` 验签、`code/result.range` 响应；复用同一上游/鉴权/统计）。
- ✅ 上游：唯一伽马，归一化为 `UpstreamResult`（`001`查得/`999`查无）；保留 `upstream.Router` 抽象便于扩展。
- ✅ 成功查得数统计（**仅查得数据 busiCode=10 计数**，无额度拦截）、台账状态机、requestId 追踪（`head.logId`）、建表 DDL。
- ✅ 持久化：`memory`（开发）与 `postgres`+`redis`（生产/e2e）；`dev_db` / `prod_db` 同实例隔离。
- ✅ 管理后台：管理员登录（JWT）、用户 CRUD（手机号/密钥时间/过期日期、检索）、`appKey/secret` 生成与轮换、审计日志（含 `?q=` 关键字过滤）、React+Vite SPA。
- ✅ 固定测试套件（`test/run.ps1`，7 个 case；结果输出 `test_res/<date>/`）。
- 🚧 待完善：
  - 伽马 `Requery` 当前为 stub（`Reachable=false`），RequeryWorker 对伽马上游暂无实际复查能力。
  - `license.valid_to` 已存储并在后台展示，鉴权目前仅检查 `status==ACTIVE`（未按日期自动过期）。
  - `license.rate_limit` 列存在但代码未读取。
  - 密钥列 `app_secret_enc` 开发/e2e 为明文存储（生产应接入 KMS/加密）。

## 测试

```powershell
powershell -ExecutionPolicy Bypass -File .\test\run.ps1
powershell -ExecutionPolicy Bypass -File .\test\run.ps1 -ConfigFile config.local.mem.yaml
powershell -ExecutionPolicy Bypass -File .\test\run.ps1 -SkipReal   # 跳过真实 gama 冒烟
```

详见 [`test/README.md`](test/README.md)。
