# 人脸身份证比对（rlbd1 / rlbd2）— 延迟测试说明

> 对应接口文档：`docs/API_接口文档与使用手册_rlbd1.pdf`、`docs/API_接口文档与使用手册_rlbd2.pdf`

本文档用于在**服务器侧**调用 rlbd1 / rlbd2 网关接口，统计单次/多次调用的网络与服务耗时。

rlbd1 与 rlbd2 **协议完全一致**，仅路由路径和 appKey 不同（独立账户/独立统计）。

---

## 1. 接口信息

| 项目 | rlbd1 | rlbd2 |
|------|-------|-------|
| 路径 | `POST /v1/openapi/zlx/querySrmxRLBD1` | `POST /v1/openapi/zlx/querySrmxRLBD2` |
| 服务地址 | `http://aiszcloud.cn:8080` 或 `http://aiszcloud.com.cn:8080` | 同上 |
| 鉴权 | appKey + MD5 签名 | 同上 |

### 1.1 请求信封

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `appKey` | String | 是 | 我方分配的账户标识 |
| `sign` | String | 是 | 对 body 业务参数的 MD5 签名 |
| `encryptionType` | int | 否 | 默认 `1`（明文） |
| `body` | Object | 是 | 业务参数 |

### 1.2 body 业务参数

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | String | 是 | 姓名（明文） |
| `idCard` | String | 是 | 身份证号（18 位，末位可为 X） |
| `image` | String | 否 | 人像照片 Base64，与 `url` 二选一 |
| `url` | String | 否 | 人像照片 URL，与 `image` 二选一 |

### 1.3 加签规则

1. 取 body 中所有**非空**业务参数
2. 按 key **ASCII 升序**排序
3. 拼接为 `key1value1key2value2...`，末尾追加 `appSecret`
4. 对整串做 **MD5**，取 32 位小写 hex 作为 `sign`

示例（body 含 `idCard`、`name`、`url`）：

```text
待签名串 = idCard330129199109094312name张三urlhttp://img.example.com/a.jpg<appSecret>
sign = MD5(待签名串).hexdigest()
```

### 1.4 响应耗时字段

成功或失败时，响应 `head.time` 均为服务端处理耗时（毫秒）。脚本会同时统计：

| 指标 | 含义 |
|------|------|
| `client_ms` | 客户端 `perf_counter()` 包裹的整个 HTTP 往返 |
| `requests_ms` | `requests` 库的 `response.elapsed` |
| `server_ms` | 响应中的 `head.time`（网关侧处理耗时） |

---

## 2. 测试脚本

文件：`docs/人脸身份证比对/test_rlbd_latency.py`

### 2.1 安装依赖

```bash
pip install requests
```

### 2.2 环境变量

| 变量 | 说明 |
|------|------|
| `RLBD_BASE_URL` | 可选，默认 `http://aiszcloud.cn:8080` |
| `RLBD1_APP_KEY` / `RLBD1_APP_SECRET` | rlbd1 账户凭证 |
| `RLBD2_APP_KEY` / `RLBD2_APP_SECRET` | rlbd2 账户凭证 |

### 2.3 rlbd1 测试

```bash
export RLBD1_APP_KEY="你的appKey"
export RLBD1_APP_SECRET="你的appSecret"

# 用照片 URL（适合快速测延迟）
python test_rlbd_latency.py \
  --route rlbd1 \
  --name 张三 \
  --id-card 330129199109094312 \
  --url http://img.example.com/a.jpg

# 用本地照片 + 多次统计
python test_rlbd_latency.py \
  --route rlbd1 \
  --name 张三 \
  --id-card 330129199109094312 \
  --photo /path/to/face.jpg \
  --repeat 10 \
  --interval 0.5
```

### 2.4 rlbd2 测试

```bash
export RLBD2_APP_KEY="你的appKey"
export RLBD2_APP_SECRET="你的appSecret"

python test_rlbd_latency.py \
  --route rlbd2 \
  --name 张三 \
  --id-card 330129199109094312 \
  --url http://img.example.com/a.jpg \
  --repeat 10 \
  --interval 0.5
```

### 2.5 示例输出

```text
=== 人脸身份证比对 (rlbd1) 延迟测试 ===
endpoint: http://aiszcloud.cn:8080/v1/openapi/zlx/querySrmxRLBD1
repeat: 10  interval: 0.5s  timeout: 15.0s

[#1/10] errorCode=0 body.code=001 client_ms=1243.2 requests_ms=1239.8 server_ms=1186 logId=a1b2c3d4
...

--- 耗时汇总 client_ms (客户端全链路, 单位 ms) ---
count=10  min=1102.4  max=1345.6  avg=1218.3  p50=1205.1  p95=1320.8
--- 耗时汇总 server_ms (响应 head.time, 单位 ms) ---
count=10  min=1050.0  max=1280.0  avg=1165.2  p50=1158.0  p95=1265.0
```

---

## 3. 核心示例代码（Python）

```python
import hashlib
import json
import time
import requests

def sign_body(body: dict, app_secret: str) -> str:
    parts = [f"{k}{v}" for k, v in sorted(body.items()) if v]
    return hashlib.md5(("".join(parts) + app_secret).encode("utf-8")).hexdigest()

def query_rlbd(base_url, route, app_key, app_secret, name, id_card, url="", image=""):
    path = "/v1/openapi/zlx/querySrmxRLBD1" if route == "rlbd1" else "/v1/openapi/zlx/querySrmxRLBD2"
    body = {"name": name, "idCard": id_card}
    if image:
        body["image"] = image
    else:
        body["url"] = url

    payload = {
        "encryptionType": 1,
        "appKey": app_key,
        "sign": sign_body(body, app_secret),
        "body": body,
    }

    t0 = time.perf_counter()
    resp = requests.post(
        base_url.rstrip("/") + path,
        headers={"Content-Type": "application/json; charset=utf-8"},
        data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
        timeout=15,
    )
    client_ms = (time.perf_counter() - t0) * 1000
    data = resp.json()
    server_ms = (data.get("head") or {}).get("time")
    print(f"client_ms={client_ms:.1f} server_ms={server_ms} errorCode={(data.get('head') or {}).get('errorCode')}")
    return data
```

---

## 4. 注意事项

1. **超时**：文档建议客户端读超时 ≥ 10 秒（含图片传输），脚本默认 15 秒，可用 `--timeout` 调整。
2. **计费**：仅 `head.errorCode=0` 且 `body.code=001`（查得比对结论）时计费。
3. **照片规格**：JPG/PNG，≤ 500KB，正脸清晰；`image` 与 `url` 至少提供一个。
4. **rlbd1 vs rlbd2**：同一服务地址、同一加签方式，使用各自账户的 appKey/appSecret，互不影响。
5. **排障**：异常时请记录响应中的 `head.logId`。

---

## 5. 上传到服务器

只需上传以下文件：

```text
docs/人脸身份证比对/test_rlbd_latency.py
```

在服务器上设置对应路由的 appKey/appSecret 后执行即可。
