---
name: api-doc
description: 编写/修改/生成本网关某条路由的对外《API 接口文档与使用手册》（md → html → pdf 全链路），并与代码逐项核对出入参一致性。只要用户提到「使用手册」「API 文档」「接口文档」「生成/更新某路由的文档」「文档转 pdf」，即使没明说要用 skill，也必须使用本 skill——它固化了模板结构、版本隔离铁律、代码核对锚点与 pdf 产出命令，照单执行，不要自行发明格式。
---

# 对外 API 使用手册编写规范

本网关每条路由（版本）各有一份对外使用手册，交付给该路由的客户。手册的质量红线
只有三条：**版本隔离、与代码一致、自包含**。其余全是照模板生产。

## 铁律（违反任何一条即返工）

1. **版本隔离**：每份文档的读者不得从任何措辞察觉其他版本/路由的存在。
   - 禁止出现其他路由名（x1/v9/v8/zlf/blk/hlt…按当前仓库 `model.Versions` 为准）；
   - 禁止"与 X 一致 / 与其他版本的差异 / 已接入 X 的客户可直接切换"这类对比句；
   - 禁止引用其他文档（如"加签示例见《…》第 2.4 节"）——一切内容必须内联；
   - **唯一例外**：同一上游因版本升级拆出的版本组（如主仓 v8/v9）按业务要求写在
     同一份文档里，组内互相说明是允许的，但仍不得提组外版本。
2. **与代码一致**：动笔前先按「代码核对锚点」逐项核对，文档描述以代码为准。
3. **自包含 + 真实域名**：完整地址写真实域名端口；「通信协议」措辞必须与域名
   scheme 一致（`http://` 就写 HTTP，别写 HTTPS）；加签代码（Java/Python/Go
   三份）必须整段内联在 2.4 节。

## 模板结构（七段式，整篇复制现有文档改写，不要从零写）

选模板：`result.range` 是**纯评分字符串** → 参考 zlf 版；是**富对象 JSON 字符串**
（需二次 JSON.parse，含 3.1.5 结构说明节）→ 参考 blk/hlt 版。

```
标题：<业务名>查询服务（<route>）· API 接口文档与使用手册
引言 blockquote：版本｜通信：HTTP + JSON｜编码 UTF-8；统一信封说明；关键特性
一、接入必读        1.1 适用范围 / 1.2 接入须知 / 1.3 接口说明表 / 1.4 环境说明
二、鉴权与加签      2.1 请求信封 / 2.2 鉴权校验顺序 / 2.3 加签方式 / 2.4 代码示例(内联)
三、接口列表        3.1 主查询(路径/完整地址/入参/请求响应示例[/3.1.5 range结构])
                    3.2 成功查得数查询(quota) / 3.3 健康检查(/healthz)
四、返回码说明      4.1 网关 head.errorCode 表 / 4.2 业务 body.code 表(上游对应列)
五、计费说明        仅 001 计费；999 与网关级错误不计费；台账幂等
六、使用手册        6.1 接入流程 / 6.2 幂等与重试 / 6.3 错误处理建议 / 6.4 自检清单
附录：术语表
```

## 代码核对锚点（动笔前逐项过，文档跟代码走）

| 文档内容 | 代码位置 | 核对点 |
|---|---|---|
| 鉴权校验顺序 §2.2 | `internal/domain/auth/service.go` Authenticate | 505001→505004→505007→505002 的顺序 |
| 错误码表 §4.1 | `internal/common/errs/errs.go` errorCodeByBusi/defaultMsg | 码值与 errorMsg 文案 |
| 入参校验 §3.1.1 | `internal/domain/parse/parse.go` | 手机号 `^1\d{10}$`、身份证 `^\d{17}[\dX]$`、name 必填性看该路由上游 client |
| 签名算法 §2.3 | `internal/domain/auth/md5.go` Sign | ASCII 升序、剔空值、小写 hex、信封字段不参与 |
| 响应字段 §3.1.3 | `internal/domain/mapping/mapping.go` + `model/model.go` QueryBody | head/body 字段名、result 仅 001 时存在 |
| quota 响应 §3.2 | `internal/api/handler.go` quota 响应结构体 | **逐字段对**（如 serviceUsed、totalCalls——历史上漏过 totalCalls） |
| 业务码语义 §4.2 | `internal/infrastructure/upstream/<kind>.go` Query | 001/999 判定条件、uid 的实际含义（上游流水号/authCode/订单号）、上游错误码归一 505062 |
| 超时时间 §1.3 | 该仓库 `config.example.yaml` upstream.timeout | 别照抄别的仓库（主仓 4s、health 仓 8s，两步上游调用要写更长的建议读超时） |
| 路径/完整地址 §3.1 | 路由自动生成 `querySrmx<ROUTE>`/`quota<ROUTE>`（大写） | 域名端口问用户，勿臆测 |

## 产出流程（md 是唯一源，html/pdf 都是生成物）

1. 编辑 `docs/API_接口文档与使用手册_<route>.md`。
2. md → html：用本 skill 自带转换器（样式与既有文档统一）：
   `python .claude/skills/api-doc/scripts/md2html.py docs/API_接口文档与使用手册_<route>.md docs/API_接口文档与使用手册_<route>.html`
   （转换器只支持手册用到的语法子集：#标题/表格/```代码块/列表/引用/粗体/行内码/`---`/内部锚点链接转「」；写 md 时别用其它花哨语法。）
3. html → pdf（Chrome headless，必须用独立 user-data-dir 否则与已开 Chrome 冲突报拒绝访问）：
   ```bash
   cd docs && TMPUD=$(mktemp -d) && \
   "/c/Program Files/Google/Chrome/Application/chrome.exe" --headless --disable-gpu \
     --no-first-run --user-data-dir="$(cygpath -w "$TMPUD")" --no-pdf-header-footer \
     --print-to-pdf="$(cygpath -w "$PWD/API_接口文档与使用手册_<route>.pdf")" \
     "$(cygpath -w "$PWD/API_接口文档与使用手册_<route>.html")"; rm -rf "$TMPUD"
   ```
   看到 `bytes written to file` 即成功（GCM/PHONE_REGISTRATION 报错行是无害噪音）。

## 终检清单（全部通过才算完成）

- [ ] 交叉引用扫描：对本文档 grep 所有**不属于本文档**的路由名 + 「其他版本/别的
      版本/与 X 一致」，命中数必须为 0（注意排除误报，如产品名里的 V35 不是 v3）。
- [ ] `grep '](#' <html>` 无残留 markdown 链接语法。
- [ ] 协议一致：域名是 http 则全文无 HTTPS 措辞残留（`grep -n HTTPS`）。
- [ ] quota 响应字段与 `internal/api/handler.go` 逐字段一致。
- [ ] 示例 JSON 里的 appKey 用该路由的 demo appKey（`model.DemoAppKey`），不要串。
- [ ] pdf 重新生成过（md 改动后 html/pdf 必须重出，三者内容一致）。
- [ ] 敏感信息：文档中不得出现任何真实密钥/凭证（上游凭证、OSS key 等一律不进文档）。
