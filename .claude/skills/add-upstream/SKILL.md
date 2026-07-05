---
name: add-upstream
description: DataHub 新增上游接入（新增一条对外路由 + 对接一个新的信息提供商/数据源上游）的完整改动清单与操作流程。只要用户提到「新增上游」「接入上游」「对接新数据源/信息提供商」「新增一条路由」「加一个 querySrmxXXX 接口」「add a new upstream/provider/route」，即使没有明说要用 skill，也必须使用本 skill——它列出了全部需要修改的文件与锚点，照单执行即可，不要自行通读代码库。
---

# DataHub 新增上游接入

本项目是接口转接网关：每接入一个新上游（信息提供商），就新增一条对外路由做转发。
架构（v0.9，存储按「域」隔离：新路由独立成域；v8/v9 特例共用 v8v9 域）已把路由
注册、License 鉴权、管理后台、按路由统计、
持久化全部泛化到 `model.Versions` 列表上迭代，因此**新增上游是一份封闭的、可枚举
的改动清单**。唯一需要写"新逻辑"的地方是上游客户端的协议适配；其余全是照模板
追加条目。**严禁通读整个代码库**——只读本清单点名的文件。

若发现清单里的某个锚点与实际代码不符（函数改名、列表挪位置等），以代码为准，
在该文件内搜索既有路由名（如 `blk`）定位等价位置，并在完工后提醒用户更新本 skill。

## 第 0 步：向用户收集信息

开工前先确认以下信息，缺哪项问哪项（上游产品文档通常在 `docs/` 下有 PDF/MD）：

1. **路由名** `<route>`：小写字母数字（如 `zlf`、`blk`）。对外路径自动生成为
   `POST /v1/openapi/zlx/querySrmx<ROUTE>`（大写后缀）与 `GET .../quota<ROUTE>`。
2. **上游 kind 名** `<kind>`：上游客户端家族名（如 `rental`、`blacklist`），用作
   Go 文件名、Provider 常量、config 的 `kind` 值。
3. **上游协议细节**：endpoint、HTTP 方法、签名/加密方式、请求参数、响应结构、
   「查得 / 查无 / 上游侧错误」分别对应的响应码。
4. **凭证字段**：上游分配给我方的凭证有哪些字段（appId/secret/account/aesKey…）。
5. **result.range 透出口径**：下游 `body.result.range` 放什么——纯评分字符串
   （x1/zlf 模式），还是把上游富对象 JSON 序列化成字符串整体透出（blk 模式）。

## 架构不变量（改动时必须遵守）

- **对外契约冻结**：所有路由对外统一 x1 信封（`appKey/sign/encryptionType/body`
  + MD5 加签）与 `head/body` 响应，新上游的差异只允许体现在 `result.range` 的
  内容里。不要动 `internal/api/handler.go`、`mapping`、`auth`、`parse`。
- **存储按域隔离**（v0.9）：新路由默认独立成域（`model.RouteDomain` 缺省即
  路由名）——独立数据库 + 独立 Redis 逻辑库 + 独立 license/appKey/secret +
  独立统计/日志；仅 v8/v9 为历史特例共用 v8v9 域（同一套 license，统计/日志
  按路由分开）。跨域使用 license 一律鉴权失败。启动时 `checkStorageIsolation`
  会拒绝两个不同的域共用同一 DB 或同一 Redis 逻辑库的配置——分配存储时编号/
  库名不得复用。新增路由**不要**做共用域，除非用户明确要求（那时才在
  `RouteDomain()` 加 case，且不追加 `Domains` 条目）。
- **上游归一化**：上游客户端实现 `port.UpstreamPort`，把响应归一化为
  `model.UpstreamResult`：查得 → `Code:"001"`；查无 → `Code:"999"`；上游侧错误
  （账户/参数/系统问题）→ 返回 `error`（不计费，走复查/对账兜底）。
- **不要动** quota / billing / persistence / admin 后端——它们对路由完全泛化。

## 改动清单（按此顺序执行）

### A. 后端核心（必改）

1. **[internal/domain/model/model.go](internal/domain/model/model.go)**
   - `Versions` 列表末尾追加 `"<route>"`；
   - `Domains` 列表末尾追加 `"<route>"`（新路由独立成域；`RouteDomain` 缺省
     分支已返回路由名，无需改）；
   - `DemoAppKey()` 的 switch 加 `case "<route>":`，返回一个新的独立 demo
     appKey（惯例 8 位左右，如 `y8909zlf`/`y8909blk` 的风格，不得与既有重复）；
   - 顺手更新 `Versions` 上方注释里的路由枚举。

2. **[internal/infrastructure/upstream/router.go](internal/infrastructure/upstream/router.go)**
   - const 块追加 `Provider<Kind> = "<kind>"`。

3. **新建 `internal/infrastructure/upstream/<kind>.go`** —— 唯一的新逻辑。
   按协议形态选一个最接近的既有客户端整篇参考（先读它再动笔）：
   - JSON POST + MD5 信封（应诺尔系）→ [blacklist.go](internal/infrastructure/upstream/blacklist.go)
     （最简洁；可复用 `gamaEnvelope`/`signGama`，PII 摘要见 `encodePII`）；
   - AES 加密 biz_data + form 提交 → [rental.go](internal/infrastructure/upstream/rental.go)
     （AES 工具在 [aesecb.go](internal/infrastructure/upstream/aesecb.go)）；
   - GET + query 验签 → [income.go](internal/infrastructure/upstream/income.go)。
   结构固定为：`<Kind>Config` + `<Kind>Client` + `New<Kind>`（填默认值）+
   `Query`（归一化到 001/999/error）+ `Requery`（未联调前返回
   `&model.RequeryResult{Reachable: false}`，与既有上游一致）。
   富对象透出用 blacklist.go 里现成的 `compactJSON`。

4. **[cmd/relay/config.go](cmd/relay/config.go)**
   - 若需要新的凭证字段：`upstreamConfig` 与 `fileUpstream` 各加字段（能复用
     既有字段如 `appID/appSecret/account/key` 就复用，不要重复造），并在
     `loadConfig()` 的 `upstreamConfig{...}` 字面量里补映射；
   - `defaultKind()` 加 `case "<route>": return "<kind>"`。

5. **[cmd/relay/main.go](cmd/relay/main.go)**
   - `buildUpstream()` 加 `case upstream.Provider<Kind>:`，构造客户端并包进
     单 provider 的 `upstream.NewRouter`（照抄相邻 case 的形状）。
   - 存储装配（`buildRouteStorage`）、demo 播种（`seedDemo`）对路由泛化，零改动。

### B. 配置

6. **[config.example.yaml](config.example.yaml)**
   - `versions:` 下追加 `<route>:` 块：`upstream.kind: "<kind>"` + 凭证占位符
     （`REPLACE_WITH_...`）、`database.name: "datahub_<route>_db"`、
     `redis.db:` 取下一个未用的逻辑库编号（看文件里既有各路由的 `db:` 值顺延；
     启动防呆校验不允许复用）；
   - 更新文件头部注释里的路由枚举。
   - 提醒用户：真实配置文件（`config.aliyun.prod.yaml`、`config.aliyun.e2e.yaml`
     等）已被 .gitignore，需要用户自己在本机/服务器上补同样的块并填真实凭证。

### C. 管理平台（路由 license 管理）

后端 admin API 按 `{ver}` 路径完全泛化，**零改动**；只改前端两处：

7. **[web/admin/src/api.js](web/admin/src/api.js)** — `VERSIONS` 数组追加 `'<route>'`。
8. **[web/admin/src/App.jsx](web/admin/src/App.jsx)** — `VERSION_LABELS` 追加
   `<route>: '<ROUTE>'`。
   改完需重新构建 SPA：`cd web/admin && npm run build`（产物在 `dist/`）。

### D. 脚本与测试

9. **新建 `scripts/mock_<kind>.go`** — 参考 [scripts/mock_blacklist.go](scripts/mock_blacklist.go)
   的结构（`//go:build ignore` + 单文件 main）。监听下一个空闲端口
   （已占用：gama 9112 / income 9113 / rental 9114 / blacklist 9115，顺延）。
   必须模拟：验签通过的查得、特定手机号（惯例 `13800000000`）查无、坏签名报错。
10. **[scripts/recreate_databases.go](scripts/recreate_databases.go)** —
    `versionOrder` 追加 `"<route>"`。
11. **[scripts/e2e.go](scripts/e2e.go)** — `demoAppKeys` 映射追加
    `"<route>": "<demo appKey>"`（与 `model.DemoAppKey` 保持一致）。
12. **[test/harness/harness.go](test/harness/harness.go)** — `Versions` 追加
    `"<route>"`；`demoAppKeys` 映射追加同上（01 可达性用例遍历 `Versions`
    自动覆盖新路由；08 隔离用例里的"其它路由"列表如硬编码也顺带补上）。
13. **新建 `test/cases/<NN>_<route>_query.go`** — 编号取 `test/cases/` 现有最大
    编号 +1；整体照抄 [test/cases/10_blk_query.go](test/cases/10_blk_query.go) 的
    场景集（成功查得 / 查无 / 错签 505002 / 未知 appKey 505004 / 缺 appKey 505001 /
    手机号身份证非法 505062），只改 version 常量与 range 断言口径。
    注意：appKey 一律用 `harness.AppKeyFor(version)`，不要用 `harness.AppKey`
    （那是 x1 专用的向后兼容常量）。
14. **[test/cases/04_found_count.go](test/cases/04_found_count.go)** — 该用例
    **硬编码**了各路由的隔离断言（每路由一对 before/after），照样为 `<route>`
    加一组"计数不受 x1 流量影响"检查（用 `harness.AppKeyFor("<route>")`）。
15. **[test/run.ps1](test/run.ps1)** — 照 mock_blacklist 的三处样式：定义
    `$<kind>Exe`、`go build`、`Start-Process`（含日志重定向）。
16. **[test/README.md](test/README.md)** — 用例表加一行，头部路由枚举更新。

### E. 文档

17. **新建 `docs/API_接口文档与使用手册_<route>.md`** — 以
    [docs/API_接口文档与使用手册_blk.md](docs/API_接口文档与使用手册_blk.md) 为模板，
    改路由名与 `result.range` 语义。
18. **[README.md](README.md)** — "对外（下游）"加一条 bullet、"对内（上游，按版本
    路由）"加一条、涉及路由枚举处更新。
19. **[docs/DESIGN.md](docs/DESIGN.md)** — 上游/路由枚举处同步（搜既有路由名如
    `blk` 定位需要更新的段落）。

## 验证（必须全部执行）

1. `go build ./...` 与 `go vet ./...` 通过。
2. **memory 模式冒烟**（无需 PG/Redis）：
   - 起 mock：`go run ./scripts/mock_<kind>.go`；
   - 复制一份 memory 配置（参考 config.example.yaml，`storage.driver: memory`，
     `versions.<route>.upstream.baseURL` 指向 mock 端口）后
     `CONFIG_FILE=<该文件> go run ./cmd/relay`；
   - 用新路由的 demo license（appKey = `model.DemoAppKey("<route>")` 的返回值，
     secret = `demo-app-secret`）按 x1 信封加签 POST
     `/v1/openapi/zlx/querySrmx<ROUTE>`，确认 `errorCode=0`、`body.code=001`，
     换查无手机号确认 `999`。签名算法见 `test/harness/harness.go` 的 `SignX1`。
3. **回归测试套件（最终验收标准）**：
   `powershell -ExecutionPolicy Bypass -File .\test\run.ps1`
   （需要 `config.aliyun.e2e.yaml` 及可连通的 e2e PG/Redis；e2e 配置里必须已补
   `<route>` 块，`recreate_databases.go` 会自动建库+播种）。全部用例 PASS 才算
   完成；报告在 `test_res/<日期>/REPORT.md`。

## 上线注意

- 生产新库：在 RDS 上 `CREATE DATABASE datahub_<route>_db` 即可，relay 启动时
  自动跑 migrations（生产**不**播种 demo license）。
  **绝不要**对生产库跑 `scripts/recreate_databases.go`——它会 DROP 重建表。
- Redis：新路由用独立逻辑库编号，与 config 一致（复用会被启动校验拒绝）。
- 提醒用户更新生产 config 并重新构建部署（含 `web/admin` 前端产物）。
