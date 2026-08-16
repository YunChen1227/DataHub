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
   「查得 / 查无 / 上游侧错误」分别对应的响应码。**响应里哪个字段是"订单号"、
   哪个是"请求/日志号"也要弄清**——成功与失败都要把它们记进审计（`UID`/`LogID`），
   否则失败时无法向上游对账追查（见下「失败也要可追查」铁律）。
   - **请求参数必须逐字段抄全上游参数表**：字段名 + 类型 + **必填/选填** + 含义，
     一个不落。尤其警惕**易被忽略的授权/合规类字段**（如「授权书编号 authlet」
     「授权码」「授权日期」「查询原因码 reasoncode」「业务流水号」等）——它们常
     与身份三要素（name/idCard/mobile）并列出现在私有参数里，漏掉会导致上游认证
     失败。**不要只抄身份三要素**。参数表里的「是否必填」列如与用户/实际联调结果
     冲突，**以实际上游要求为准**（写文档常滞后；用户明确说某字段必填就按必填做）。
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
- **上游 requestId 无论成功失败都必须落审计（铁律，违反即返工）**：管理后台「操作
  记录」有 `上游uid`(`UpstreamUID`) 与 `上游logId`(`UpstreamLogID`) 两列，运营靠它们
  拿上游单号向上游对账。**每一条响应路径**——查得(001)、查无(999)、上游业务失败——
  都必须把上游返回的请求号/订单号带出来，一条都不能漏：
  1. **成功 / 查无路径**：构造 `model.UpstreamResult` 时 `UID` 填上游订单号、`LogID`
     填上游请求/流水号。**上游只有一个标识时，`UID` 与 `LogID` 同填该值**（宁可重复，
     也不能让 `上游logId` 列为空）。所有 `return &model.UpstreamResult{...}` 都要带
     `LogID`——包括 999 分支、聚合 `aggregate.go` 的合并结果（`logidOf` 取任一子源）。
  2. **失败路径（非成功业务码）**：**必须**返回 `*model.UpstreamError`（用
     `busiErr`/`busiErrf` 助手构造，见
     [upstream/errors.go](internal/infrastructure/upstream/errors.go)），把上游
     **code / msg / uid(订单号) / logID(请求号)** 全带上——**禁止**用裸 `fmt.Errorf`
     把这些标识丢进字符串。orchestrator 会在失败路径（含最终 PENDING）用 `errors.As`
     取出写入审计。
  3. **仅**纯网络/传输失败（上游不可达、读超时，压根没有上游标识）才用 `fmt.Errorf`。
  4. 落地前先在响应结构里认清"哪个字段是订单号、哪个是请求/日志号"，再成功/失败两条
     路径都填。既有映射参考：idverify `OutBizNo`(uid)/`RequestId`(logId)；consumetxn
     `reqno`(uid=logId)；gama/blacklist `seqNo`(uid=logId)；income `uid`(uid=logId，
     注意 `reqid` 是我方回显不算)；rental `respOrder`(uid=logId)；facecompare
     `orderNo`(uid=logId)；complaint `token`(uid=logId)。
  5. 幂等重放（命中台账）也要回填：`orchestrator.replay` 从 `Ledger.UpstreamUID/
     UpstreamLogID` 回填审计，别让命中缓存的请求这两列变空。
  6. **聚合路由（`aggregate.go`，多子源）失败路径同样铁律，且最易踩坑**：
     - 采集子源标识时**必须同时从「成功结果」和「失败错误」两头取**——子源失败时
       `Query` 返回 `(nil, err)`，`res` 为 nil，只从 `res` 取 uid/logid 会**全空**！
       必须对 `err` 做 `errors.As(&*model.UpstreamError)` 取出 `UID/LogID/Code/Msg`。
     - **全部子源失败**（`errCnt==len`）**禁止**返回裸 `fmt.Errorf("...全部失败...")`
       （那会让 orchestrator 无法 `errors.As`，导致 `调用上游=否` + 上游 code/uid/logId
       三列全空）——**必须**返回 `busiErr(code, 汇总msg, uid, logid)`，其中 uid/logid
       取任一子源的上游标识、code 取任一子源业务码。
     - 部分成功(002)/全查无(999)分支也要把从**失败子源**捞到的 uid/logid 一并纳入
       采集（首个非空即可）。回归测试见
       [upstream/aggregate_test.go](internal/infrastructure/upstream/aggregate_test.go)
       （`TestAggregatorAllFailedCarriesUpstreamIDs`），改聚合逻辑后必须保持它通过。
     - 前提：单产品 client 在**上游已应答但业务失败**时，`fail` 分支
       也要回传上游响应里的 `orderNo`（别写死空串）——否则聚合器无从捞起。
- **入参与上游严格对齐（铁律，违反即返工）**：本服务是纯转发网关——
  0. **字段集合以「本上游」参数表为唯一依据，禁止照搬既有路由**：每个上游要的
     字段都不一样。开工前先把上游参数表里的**每一个私有字段**（含授权书编号/
     授权码/原因码等非身份字段）连同其必填性完整列出来，再逐一落地。**绝不能**
     以「以前的路由有 name/idCard/mobile」为默认集合——那是最常见的漏字段根因。
     若上游有 `model.QueryCommand`/`UpstreamRequest` 里尚不存在的新字段，先在
     `model.go` 给这两个结构体各加字段，再在校验器与上游客户端里贯通。
  1. **字段名以上游真实契约为准**：新路由的下游入参字段名必须用上游要求的名字，
     不得默认沿用既有路由的 mobile/idCard/name，
     也不得臆造中间层字段名；上游文档示例与服务器报错不一致时**以服务器报错为准**。
  2. **必填/选填口径必须与上游一致**：上游必填的字段，网关校验器必须**前置拦截**
     （对外手册承诺"参数非法不调用上游、不计费"），禁止靠透传给上游报错兜底。
     默认 `parse.Parse`（mobile必/idCard必/name选）只适用于与经济能力同口径的
     上游；口径不同就写专属校验器并在 `cmd/relay/main.go` buildRouteStack 的
     kind switch 里 `orch.WithParser(...)` 挂上（参考 zlf/blk 的 ParseWithName、
     xfjy 的 ParseConsumeTxn——后者含授权书编号 authlet 必填校验）。上游文档「是否必填」列与用户/联调实况冲突时以实况为准。
  3. 交付前必须逐字段核对一遍「上游参数表（含必填列）→ 下游契约 → 网关校验 →
     上游客户端发送」四层一致性，一个字段都不能漏；测试用例必须包含每个必填字段
     的"缺失拦截"场景（含授权类字段），以及探测/联调脚本也要带齐必填字段。
- **每个源必须与「该源自己的文档」逐字一致（铁律，违反即返工）**：一个源一份文档
  （在 `docs/` 下，PDF/DOCX/MD/XLSX）。落地与验收都以**该源文档**为唯一依据，禁止
  用别的源的文档、别的源的代码、或「同类协议的常见做法」代替。交付前逐条确认：
  1. **接口清单要全**：把文档「接口协议」章节里的**每一个**业务接口列出来，逐个
     标注「已实现 / 不实现及理由」。决定不实现的，要在客户端注释里写明依据（如不在
     下游契约覆盖范围内），不许默默漏掉。
     *踩过的坑*：销项数据文档 §4 定义了 3 个接口（月度开票汇总/月度下游企业/发票
     明细），源5 只实现了前 2 个，第 3 个连注释都没提。
  2. **字段名逐字抄，含大小写**：`StartIndex`/`CountLimit` 这类首字母大写、
     `taxpayerIdNum` 这类驼峰、`monthlyInvoiceSummryInfo` 这类上游自带的拼写错误
     （Summry 少个 a），一律原样照抄，**不许"纠正"成自己觉得对的写法**。
  3. **文档自相矛盾时两种都兼容并留注释**：报文示例与字段表对同一字段给不同拼写很
     常见（销项数据文档里 `invoiceDayMonth` vs `invoceDayMonth`、
     `buyerTaxpayerIdNum` vs `buyerTaxPayerIdNum`）——**两种都认**，并在代码里记下
     矛盾出处，不要挑一个赌。
  4. **凭证的编码形态必须验算，不能想当然**：AES 密钥只接受 16/24/32 字节。拿到
     AppKey/密钥先**数长度**：64 个十六进制字符 = 32 字节 AES-256 密钥（必须 hex
     解码），**不是** 64 字节 ASCII；Base64 形态的要先 Base64 解码。密钥推导必须
     **显式实现 + 单元测试**，长度不合法立刻报错，禁止静默降级。
     *踩过的坑*：源5 把 64 字符 AppKey 直接 `[]byte()` 交给 `aes.NewCipher`，得到
     `invalid key size 64`——上线后一个请求都没发出去过，日志里只有加密失败。
  5. **拿到真实凭证当天就本地验算一遍**：用真实 appId/appKey 跑一遍加签/加密，确认
     能算出密文再谈联调。**本地就加密失败的，联调一万次也不会通**。
  6. **结果码全覆盖**：文档结果码表里每个码都要有归一化去处（查得/查无/上游侧错误），
     并处理「其余按此顺序拓展」这类开放式约定。
  7. **地址与环境**：生产/测试两套地址都写进配置注释；路径拼接方式（如
     `…/api/ws/<业务接口>`）照文档，不要自创。
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
   - **成功/查无路径**：`model.UpstreamResult` 的 `UID` 填上游订单号/流水号（`seqNo`/
     `OutBizNo`/`respOrder`…），`LogID` **必填**上游请求/日志号；上游只有一个标识时
     `UID`/`LogID` 同填（见上「上游 requestId 无论成功失败都必须落审计」铁律）。
   - **失败路径（非成功业务码）**：一律 `return nil, busiErr(...)` /
     `busiErrf(...)`（见上「失败也要可追查」铁律），把上游 code/msg/订单号/请求号
     带全；不要用 `fmt.Errorf`。只有网络不可达/读超时才用 `fmt.Errorf`。

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
   （已占用：gama 9112 / income 9113 / rental 9114 / blacklist 9115 /
   facecompare 9117 / idverify 9118 / consumetxn 9119 / complaint 9120，顺延）。
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
4. **真实配置文件必须确认该源已装配**：`go build` 通过 ≠ 线上调得通。交付前打开
   实际部署用的配置（`config.aliyun.prod.yaml` 等），确认新源**确实出现在**
   `versions.<route>.upstreams` 列表里且凭证是真值而非 `REPLACE_WITH_…`。
   *踩过的坑*：源5 代码写完了，但生产配置还停在废弃的单块 `upstream:` +
   `products:` 写法上——`products` 早已不被 `loadConfig` 解析，于是只装配出
   一个 product 为空的子源：其余子源全废，线上表现就是"调不通"。
5. **上游连不通先分清是谁的问题**：本机直连上游被秒断（`curl: (52) Empty reply
   from server`）通常是**上游按源 IP 白名单**，不是代码错——需要把部署机（阿里云
   ECS）的出口 IP 报给上游加白，并从该机复测。别把白名单问题当协议问题去改代码。

## 上线注意

- 生产新库：在 RDS 上 `CREATE DATABASE datahub_<route>_db` 即可，relay 启动时
  自动跑 migrations（生产**不**播种 demo license）。也可跑
  `CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/recreate_databases.go`——
  它默认**只补建缺失的库 + 迁移，绝不删数据**，且对生产库硬拒绝 DROP。
  破坏性重置（`RESET_DESTRUCTIVE=1`）仅用于测试库，脚本会自动拒绝生产环境。
- Redis：新路由用独立逻辑库编号，与 config 一致（复用会被启动校验拒绝）。
- 提醒用户更新生产 config 并重新构建部署（含 `web/admin` 前端产物）。
