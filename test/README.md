# DataHub 固定测试套件（test/）

一套可重复执行的全链路测试。每次需要测试时，运行根目录入口脚本即可：它会启动本地 mock 上游 + relay（连接你在阿里云的线上 PostgreSQL + Redis），依次跑完 `test/cases/` 下的所有脚本，把每个脚本的结果写进**以当天日期命名的子目录**，最后汇总成一份易读的 `REPORT.md`。

## 一键运行

```powershell
# 在 DataHub 目录下
pwsh ./test/run.ps1
```

可选参数：

```powershell
pwsh ./test/run.ps1 -ConfigFile config.aliyun.e2e.yaml   # 默认即此，连线上 PG+Redis
pwsh ./test/run.ps1 -SkipReal                            # 跳过真实 gama 连通性 smoke
```

运行后结果在：`test_res/<YYYY-MM-DD>/`，其中：

- `<suite>.json`：每个脚本的结构化结果（机器可读）。
- `<suite>.log`：每个脚本的完整 stdout（人类可读）。
- `relay.log` / `mock_gama.log`：服务端日志，排错用。
- `REPORT.md`：**最终汇总报告**，逐接口/功能给出"通过/失败/跳过 + 原因"。

## 架构与连通性

- relay 以 `CONFIG_FILE=config.aliyun.e2e.yaml` 启动，存储后端 = **线上阿里云 PostgreSQL + Redis**；上游 gama 默认指向本地 mock（`scripts/mock_gama.go`，:9112），保证主测试矩阵确定可重复。
- relay 启动会自动跑迁移（含 `0004_drop_dim2.sql`）并 seed demo license（`appKey=y89098io` / `secret=demo-app-secret`）。
- `00_connectivity` 会**直接** ping 线上 PG + Redis，确认本机确实连得上。

## 对线上数据的影响（已尽量降到最低）

- 计数类断言用"前后差值"，不依赖绝对值；demo license 的 `serviceUsed` 会随每次成功查得累计（正常现象）。
- `06_admin_crud` 创建的临时用户用完即删；`05_ip_whitelist` 用 `defer` 复原全局白名单。
- 审计日志为追加写、不可回收，会随每次运行累积（报告中会注明）。

---

## 各脚本说明（test/cases/）

| 脚本 | 测什么 | 预期结果 | 可能出现的情况/报错 |
|---|---|---|---|
| `00_connectivity.go` | 直连线上 PostgreSQL + Redis 并 PING | 两者均 PASS | PG/Redis 不可达（防火墙/白名单/密码错）→ FAIL，原因为连接错误文本 |
| `01_health_routes.go` | `/healthz` 与 x1 / quota / v9 四条路由的可达性 | healthz 返回 `ok`；各业务路由返回 JSON 信封（非 404） | relay 未起来 → 连接错误；路由未注册 → 404 |
| `02_x1_query.go` | 主接口 `POST querySrmxX1` 全场景 | 成功 `errorCode=0/body.code=001/range=7`；查无 `body.code=999`；错签 `505002`；未知 appKey `505004`；缺 appKey `505001`；手机号/身份证非法 `505062`；SUSPENDED 用户 `505007` | mock 未起 → 上游错误 `505062`；线上库异常 → 台账写入失败 `505062` |
| `03_v9_query.go` | 旧版 `GET v9` 兼容接口全场景 | 成功 `code=001/range=7`；查无 `999`；错签 `013`；account空 `009`；reqid空或>20 `008`；idCard非法 `005`；mobile非法 `020`；verify空 `011`；同 reqid 幂等 | 同 x1 上游/库异常 → `012` |
| `04_found_count.go` | 成功查得数统计 + 无额度限制 | N 次成功 + M 次查无后，`serviceUsed` 增量==N；任何场景都不返回 1001/1006，不被拦截 | 计数漂移（并发/复查）→ 增量≠N 时 FAIL 并给出实际增量 |
| `05_ip_whitelist.go` | 全局 IP 白名单（管理端）+ per-user 白名单 | 设为不匹配 CIDR 后 x1 被拦 `505002`、v9 被拦 `012`；复原后恢复正常 | 复原失败会导致后续被拦——脚本用 `defer` 保证复原 |
| `06_admin_crud.go` | 管理后台全流程 | 登录(对/错)、建用户(返回 secret)、查/列、改(SUSPENDED)、轮换密钥(旧签失败/新签成功)、删、审计(过滤+PII 掩码)、无 token `401` | 登录失败 → 后续 JWT 步骤 SKIP |
| `07_real_gama_smoke.go` | 可选：直连真实 gama 上游 | 提供 `config.gama.real.yaml` 且 IP 已白名单 → 一次真实 x1 查询 PASS | 缺该配置文件 → **SKIP**；IP 未白名单/上游报错 → **SKIP**（不计失败） |

> 说明：所有业务接口无论成功/失败均返回 HTTP 200，错误体现在信封里的 `head.errorCode`（x1）或 `code`（v9）。

## 真实 gama 连通性 smoke 的启用方式（可选）

`07_real_gama_smoke.go` 默认跳过。若要真正打通真实上游，在 DataHub 目录放一个 `config.gama.real.yaml`（已在 `.gitignore`，**不要提交**），填入真实 gama 凭证与可访问的 baseURL，例如：

```yaml
upstream:
  provider: "gama"
  timeout: "6s"
  gama:
    baseURL: "https://<真实域名>/enol/api/v1/doCheck"
    appId: "<真实 appId>"
    appSecret: "<真实 appSecret>"
    apiKey: "gama_ctmz_layer_score"
```

脚本会用该配置另起一个临时 relay 实例（独立端口）发一次查询；若上游因 IP 未白名单/鉴权失败返回错误，则记为 SKIP 并附原因。

## 退出码

- 每个 case 脚本：有任意 FAIL → 退出码 1，否则 0（SKIP 不算失败）。
- `run.ps1`：任一脚本失败则整体退出码非 0，便于 CI 接入。
