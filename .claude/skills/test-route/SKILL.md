---
name: test-route
description: DataHub 单条路由的定向测试：只启动被测路由所需的 mock 与 relay（memory 存储，不碰线上 PG/Redis），只跑该路由自己的用例，测试执行统一交给 Composer 2.5。Use when 用户要测试、回归、验证某一条 DataHub 路由（x1/v9/v8/zlf/blk/rlbd1/rlbd2/sfzhy/xfjy/tsfx/lxf/grgjj/grsb/sfsm），或刚接入完一个上游要验证对应路由，或提到「只测某个路由」「单路由测试」「别把不相干的服务都起起来」。
---

# DataHub 单路由定向测试

## 三条硬性约束

1. **只测被测路由**：只跑 `test/cases/*_<route>_*.go`，不跑全量 `test/cases/*.go`。
2. **用不到的 mock 不启动**：只构建并启动该路由上游对应的 mock 进程，其余一律不起。
3. **测试执行交给 Composer 2.5**：跑测试的那一步必须派 `Task` 子代理，`model` 指定 `composer-2.5-fast`（Composer 2.5）。不要在主会话里直接跑测试命令。

## 工作流

### Step 1：确定路由名

从用户话里取路由名（小写，如 `grsb`）。说不清就用 `AskQuestion` 让其在下表中选，不要猜。

### Step 2：派 Composer 2.5 子代理执行

用 `Task` 工具，`subagent_type: "shell"`、`model: "composer-2.5-fast"`、`run_in_background: false`，prompt 模板：

```
仓库：d:\workspace\DataHub（cwd 设为此目录）

只执行这一条命令，不要改任何代码、不要跑别的测试：
  powershell -ExecutionPolicy Bypass -File .\test\route.ps1 -Route <ROUTE>

该脚本只起 <ROUTE> 所需的 mock + relay(memory)，只跑 test/cases/*_<ROUTE>_*.go。

跑完回报：
1. 脚本退出码；
2. 每个用例套件的 "N passed, N failed, N skipped" 汇总行；
3. 若有 FAIL：把失败用例名、期望/实际、以及 test_res/<日期>/relay.err.log 与
   mock_*.err.log 里的相关报错原样贴出（不要自行推测原因）；
4. REPORT.md 的绝对路径。
```

### Step 3：只汇报该路由的结论

先给结论（该路由 N/N 通过，或哪几条失败），再给细节。**不要**把 `REPORT.md` 里当日其它路由的历史套件当成本次结果——本次只重跑了该路由的套件，脚本末尾会打印 `本次重跑套件：...`，以它为准。

### Step 4：失败时定位

按此顺序看，定位到具体一层再动手：

| 现象 | 先看 |
|---|---|
| `relay /healthz not ready` | `test_res/<日期>/relay.err.log`——多为生成的配置缺字段或端口被占 |
| 网关侧错误码（`5050xx`） | `relay.log` + `internal/domain/parse/parse.go` 的该路由校验分支 |
| 上游侧错误 / 解密失败 | `test_res/<日期>/mock_<kind>.err.log`——多为凭证与 mock 缺省值不一致 |
| 用例断言不符 | 该路由 `test/cases/NN_<route>_query.go` 的期望值 |

## 覆盖范围

**跑**：`test/cases/*_<route>_*.go`（该路由主接口全场景：查得/查无/签名错/未知 appKey/入参校验/二次查得；`grgjj` 额外含 `21_grgjj_failover.go` 双源串行寻源）。

**不跑**（本 skill 的边界，需要时明确另说）：`00_connectivity` / `01_health_routes` / `04_found_count` / `06_admin_crud` / `08_version_isolation` / `11_license_route_stats` 等全局套件、Go 单测（`go test ./...`）、真实上游联调（`07_real_gama_smoke`）。要跑全量带线上 PG/Redis 的套件，用 `test/run.ps1`，不属于本 skill。

## 路由 → mock 对照

`test/route.ps1` 内置此表（凭证对齐 `scripts/mock_*.go` 的 env 缺省假值）：

| 路由 | 上游 kind | 启动的 mock | 用例 |
|---|---|---|---|
| x1 | gama | mock_gama :9112 | 02 |
| v9 | income | mock_income :9113 | 03 |
| v8 | income | mock_income :9113 | 05 |
| zlf | rental | mock_rental :9114 | 09 |
| blk | blacklist | mock_blacklist :9115 | 10 |
| rlbd1 | facecompare | mock_facecompare :9117 | 13 |
| rlbd2 | facecompare | mock_facecompare :9117 | 15 |
| sfzhy | idverify | mock_idverify :9118 | 14 |
| xfjy | consumetxn | mock_consumetxn :9119 | 16 |
| tsfx | complaint | mock_complaint :9120 | 17 |
| lxf | lxscore | mock_lxscore :9122 | 18 |
| grgjj | incomeag + bgjj | mock_incomeag :9123 + mock_bgjj :9125 | 19 + 21 |
| grsb | bgpg | mock_bgpg :9126 | 22 |
| sfsm | idcheck | mock_idcheck :9127 | 23 |

## 脚本行为

`test/route.ps1 -Route <route>` 依次做：生成 `test_res/<日期>/config.route.<route>.yaml`（`storage.driver: memory`、`demo.seed: true`、`demo.appSecret: demo-app-secret` = `test/harness.Secret`，`versions` 下**只有该路由**）→ 构建 `relay.exe` 与所需 mock → 启动 → 等 `/healthz` → 按序 `go run` 该路由用例 → `go run test/report.go` 汇总 `REPORT.md` → `finally` 停服。

可选参数：`-KeepAlive` 跑完保留进程供手工调接口（回车停服）；`-ConfigFile <path>` 用现成配置替代自动生成（如对真实上游）。

## 新增路由后的登记

按 `add-upstream` skill 接完新路由后，在 `test/route.ps1` 的 `$routeMap` 追加一项即可：

```powershell
"<route>" = @{
    mocks = @(@{ name = "mock_<kind>"; port = <port> })
    yaml  = @'
      - kind: "<kind>"
        baseURL: "http://127.0.0.1:<port>/<path>"
        # 其余凭证字段照抄 scripts/mock_<kind>.go 里 env() 的缺省值
'@
}
```

用例文件按 `test/cases/NN_<route>_query.go` 命名，脚本靠 `*_<route>_*.go` 自动匹配，无需再改脚本。

## 注意

`test/route.ps1` 必须存为 **UTF-8 with BOM**：Windows PowerShell 5.1 对无 BOM 的 UTF-8 按 ANSI 解码，脚本里的中文字符串会让解析器报「字符串缺少终止符」。
