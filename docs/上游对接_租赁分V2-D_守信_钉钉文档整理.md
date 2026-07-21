# 租赁分V2-D 接口文档（授权协议版本）— 上游对接整理

> 来源：守信（shouxin168）提供的钉钉在线文档
> https://alidocs.dingtalk.com/i/p/rWO4Gj87WkkXDRM0BgX4YBy9A2ME3m8e
> 整理日期：2026-07-06。本文对应本服务 **zlf 路由** 的上游（`internal/infrastructure/upstream/rental.go`）。
> 计费提示（原文）：产品调用按**查得次数**计费，请做好查询设置，避免短时间内多次发起调用造成损失。

## 接口地址

**（需要添加服务器白名单）**

| 环境 | URL |
|---|---|
| 测试（沙箱） | `http://sit-shouwei.shouxin168.com/sandbox/lightning/product/query` |
| 生产 | `https://shouwei.shouxin168.com/api/lightning/product/query` |

## 1 数据流转

1. 构造请求数据：按接口规范构造业务数据，程序内做 AES 加密；
2. 发送请求数据：POST（form-data 格式）发给上游；
3. 上游接收请求：解析、拆包、格式校验，合法则处理；
4. 返回响应数据；
5. 合作方对响应做业务处理。

## 2 Request 请求

### 2.1 请求方法
POST

### 2.2 请求格式
- 格式1：`Content-Type: multipart/form-data`
- 格式2：`Content-Type: application/x-www-form-urlencoded`

### 2.3 请求参数（form 字段）

| 字段 | 类型 | 必传 | 含义 |
|---|---|---|---|
| `institution_id` | String | 是 | 上游给客户分配的唯一编号 |
| `biz_data` | String | 是 | 业务数据（见 2.5），需 AES 加密（见 2.4），可用在线 AES 网站校验 |

文档 Postman 示例（沙箱调试凭证，来自文档截图）：
- URL：`http://sit-shouwei.shouxin168.com/sandbox/lightning/product/query`
- `institution_id = d6518f1e-9270-11e9-890c-9801a79f5a77`（沙箱示例值）
- `biz_data = 1l5DQCiax9ZUCyRUnfwmqB8n1Le+jhtzeMWRx…`（密文示例，截图截断）

### 2.4 加密过程

- **AES 分组模式 `ECB`，填充 `PKCS5Padding`**（在线工具中对应 PKCS7 / 128bits / UTF-8）。
- 流程：`biz_data` 明文 JSON 化 → 用上游分配的 AES 密钥加密 → 密文 **Base64** 编码 → 放入 `biz_data` 字段。
- 在线校验工具（原文推荐）：https://the-x.cn/cryptography/Aes.aspx
- 文档演示用的**沙箱 AES 密钥**：`Q0ymUIe1t26ZfG7s`（16 字节；生产密钥由商务另行分配）。
- 文档演示解密后的明文样例（脱敏）：

```json
{
  "name": "马xx",
  "ident_number": "33028xxxxxxxx489",
  "phone": "187xxxx8084",
  "service": "beethoven_score_service",
  "mode": "mode_beethoven_score",
  "encryption": "plaintext"
}
```

> 注意：上面截图样例的 service/mode 是其它产品的演示值；租赁分V2-D 用 2.5 的默认值。

### 2.5 业务数据内容（biz_data 明文 JSON）

| 字段 | 类型 | 必传 | 含义 |
|---|---|---|---|
| `name` | String | 是 | 姓名 |
| `phone` | String | 是 | 手机号 |
| `ident_number` | String | 是 | 身份证 |
| `encryption` | String | 否 | 加密方式，md5 |
| `service` | String | 是 | 接口名称，默认值 `"buer_unique_service"` |
| `mode` | String | 是 | 接口模式，默认值 `"mode_rent_score_v2_d"` |
| `licenseUrl` | String | 是 | 授权书 OSS 地址（需先上传到布尔提供的 OSS），支持 pdf/jpg/jpeg/png/bmp。样例：`https://shouwei.oss-cn-shanghai.aliyuncs.com/approve_files/XXX.pdf` |
| `licenseType` | Number | 是 | 授权书类型（0:图片 1:pdf，样例模板中为 0） |

**授权协议 OSS 参数**（上游分配，用于上传授权书）：

- endpoint: `oss-cn-shanghai.aliyuncs.com`
- bucketName: `shouwei`
- objectName: **必须以 `approve_files/` 开头**
- accessKeyId / accessKeySecret：**真实凭证，不写入本文档**——已配置在
  `config.aliyun.prod.yaml`（被 .gitignore 忽略，不提交）的 `versions.zlf.upstream.oss` 段。

biz_data 明文模板（原文，示例是脱敏的，实际使用需真实数据）：

```json
{
  "name": "xxxxx",
  "phone": "xxxxx",
  "ident_number": "xxxx",
  "service": "buer_unique_service",
  "mode": "mode_rent_score_v2_d",
  "licenseUrl": "授权书oss地址",
  "licenseType": 0
}
```

## 3 Response 响应

### 3.1 响应格式
JSON 返回：`Accept: application/json;charset=utf-8`

### 3.2 响应字段

外层字段：

| 字段 | 类型 | 含义 |
|---|---|---|
| `resp_code` | String | 响应状态码（见 §4） |
| `resp_data` | String/Object | 响应主体数据 |
| `resp_msg` | String | 状态码文字描述 |
| `resp_order` | String | 响应订单号 |
| `timestamp` | String | 时间戳 |

响应主体（`resp_data`）：

| 字段 | 含义 |
|---|---|
| `score1` | 租赁分（500-700）：[500-550] 高风险 /(550-590] 中 /(590-700] 低 |

返回成功示例（原文）：

```json
{
  "resp_code": "SW0000",
  "resp_msg": "查询成功",
  "timestamp": 1721198230300,
  "resp_order": "lgt17211982299952",
  "resp_data": {
    "score1": 546.6
  }
}
```

## 4 响应状态码对照表

| resp_code | 含义 | 计费 |
|---|---|---|
| SW0000 | 查询成功 | **收费** |
| SW0001 | 认证失败 | 不收费 |
| SW0002 | 查无记录 | 不收费 |
| SW0003 | 通道超时或异常 | 不收费 |
| SW0017 | biz_data 参数错误 | 不收费 |
| SW0030 | 签名为空 | 不收费 |
| SW0031 | 客户公钥为空 | 不收费 |
| SW0032 | 验签失败 | 不收费 |
| SW0033 | 验签错误 | 不收费 |
| SW0034 | 解密失败 | 不收费 |
| SW0040 | 产品未开通 | 不收费 |
| SW0041 | 调用量已达上限 | 不收费 |
| SW0042 | 账户余额不足 | 不收费 |

> 注：表格截图读取到 SW0017 为止，若原文下方还有零星状态码（如更多参数类错误），以钉钉原文为准。

## 5 demo

原文附 Python 版 demo（AES/ECB/PKCS5 加密 biz_data 后 form POST），与本仓库
[rental.go](../internal/infrastructure/upstream/rental.go) + [aesecb.go](../internal/infrastructure/upstream/aesecb.go)
的实现一致，未逐行转录。

## 实测记录（2026-07-06，本机开发环境）

用文档公开的沙箱凭证（institution_id `d6518f1e-...5a77` + AES key `Q0ymUIe1t26ZfG7s`）、
合法格式的虚构身份（张三/330129199109094318/13809091009）按 2.4 流程加密后 form POST：

| 目标 | 结果 | 报文 |
|---|---|---|
| 沙箱（sit-shouwei…/sandbox/…） | ✅ **全链路通** | `{"resp_code":"SW0000","resp_msg":"查询成功",…,"resp_data":{"score1":586.6}}` |
| 沙箱（身份证校验位错误时） | 参数校验生效 | `{"resp_code":"SW1010","resp_msg":"身份证号格式有误",…}`（**SW1010 不在 §4 表内**，实测发现） |
| 生产（shouwei…/api/…） | ❌ 本机不可达 | 无 HTTP 报文：TCP 可连，**TLS 握手即被对端断开**（curl: `failed to receive handshake`；Go: `EOF`） |

结论：
- 沙箱不强制 `licenseUrl`（带/不带均 SW0000）；身份证校验位必须真实合法。
- 生产端表现与"**需要添加服务器白名单**"一致：非白名单来源在 TLS 层直接被断开，拿不到任何业务报文。
  本机流量经代理（TUN/fake-ip），出口 IP 不固定，本就不可能在白名单内；生产连通性需在
  **已报备白名单的服务器**（生产 ECS）上验证。

## 与本服务（zlf 路由）的映射备忘

- `config.yaml` → `versions.zlf.upstream`：`kind: rental`、`baseURL`（上表生产 URL）、`institutionId`、`aesKey`、`service`/`mode`（可省用默认）、`licenseFile`/`licenseType` + `oss.*`（上表 OSS 参数）。
- 本服务启动时把固定授权书上传 OSS 一次，缓存 `licenseUrl` 复用（见 `cmd/relay/main.go` buildUpstream 的 rental 分支）。
- 上游 `SW0000` → 下游 `001` 查得（`result.range` = `score1`）；`SW0002` → `999` 查无；其余码视为上游侧错误（不计费，走复查/对账）。
- **调用方服务器出口 IP 需先加入上游白名单**（测试/生产均需）。
