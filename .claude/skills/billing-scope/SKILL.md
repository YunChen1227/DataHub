---
name: billing-scope
description: DataHub 计费口径（哪些响应计费、哪些不计费）与上游对齐的权威规则表。只要用户提到「计费」「收费」「扣费」「不计费」「成功查得数」「对账」「账单对不上」「漏计费/多计费」「查无要不要收钱」「billing」，或你要新增/修改任何上游客户端的响应码归一化（001/999/error）、改动 billing.DecisionTable、改 quota.Settle、改 orchestrator 的查得/查无分支，即使用户没明说要用 skill，也必须先读本 skill。表里每条口径都有上游文档出处，未经核对文档不得改动。
---

# DataHub 计费口径（与上游对齐）

计费错一位，要么长期倒贴钱（上游收了我们没收），要么被客户投诉乱收费。本 skill
把**每条路由的收费口径逐条钉死**，并给出核对流程。**改任何一条前，先回上游文档
把该码的计费标注抄出来贴进 PR 描述**，不许凭直觉或"参考隔壁路由"。

## 一、计费是怎么算出来的（代码路径）

一次请求的计费结论由两步产生，**改任何一步都要重读本 skill**：

1. **上游客户端归一化**（`internal/infrastructure/upstream/<kind>.go` 的 `Query`）
   把上游自己的业务码归一成三种之一：
   - `model.CodeFound`（`"001"`）查得数据
   - `model.CodeNotFound`（`"999"`）查无结果（上游给了确定结论，只是没数据）
   - 返回 `*model.UpstreamError` → 上游侧错误，**一律不计费**，走复查/对账兜底
   - `model.CodePartial`（`"002"`）仅多源路由产生，**不计费**
2. **计费码表**（`internal/domain/billing/billing.go`）把归一码翻成两个独立结论：
   - `Resolved` → 上游给了确定结论 → 台账 `BILLED`（**只是结算完成，不等于收钱**）
   - `Returned` → **本次应计费** → `成功查得数 +1`、台账 `counted_service=true`、
     审计 `billed=true`

**`Returned` 才是"收钱"的唯一判据。**默认口径 `DefaultTable()` 只有 `001` 计费；
`TableFor(route)` 给「上游对查无也收费」的路由额外把 `999` 也算计费。

### 铁律 1：计费口径 ≠ 报文形态

下游看到的 `body.code` **只由归一码决定**（`001`→`mapping.Found`，其余→`NotFound`），
**绝不许**用 `decision.Returned` 当分支条件。两者在 blk 上必然分叉：查无要收费，
但下游必须照样看到 `999`。判"是不是查得"一律用 `model.IsFoundCode(code)`。

涉及的三处（改动时同步检查）：
- `application.respondX1` —— 回源路径的报文分支
- `application.replay` —— 幂等重放，依据台账的 `UpstreamCode`（不是 `CountedService`）
- `application.replayCached` / `cache.Entry.Found()` —— 缓存命中路径

### 铁律 2：结算必须把上游标识写进台账

`quota.Settle` 通过 `model.LedgerSettlement` 把 `UpstreamCode/UpstreamUID/UpstreamLogID`
一并落库。缺了它们，台账只能证明"我方认为该收费"，无法证明"上游那边是哪一笔"，
对账时死无对证；`replay` 也会失去判"查得还是查无"的依据。

### 铁律 3：上游若下发显式计费标志，以它为准

上游报文里带"本次是否计费"字段的（目前只有 sfzhy 的 `IsCharge`），
**必须以该字段为唯一判据**，不许从响应码反推。码表只是文档快照，上游改口径时
先变的是这个字段。

## 二、逐路由计费口径（权威表，改前必须核对文档）

「计费」列 = 我方是否 `成功查得数 +1`，必须与上游账单口径一字不差。

| 路由 | 上游 kind | 上游码 → 归一码 | 计费 | 文档出处 |
|---|---|---|---|---|
| x1 | gama 伽马分层分 | `busiCode 10` → 001 | ✅ | `docs/伽马分层分_定制版.pdf` §2.1「10 查询成功【计费】」 |
| | | `busiCode 1000` → 999 | ❌ | 同上「1000 数据未查得」——**无**【计费】标注 |
| | | 其余 busiCode / `code≠0` → error | ❌ | 1001 余额不足、1003 appId 异常… |
| **blk** | blacklist 黑名单因子V35 | `busiCode 10` → 001 | ✅ | `docs/黑名单因子V35.pdf` §2.1「10 查询成功【计费】」 |
| | | **`busiCode 1000` → 999** | **✅** | 同上「**1000 未查得【计费】**」——**查无也收费** |
| | | 其余 → error | ❌ | |
| v9 / v8 | income 经济能力 | `001` → 001 | ✅ | `docs/income_cls.md` 返回码字典「001 成功」 |
| | | `999` → 999 | ❌ | 同上「999 查无结果」 |
| | | 002/003/004/… → error | ❌ | 账号不存在/余额不足/未授权… |
| zlf | rental 租赁分V2-D | `SW0000` → 001 | ✅ | `docs/上游对接_租赁分V2-D_守信_钉钉文档整理.md` §4「SW0000 **收费**」 |
| | | `SW0002` → 999 | ❌ | 同上「SW0002 查无记录 **不收费**」 |
| | | SW0001/0003/0017/003x/004x → error | ❌ | 同上表其余行均「不收费」 |
| rlbd1 / rlbd2 | facecompare 人脸身份证比对一所 | `code=200` 且 `incorrect ∈ {100,101,103,109,110,111,112}` → 001 | ✅ | `docs/人脸身份证比对一所--ShowDoc.html`「incorrect 字段详解」是否收费=**是** |
| | | `incorrect ∈ {104,106,107,108,113}` → error | ❌ | 同表 是否收费=**否** |
| | | `code≠200`（400/404/500/501/60x） → error | ❌ | 同文档「code 错误码说明」 |
| | | *本上游无「查无」概念，不产生 999* | | |
| **sfzhy** | idverify 身份证三要素核验 | `Code=0` 且 **`IsCharge=true`** → 001 | ✅ | `docs/身份证三要素核验接口文档.docx` §3「IsCharge 计费标志」+ §5.2 Result 0–5 均「计费」 |
| | | `Code=0` 但 **`IsCharge=false`** → 999 | ❌ | 同 §3：上游明说不收费，我方不得计费（落 warn） |
| | | `Code≠0`（401–463 / 501–504） → error | ❌ | 同 §4.2.2 失败示例 `IsCharge:false` |
| xfjy | consumetxn 消费交易特征 | `code=0` 且 `result=0` → 001 | ✅ | `docs/消费交易特征/消费交易特征.html`「0：查询成功（计费）」 |
| | | `code=0` 且 `result=1` → 999 | ❌ | 同上「1：未查得（不计费）」 |
| | | `code≠0` 或 result 越界 → error | ❌ | |
| lxf | lxscore 灵犀分 score_195_v1 | `status=200` 且 分数 ≠ `-1` → 001 | ✅ | `docs/灵犀分-score_195_v1-接口文档.pdf`「计费规则」第 2 条 |
| | | `status=200` 且 分数 = `-1` → 999 | ❌ | 同上第 3 条「分数等于-1，查得失败」 |
| | | `status=200` 但 data 为空 → 999 | ❌ | 文档未列；保守按「无报告」不计费 |
| | | `status≠200` → error | ❌ | 同上第 3 条「非200…查得失败」 |
| grsb | bgpg BJPG-01 背景评估 | `code=200` → 001 | ✅ | `docs/BJPG-01背景评估 (2)(1).pdf` §4.3 返回码表「200 请求成功 **计费**」 |
| | | `code ∈ {2-404, 3-404}` → 999 | ❌ | 同表「没有查询到数据 **不计费**」 |
| | | 2-5xx / 3-5xx → error | ❌ | 同表其余行均「不计费」 |
| grgjj | incomeag 收入A_g版（主源） | `001` → 001 | ✅ | `docs/收入A_g版--ShowDoc.html` 返回码表「001 成功（**计费**）」 |
| | | `999` → 999 | ❌ | 同表「999 无结果返回」——无计费标注 |
| | bgjj 备用公积金（备源） | `code=100` → 001 | ✅ | ⚠️ **上游未提供码表**（`docs/备用公积金1/对接事项.txt` 只有凭证），当前口径据实测推断 |
| | | `code=201` → 999 | ❌ | ⚠️ 同上，**待上游书面确认** |
| sfsm | idcheck 身份证二要素核验 | `code=200` 且 `result ∈ {0,1}` → 001 | ✅ | ShowDoc 返回值说明：0 一致 / 1 不一致 均**收费** |
| | | `code=200` 且 `result=2` → 999 | ❌ | 同上「2 无记录（预留）」未标收费 |
| | | `code≠200` 或 result 越界/缺失 → error | ❌ | ⚠️ 该源文档未落到本仓 `docs/`，口径来自代码注释引用 |
| tsfx | complaint 投诉分析识别名单 | `code=0000` → 001 | ✅ | ⚠️ `docs/投诉分析识别名单-接口文档-V1.0.0.pdf` §2.1 状态码字典**无计费列**；当前按「调用成功即计费」，**待上游书面确认** |
| | | 1001/1002/1010/1012/1013/1099/8000 → error | ❌ | 同上，均为账户/参数/系统/通道问题 |
| **dtjd** | multiloan 多头借贷行为 | `SW0000` 认证成功 → 001 | ✅ | `docs/上游对接_多头借贷行为_守信_钉钉文档整理.md` §4「SW0000 认证成功 **收费**」 |
| | | **`SW0001` 认证失败 → 999** | **❌** | 同表「SW0001 认证失败 **收费**」——⚠️ **上游收费但我方不向下游计费**，见下「三之二」 |
| | | `SW0002` 查询无记录 → 999 | ❌ | 同表「SW0002 查询无记录 **不收费**」 |
| | | SW0003/0017/0018/003x/004x/SW10xx/SW9999 → error | ❌ | 同表其余行均「不收费」 |
| | | `SW0000` 但 resp_data 空/不可解析 → error | ❌ | 拿不到数据不得计费（第四节第 5 条） |
| **snhmd** | compassblack 司南黑名单 | `SW0000` 认证成功 → 001 | ✅ | `docs/上游对接_司南黑名单_守信_钉钉文档整理.md` §4「SW0000 认证成功 **收费**」；含 `black_list=0` 未命中——那是有效结论不是查无 |
| | | **`SW0001` 认证失败 → error** | ❌ | 同表「SW0001 认证失败 **不收费**」——⚠️ 与兄弟产品 dtjd 的同码**相反**，故归一去处也不同 |
| | | `SW0002` 查询无记录 → 999 | ❌ | 同表「SW0002 查询无记录 **不收费**」 |
| | | SW0003/0017/0018/003x/004x/SW10xx/SW9999 → error | ❌ | 同表其余行均「不收费」 |
| | | `SW0000` 但 resp_data 空/不可解析 → error | ❌ | 拿不到数据不得计费（第四节第 5 条） |
| 多源路由 | aggregate / sourcing | `002` 部分成功 / 未取得数据 | ❌ | 数据不完整不向下游计费（成本侧另按各子源实际调用与上游结算） |
| | | 全部子源查无 → 999 | 按主源口径 | |

**⚠️ 标记的三处（bgjj / sfsm / tsfx）缺上游书面计费口径，属已知风险**：口径基于
实测或既有约定，不是文档背书。拿到上游确认后回来更新本表并同步 `billing.TableFor`
与对应客户端。dtjd 的 SW0001 是另一类风险（文档有标注但我方刻意不跟随），见下。

### ⚠️ 同一供应商兄弟产品的口径陷阱（已踩过两次）

| 供应商 | 同端点兄弟路由 | 同一个码 | 口径 |
|---|---|---|---|
| 应诺尔 enol | x1 伽马 vs blk 黑名单 / sffx 身份风险 | `busiCode 1000` 未查得 | x1 **不**计费，blk/sffx **计费** |
| 守信 shouxin168 | zlf 租赁分 vs dtjd 多头借贷 vs snhmd 司南黑名单 | `SW0001` 认证失败 | zlf **不收费**、dtjd **收费**、snhmd **不收费**——同一家的三条路由，三份文档，同一个码两种标注 |

守信这一家尤其要小心：三条路由**同一个 endpoint、同一套信封、同一张 19 行 SW 码表**，
只靠 biz_data 里的 `mode` 区分产品，但每份文档的「是否收费」列各自独立。dtjd 因为
`SW0001` 标收费而被迫归一成 999（见下「三之二」），snhmd 因为标不收费而直接按上游侧
错误处理——**同码不同价，归一去处也就不同**。`compassblack_test.go` 里有一条名为
`TestCompassBlackAuthFailIsUpstreamErrorNotNotFound` 的护栏用例，专门防止后人为了
"让两条路由一致"把它改成 999。

**同端点、同信封、同码、同语义，计费口径却相反**——`billing.TableFor` 按路由分别
配置正是为此。禁止因为"看起来一样"就复用兄弟路由的结论。

## 三、要加"查无也计费"的路由怎么改

只改一处：`internal/domain/billing/billing.go` 的 `billNotFoundRoutes` 加一条，
**并在旁边写上文档出处**（哪份文档、第几节、原文怎么标的）。

```go
var billNotFoundRoutes = map[string]bool{
	"blk": true,
}
```

**不要**改上游客户端把 999 改成 001——那会让下游把「查无」收成「查得」，报文错、
客户对不上账。归一码只描述"有没有数据"，计费码表只描述"收不收钱"。

### 三之二、`billNotFoundRoutes` 用不了的情况：一条路由里两个 999 口径相反

`billNotFoundRoutes` 的粒度是**整条路由的 999**，一开就是一刀切。当同一条路由有
**两个都要归一到 999、但上游计费标注相反**的码时，它无能为力：

- dtjd 多头借贷：`SW0001` 认证失败标【收费】，`SW0002` 查询无记录标【不收费】。
  把 dtjd 加进 `billNotFoundRoutes` 会让 `SW0002` 也被计费 → **对下游多收钱**。

> 对照：兄弟路由 snhmd 司南黑名单**不在**这一困境里——它的 `SW0001` 标【不收费】，
> 客户端直接返回上游侧错误，压根不归一到 999，所以该路由的 999 口径单一。
> 两条路由都不在 `billNotFoundRoutes` 里，但**理由完全不同**，别当成同一回事。

**当前处理（产品决定）**：下游一律收到 999 查无、**我方不向下游计费**；上游那一侧
被收的钱靠 `slog.Warn` + 审计里的上游订单号（`resp_order`）人工对账。代价是上游若
真对 SW0001 扣费，这部分成本由我方承担——属**刻意选择的保守口径**（宁可自己吃成本，
不可向客户多收），已在上游客户端注释、`docs/上游对接_多头借贷行为_守信_钉钉文档整理.md`
§4 ★ 和 `billing_test.go` 三处钉住。

若将来这类成本变得显著，正解是**按上游原始码计费**（在台账里存上游原始 `resp_code`
而非仅归一码，再按码表判 Returned），那是 billing/quota 的结构性改动，**动手前先与
用户确认**。

改完必须让 `internal/domain/billing/billing_test.go` 的
`TestTableFor_PerRouteChargeScope` 和 `internal/application/billing_scope_test.go`
的 `TestBillingScopeDecoupledFromWireCode` 覆盖新路由并通过。

## 四、新接一个上游时的计费核对清单

配合 `add-upstream` skill 使用，落地上游客户端时逐条打勾：

1. **把上游文档的响应码表整张抄下来**，逐码标注「计费 / 不计费」。文档没有计费列
   的（如 tsfx），**必须向上游书面确认**，不许自己假设；确认前在客户端注释与本
   skill 表里都标 ⚠️。
2. **看有没有显式计费标志字段**（`IsCharge` 这类）。有就以它为准（铁律 3）。
3. **逐码落到三种归一去处**：计费码 → `001`；上游明确不计费的"查得不到"→ `999`；
   其余（账户/参数/系统/限额）→ `busiErr(...)`。不许有码没去处。
4. **查无到底收不收费**：收 → 在 `billNotFoundRoutes` 加路由；不收 → 什么都不用做。
   同一供应商的不同产品口径可能不同（伽马 1000 不计费 vs 黑名单 1000 计费，
   **同端点同码同语义**），**逐产品看文档，禁止复用兄弟路由的结论**。
5. **"成功码但解析失败"必须走 error 不计费**：上游应答了成功码，但解密/解析炸了
   （bgpg 的 `data 解密失败`、lxf 的 `E_DATA`、incomeag 的 `result 解密失败`），
   一律 `busiErr` 不计费——我方拿不到数据就不该向客户收钱。
6. **写单测钉住整张码表**：照 `internal/infrastructure/upstream/idcheck_test.go`
   的 `TestIDCheckNormalization` 写一个表驱动用例，每个码一行，注释写明文档依据。

## 五、核对现网口径是否漂移

```powershell
# 某路由某段时间内「计费条数」与「查得条数」的差额；只有 blk 应该 > 0
psql -d datahub_<route>_db -c "
  SELECT upstream_code, count(*), sum(counted_service::int) AS billed
  FROM billing_ledger
  WHERE version='<route>' AND settled_at >= now() - interval '7 days'
  GROUP BY 1 ORDER BY 2 DESC"
```

- 非 blk 路由出现 `upstream_code='999' AND billed>0` → 多计费，立刻查
  `billNotFoundRoutes` 是否被误加。
- blk 出现 `upstream_code='999' AND billed=0` → 漏计费，查计费码表是否被 revert。
- 任何路由出现 `upstream_code=''` 且 `state='BILLED'` → 结算没落上游码，
  查 `quota.Settle` 是否绕过了 `model.LedgerSettlement`。
- `state='PENDING'` 堆积 → 上游未决，由 `job.RequeryWorker` 复查，**不计费**。
