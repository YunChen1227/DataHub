---
name: add-upstream-multi
description: DataHub 新增「多上游聚合」路由——一条对外路由内部并发查询多个上游/多个产品码，把结果聚合成一份返回下游（如 swfp 税务发票四产品聚合）。用户提到「聚合多个上游」「一个路由查多个接口」「多产品合并返回」「部分成功」时必须使用本 skill。本 skill 只写与单上游接入（add-upstream skill）的差异；未提及的步骤一律照 add-upstream 清单执行。
---

# DataHub 新增多上游聚合路由

一条路由内部调 N 个上游接口（或同一平台的 N 个产品码），聚合成一份 `result.range`
返回下游。**基础改动清单与 add-upstream skill 完全相同**（model/router/config/main/
mock/测试/文档 19 条照走），本文件只讲聚合特有的 6 个差异点。参考实现：swfp 路由
（`internal/infrastructure/upstream/entcredit.go`，税务+发票四产品码聚合）。

## 第 0 步补充：多上游特有的待确认项

在 add-upstream 第 0 步之外，还必须向用户确认：

1. **子源清单**：N 个子源各自的 endpoint/产品码/凭证——是同一平台多产品码（如
   swfp：同一端点四个 apiCode），还是完全独立的多家上游（各自域名协议）？
2. **聚合判定口径**：部分子源失败算什么？（默认按下方判定表）
3. **range 合并结构**：每个子源在合并 JSON 里的键名与内容（原样透出还是解码）。
4. **下游入参（铁律，同 add-upstream skill 的"入参与上游严格对齐"）**：聚合路由
   往往不是个人三要素（如 swfp 是企业统一社会信用代码）——入参不同就需要新的
   参数校验器（见差异点 4）。**字段名必须对齐上游真实契约**（上游文档示例与
   服务器报错不一致时以服务器报错为准），**必填/选填口径必须与上游一致**：上游
   必填的字段由网关校验器前置拦截，禁止透传给上游报错兜底；测试用例必须覆盖
   每个必填字段的"缺失拦截"场景。

5. **读完上游文档的每一页，不要只 grep 关键字段表**：PDF/接口文档常常把"字段表"
   和"接入代码示例/鉴权流程/错误码附录"分成前后两部分，字段表在前几页，真正的
   鉴权协议、签名算法调用方式、错误码含义在文档中后段甚至最后。第一次实现 swfp
   时只读了前 120 行字段表就动手写了 MD5 签名 + JSON body 的假协议，实际上游是
   HMAC-SHA256 签名 + `application/x-www-form-urlencoded` 表单——协议形态整个
   猜反了。**动笔前用 Read 工具完整读完 PDF 全文**（该工具会自动分页渲染），
   而不是用 pdftotext/grep 抽取片段。
6. **要签名算法源码，不要只凭文档描述实现**：如果文档只写"调用 xxxHelper.sign()"
   没给出内部实现，问用户要 SDK/demo 压缩包（关键词：demo、SDK、示例代码）。
   本例中 `SignedRequestsHelper.java` 源码到手后才发现真实算法是 HMAC-SHA256
   （密钥为 secretAccessKey 的 Base64 解码结果），此前假设的 MD5 完全是猜的，
   凭空实现的签名算法上生产必然 100% 验签失败。
7. **上游服务器的报错信息比文档/demo 示例更权威**：官方 demo 文档给的业务参数
   示例是 `args={"prodCode":"...","entInfo":"统一社会信用代码"}`，代码按此实现
   并通过了签名/鉴权验证——但真实联调时上游直接报
   `E1000 查询参数校验不通过,creditCode:为必填项`，说明这份 demo 示例对这四个
   具体产品码是错的，真实字段名是 `creditCode`。**只要签名/鉴权已经打通，服务器
   明确点名某个业务字段的错误信息，就该以此为准去改字段名，而不是继续信任文档
   示例**——demo 文档往往是通用范例，未必逐字对应每个产品码的真实契约。

## 铁律：一个子源一份文档，逐个对齐（违反即返工）

聚合路由的 N 个子源常来自**不同厂商、不同协议、不同文档**——swfp 就是混的：源1-4 是
证通 entcreditapi（HMAC-SHA256 签名 + form 表单 + 产品码），源5 是凯盈云 crestv
（`docs/销项数据接口文档V1.0.docx`，AES 加密 + JSON 信封 + 业务接口名走 URL 路径）。
所以：

- **每个子源都要单独**按 add-upstream skill 的「每个源必须与该源自己的文档逐字一致」
  铁律走一遍完整核对（接口清单/字段名大小写/文档自相矛盾/凭证编码形态/结果码/地址），
  **禁止**把已实现子源的协议、凭证口径、加密方式套用到新子源上；
- 第 0 步收集信息时**逐个子源**要文档，缺哪个源的文档就问用户要，不许凭"其它源是
  这么做的"推断；每个子源客户端的文件头注释要写清**它依据的是哪一份文档**；
- 契约映射层里每个源的字段映射表，必须标注依据的是**哪份文档的哪一节**（参考
  `swfpcontract.go` 的 `swfpSalesKphzMap`：源5 → xlsx 字段，逐行带口径注释）；
- **交付前逐源过四层**：源N 的文档 → 源N 的 client → 源N 在真实配置里的 `upstreams`
  子源条目 → 源N 在契约层的映射。四层齐了才算完，缺一层就是线上"调不通"。
  自检可参考 `scripts/check_swfp_wiring.go`（打印每个子源的 label/kind/凭证是否齐全、
  源5 的 AppKey 能否解出合法 AES 密钥）。

## 下游返回值口径（先判断，二选一）

聚合路由动工前**必须先问用户：这条路由有没有「明确的下游返回值文档」**（客户契约，
如 swfp 的 `docs/税票分析接口文档.xlsx`）？两种口径二选一，不能混用：

- **无返回值文档（默认）**：原样透传——聚合器把各子源结果合并为
  `{"<段名>":{"status":"ok|empty|error","data":{...}}}` 直接放进 `result.range`，
  上游返回什么就给什么（本 skill 其余章节的默认行为）。
- **有返回值文档（契约模式，参考 swfp）**：在 Aggregator 之外**加一层契约映射**
  （参考 [internal/infrastructure/upstream/swfpcontract.go](internal/infrastructure/upstream/swfpcontract.go)，
  装配点在 `cmd/relay/main.go` buildUpstreams 的 route 判断），铁律：
  1. **严格白名单**：只输出返回值文档里定义的字段，上游多余字段（含整段，如
     summaryIndicators）一律剔除；文档定义但上游缺失的字段**输出空串**；
  2. **按源分组 + 脱敏**：每个契约顶层字段的值是 `{"源1": ..., "源5": ...}` 分组
     对象（段名 label → 源N 的映射表写在契约层），各源数据不合并不去重；对下游
     隐匿一切上游标识（产品码/段名/错误详情都不得透出）；
  3. **sourceStatus**：range 里附 `{"源N":"ok|empty|error"}` 让下游知道 002 时
     谁失败了（错误详情只落我方审计，不透出）；
  4. **语义映射要逐字段核对**：不同源的同名义字段口径可能不同（含税/不含税、
     统计范围、日期格式、精度），照样映射进契约字段名，但把口径差异写进对外
     手册按源标注——**先列差异表和用户确认**，不许静默映射；
  5. **可选源 + scope 入参**：成本较高的附加源在 config 标 `optional: true`，
     下游 body 可选 `scope`（"all" 缺省 / "basic" 跳过可选源，字符串参与加签），
     校验器拦非法值；判定表 (001/999/002) 仍由 Aggregator 负责，契约层只改写
     range 内容，**不碰上游调用与归一逻辑**；
  6. 契约层必须带单元测试（参考 `swfpcontract_test.go`：白名单剔除、补空、
     源N 分组、scope 跳过、失败详情不泄漏五类断言）。

## 聚合判定表（默认口径，写代码前和用户对一遍）

| 子源结果组合 | body.code | 计费 | 台账 |
|---|---|---|---|
| 全部成功应答，≥1 份查得 | `001` | **计费** | BILLED |
| 全部成功应答，全部查无 | `999` | 不计费 | BILLED |
| ≥1 份成功应答 + ≥1 份失败 | `002`（聚合特有：部分数据源成功） | 不计费 | BILLED |
| 全部失败 | 返回 error → `505062` | 不计费 | PENDING→复查/对账 |

- 「成功应答」= 子源给出确定结论（查得或查无）；「失败」= 子源网络/系统/账户错误。
- `002` 的 range 仍带成功部分的数据 + 每段状态，让下游拿到能拿到的。
- 数据不完整不收费（`002` 不计费）——这是默认口径，用户可改。

## 与单上游的 6 个差异点

1. **billing 判定表**（[internal/domain/billing/billing.go](internal/domain/billing/billing.go)）：
   `DefaultTable()` 的 `resolvedCodes` 加 `"002"`（不加入 `returnedCodes`）——002 是
   确定结论（BILLED 状态机）但不计成功查得数。单上游路由永不产生 002，无影响。

2. **mapping 透出部分数据**（[internal/domain/mapping/mapping.go](internal/domain/mapping/mapping.go)）：
   `NotFound()` 原本不设置 `Result`；改为 `r.Range != ""` 时也透出 `result.range`。
   002 走 resolved&&!returned 分支（即 NotFound 映射），必须能带数据。查无（999）
   的 Range 恒为空，行为不变。

3. **聚合客户端**（新建 `internal/infrastructure/upstream/<kind>.go`）：
   仍实现 `port.UpstreamPort`（对 orchestrator 完全透明）。**聚合已下沉为通用能力**：
   一条路由的上游是 `upstreams` 列表 (config)，装配层给每个子源建一个单一职责 client，
   再统一套上 `internal/infrastructure/upstream/aggregate.go` 的 `Aggregator`
   (`len==1` 直通、`len>1` 并发聚合)。新增聚合上游时**通常只需写单源 client**（如
   entcredit 单产品码），并发/判定表/range 合并交给 Aggregator：
   - config：每个子源在 `upstreams` 列表里自带完整凭证 + `label`(段名) + 业务标识
     （如 entcredit 的 `product` 产品码）；同一路由所有子源 `kind` 必须一致——
     **例外**：走契约模式的路由（如 swfp 混接 entcredit+salesdata）在 loadConfig
     里豁免该校验，参数校验器按首个子源 kind 选择；
   - 单源 client 归一为 `001`(查得, Range=明细)/`999`(查无)/`error`(失败)，其余判定
     由 Aggregator 按下方判定表完成；
   - `Query()` 里 Aggregator 用 `sync.WaitGroup` **并发**调所有子源，每源独立错误互不影响；
   - 逐源归一为内部小结构 `{key, ok(查得/查无/error), data}`；
   - 按判定表聚合出 001/999/002/error；range = `{"<源key>":{"status":"ok|empty|error","data":{...}},...}`
     的 compact JSON（复用 blacklist.go 的 `compactJSON` 思路）；
   - **协议适配点集中在一个函数**（如 `callProduct`）：上游文档若缺鉴权/信封细节，
     按最简假设实现并注释 `// TODO 联调适配`，联调时只改这一个函数；
   - `Requery` 未联调前照旧返回 `&model.RequeryResult{Reachable:false}`。
   - **警惕"看似冗余的双重编码"**：部分上游 demo 代码会对同一字段做两次
     URLEncode（业务参数 JSON 手动 encode 一次 + 表单库整体再 encode 一次）——
     这不是对方 demo 的 bug，是协议约定的一部分，必须原样复刻，不要"优化掉"。
     反过来也要小心不要重复叠加：如果某个值在计算过程中（如签名结果）已经内置
     了一次编码，就不能再手动编码一次，否则变成三次、验签必然失败。逐字段数清楚
     "这个值经过了几次 encode"再落地。

4. **参数校验器按路由替换**（入参非个人三要素时才需要）：
   - [internal/domain/model/model.go](internal/domain/model/model.go)：`QueryCommand`/
     `UpstreamRequest` 增加新入参字段（如 `CreditCode`）——handler 的 json 解析与
     签名 bodyParams 都是泛化的，零改动；
   - [internal/domain/parse/parse.go](internal/domain/parse/parse.go)：新增
     `Parse<Xxx>` 校验器（如 `ParseCredit`：统一社会信用代码正则
     `^[0-9A-HJ-NPQRTUWXY]{2}\d{6}[0-9A-HJ-NPQRTUWXY]{10}$`）；
   - [internal/application/orchestrator.go](internal/application/orchestrator.go)：加
     `WithParser(fn)` setter（默认仍 `parse.Parse`），`Handle` 用 `o.parseFn`；
   - [cmd/relay/main.go](cmd/relay/main.go) `buildRouteStack`：按 upstream kind 调用
     `orch.WithParser(parse.ParseXxx)`。
   - 审计记录的 Name/IDCard/Mobile 掩码字段对此类路由留空即可，不要把新入参塞进去。

5. **mock 一个进程模拟全部子源**（`scripts/mock_<kind>.go`）：单端口实现全部产品码/
   子源，用**特殊入参值**驱动场景（照单上游 mock 用 13800000000 触发查无的惯例）：
   - 正常值 → 全部查得（001）；
   - 约定「查无值」→ 全部查无（999）；
   - 约定「部分失败值」→ 指定某个子源返回错误、其余正常（002）；
   - 坏鉴权 → 全部失败（505062）。

6. **测试用例多两个场景**：照抄单上游用例集（错签/未知 appKey/缺 appKey/参数非法）
   之外，必须覆盖 **002 部分成功**（断言 body.code=002 且 range 含 error 状态段与
   成功段数据）和**全部失败 → 505062**。入参字段按本路由实际入参写。

## 验证补充

按 add-upstream 验证三步走；memory 冒烟必须打满四个场景：001（全查得）、999
（全查无）、002（部分失败，检查 range 内容）、505062（mock 全挂/坏鉴权）。

## 上线注意补充

- 对外文档（api-doc skill）里 `002` 要有独立行：含义、计费=否、range 结构说明。
- 聚合路由的"调用上游次数"（totalCalls）口径 = 下游一次查询计 1 次，不按子源乘 N。
- config 一律用 `versions.<route>.upstreams` 列表：每个子源一个列表项，自带完整凭证
  （即使多个子源同平台同凭证也各写一份，保持子源完全独立、可分别替换）。不要把多源
  凭证拍平成一堆无前缀字段，也不要再用已废弃的单块 `upstream:` + `products` 数组。
