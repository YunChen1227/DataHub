---
name: api-doc
description: 编写/修改/生成本网关某条路由的对外《API 接口文档与使用手册》（唯一交付物为 PDF），并与代码逐项核对出入参一致性。只要用户提到「使用手册」「API 文档」「接口文档」「生成/更新某路由的文档」「文档转 pdf」，即使没明说要用 skill，也必须使用本 skill——它固化了模板结构、版本隔离与上游隐匿铁律、代码核对锚点与 pdf 产出命令，照单执行，不要自行发明格式。
---

# 对外 API 使用手册编写规范

本网关每条路由（版本）各有一份对外使用手册，交付给该路由的客户。手册的质量红线
是四条：**版本隔离、上游隐匿、与代码一致、自包含**。其余全是照模板生产。

## 铁律（违反任何一条即返工）

1. **版本隔离**：每份文档的读者不得从任何措辞察觉其他版本/路由的存在。
   - 禁止出现其他路由名（x1/v9/v8/zlf/blk/hlt…按当前仓库 `model.Versions` 为准）；
   - 禁止"与 X 一致 / 与其他版本的差异 / 已接入 X 的客户可直接切换"这类对比句；
   - 禁止引用其他文档（如"加签示例见《…》第 2.4 节"）——一切内容必须内联；
   - **唯一例外**：同一数据服务因版本升级拆出的版本组（如主仓 v8/v9）按业务要求
     写在同一份文档里，组内互相说明是允许的，但仍不得提组外版本。
2. **上游隐匿（手册只描述"如何使用本服务"）**：读者不得从任何措辞察觉本服务是
   转发网关或存在第三方数据提供方。
   - **禁止出现**："上游"、"数据提供商"、"转发/转接"、"对接 XX 平台"、上游产品名/
     公司名/产品码（busiCode、SW 码、P 码等上游侧代码）、"上游订单号/上游流水号/
     上游状态码"等一切暴露来源的措辞；
   - 一切行为都以**本服务**为主语改写：
     - "参数非法不调用上游、不计费" → "参数非法直接拒绝，不计费"；
     - "上游侧异常归一为 505062" → "服务处理异常统一返回 505062"；
     - "uid = 上游订单号" → "uid = 交易流水号（对账用）"；
     - 4.2 业务码表**不设**"上游对应"列，只写含义与计费；
     - range 内如有原始状态码字段（如 rawStatus），描述为"原始状态码（备查）"，
       不解释其来源；
   - "数据源/数据段"仅可用于描述**本服务自身**的聚合结构（如 swfp 的四个数据段），
     不得指向外部机构。
3. **与代码一致**：动笔前先按「代码核对锚点」逐项核对，文档描述以代码为准。
4. **自包含 + 真实域名**：完整地址写真实域名端口；「通信协议」措辞必须与域名
   scheme 一致（`http://` 就写 HTTP，别写 HTTPS）；加签代码（Java/Python/Go
   三份）必须整段内联在 2.4 节。

## 模板结构（七段式，参考既有 PDF 的结构改写，不要从零发明）

选模板：`result.range` 是**纯评分字符串** → 参考 zlf 版 PDF；是**富对象 JSON 字符串**
（需二次 JSON.parse，含 3.1.5 结构说明节）→ 参考 blk/hlt/swfp 版 PDF。

```
标题：<业务名>查询服务（<route>）· API 接口文档与使用手册
引言 blockquote：版本｜通信：HTTP + JSON｜编码 UTF-8；统一信封说明；关键特性
一、接入必读        1.1 适用范围 / 1.2 接入须知 / 1.3 接口说明表 / 1.4 环境说明
二、鉴权与加签      2.1 请求信封 / 2.2 鉴权校验顺序 / 2.3 加签方式 / 2.4 代码示例(内联)
三、接口列表        3.1 主查询(路径/完整地址/入参/请求响应示例[/3.1.5 range结构])
                    3.2 成功查得数查询(quota) / 3.3 健康检查(/healthz)
四、返回码说明      4.1 网关 head.errorCode 表 / 4.2 业务 body.code 表(含义+计费，无来源列)
五、计费说明        仅 001 计费；999 与网关级错误不计费；台账幂等
六、使用手册        6.1 接入流程 / 6.2 幂等与重试 / 6.3 错误处理建议 / 6.4 自检清单
附录：术语表
```

## 代码核对锚点（动笔前逐项过，文档跟代码走）

| 文档内容 | 代码位置 | 核对点 |
|---|---|---|
| 鉴权校验顺序 §2.2 | `internal/domain/auth/service.go` Authenticate | 505001→505004→505007→505002 的顺序 |
| 错误码表 §4.1 | `internal/common/errs/errs.go` errorCodeByBusi/defaultMsg | 码值与 errorMsg 文案 |
| 入参校验 §3.1.1 | `internal/domain/parse/parse.go` | 该路由实际挂载的校验器（main.go buildRouteStack 的 WithParser）决定字段与必填口径 |
| 签名算法 §2.3 | `internal/domain/auth/md5.go` Sign | ASCII 升序、剔空值、小写 hex、信封字段不参与 |
| 响应字段 §3.1.3 | `internal/domain/mapping/mapping.go` + `model/model.go` QueryBody | head/body 字段名、result 出现条件（001，聚合路由还含 002） |
| quota 响应 §3.2 | `internal/api/handler.go` quota 响应结构体 | **逐字段对**（如 serviceUsed、totalCalls——历史上漏过 totalCalls） |
| 业务码语义 §4.2 | `internal/infrastructure/upstream/<kind>.go` Query | 001/999(/002) 判定条件、uid 语义（对外一律写"交易流水号"）、异常归一 505062 |
| 超时时间 §1.3 | 该仓库 `config.example.yaml` upstream.timeout | 别照抄别的仓库（主仓 4s、health 仓 8s；多步/聚合调用要写更长的建议读超时） |
| 路径/完整地址 §3.1 | 路由自动生成 `querySrmx<ROUTE>`/`quota<ROUTE>`（大写） | 域名端口问用户，勿臆测 |

## 产出流程（唯一交付物是 PDF；md/html 只是临时中间产物）

**docs/ 与 git 里只允许存在 `API_接口文档与使用手册_<route>.pdf`**。起草用的 md 和
渲染用的 html 一律放会话临时目录（scratchpad），生成 PDF 后即弃，不进 docs/、
不进 git。后续要修改手册时，以现有 PDF 内容为准在临时目录重新起草 md。

1. 在临时目录起草 `<scratchpad>/manual_<route>.md`。
2. md → html（临时）：
   `python .claude/skills/api-doc/scripts/md2html.py <scratchpad>/manual_<route>.md <scratchpad>/manual_<route>.html`
   （转换器只支持手册用到的语法子集：#标题/表格/```代码块/列表/引用/粗体/行内码/
   `---`/内部锚点链接转「」；写 md 时别用其它花哨语法。）
3. html → pdf（Chrome headless，必须用独立 user-data-dir 否则与已开 Chrome 冲突报拒绝访问），
   PDF 直接输出到 docs/：
   ```bash
   TMPUD=$(mktemp -d) && \
   "/c/Program Files/Google/Chrome/Application/chrome.exe" --headless --disable-gpu \
     --no-first-run --user-data-dir="$(cygpath -w "$TMPUD")" --no-pdf-header-footer \
     --print-to-pdf="$(cygpath -w "<repo>/docs/API_接口文档与使用手册_<route>.pdf")" \
     "$(cygpath -w "<scratchpad>/manual_<route>.html")"; rm -rf "$TMPUD"
   ```
   看到 `bytes written to file` 即成功（GCM/PHONE_REGISTRATION 报错行是无害噪音）。
4. 删除临时 md/html；确认 docs/ 里没有本路由的 .md/.html 残留（历史遗留的也要
   `git rm` 掉）。

## 终检清单（全部通过才算完成，对临时 html 执行文本检查）

- [ ] 交叉引用扫描：grep 所有**不属于本文档**的路由名 + 「其他版本/别的版本/
      与 X 一致」，命中数必须为 0（注意排除误报，如产品名里的 V35 不是 v3）。
- [ ] **上游隐匿扫描**：grep 「上游|数据提供商|转发|转接|对接」+ 该路由上游的
      产品名/公司名/上游侧状态码词（busiCode、SW\d、P01 等），命中数必须为 0。
- [ ] `grep '](#'` 无残留 markdown 链接语法。
- [ ] 协议一致：域名是 http 则全文无 HTTPS 措辞残留（`grep -n HTTPS`）。
- [ ] quota 响应字段与 `internal/api/handler.go` 逐字段一致。
- [ ] 示例 JSON 里的 appKey 用该路由的 demo appKey（`model.DemoAppKey`），不要串。
- [ ] 敏感信息：文档中不得出现任何真实密钥/凭证。
- [ ] **交付物检查**：docs/ 里只新增/更新了 `.pdf`；无 `.md`/`.html` 落盘或入 git。
