#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
人脸身份证比对查询服务（rlbd1 / rlbd2）— 延迟测试脚本

调用 DataHub 网关接口 querySrmxRLBD1 / querySrmxRLBD2，统计单次/多次调用耗时。

用法:
  # rlbd1
  export RLBD1_APP_KEY="your_app_key"
  export RLBD1_APP_SECRET="your_app_secret"
  python test_rlbd_latency.py --route rlbd1 --name 张三 --id-card 330129199109094312 --url http://img.example.com/a.jpg

  # rlbd2
  export RLBD2_APP_KEY="your_app_key"
  export RLBD2_APP_SECRET="your_app_secret"
  python test_rlbd_latency.py --route rlbd2 --name 张三 --id-card 330129199109094312 --photo face.jpg --repeat 10

依赖: 无（仅 Python 3 标准库，无需 pip install）
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

DEFAULT_BASE_URL = "http://aiszcloud.cn:8080"

ROUTES = {
    "rlbd1": "/v1/openapi/zlx/querySrmxRLBD1",
    "rlbd2": "/v1/openapi/zlx/querySrmxRLBD2",
}


def sign_body(body: dict[str, str], app_secret: str) -> str:
    """body 业务参数 ASCII 升序 key+value 拼接 + appSecret，MD5 小写 hex。"""
    parts = [f"{k}{v}" for k, v in sorted(body.items()) if v]
    raw = "".join(parts) + app_secret
    return hashlib.md5(raw.encode("utf-8")).hexdigest()


def load_image_base64(photo: str) -> str:
    p = Path(photo)
    if not p.is_file():
        raise ValueError(f"照片文件不存在: {photo}")
    raw = p.read_bytes()
    if len(raw) > 500 * 1024:
        raise ValueError(f"照片原始大小 {len(raw)} 字节，超过 500KB 限制")
    return base64.b64encode(raw).decode("ascii")


def percentile(values: list[float], p: float) -> float:
    if not values:
        return 0.0
    if len(values) == 1:
        return values[0]
    xs = sorted(values)
    k = (len(xs) - 1) * p / 100.0
    f = int(k)
    c = min(f + 1, len(xs) - 1)
    if f == c:
        return xs[f]
    return xs[f] + (xs[c] - xs[f]) * (k - f)


def resolve_credentials(route: str, app_key: str | None, app_secret: str | None) -> tuple[str, str]:
    route_upper = route.upper()
    key = (
        app_key
        or os.getenv(f"{route_upper}_APP_KEY")
        or os.getenv(f"RLBD_{route_upper}_APP_KEY")
        or os.getenv("RLBD_APP_KEY")
    )
    secret = (
        app_secret
        or os.getenv(f"{route_upper}_APP_SECRET")
        or os.getenv(f"RLBD_{route_upper}_APP_SECRET")
        or os.getenv("RLBD_APP_SECRET")
    )
    if not key or not secret:
        raise ValueError(
            f"请设置 --app-key / --app-secret，或环境变量 "
            f"{route_upper}_APP_KEY / {route_upper}_APP_SECRET"
        )
    return key, secret


def build_body(name: str, id_card: str, image: str, url: str) -> dict[str, str]:
    body: dict[str, str] = {"name": name, "idCard": id_card}
    if image:
        body["image"] = image
    elif url:
        body["url"] = url
    else:
        raise ValueError("请提供 --photo（本地照片 Base64）或 --url（照片 URL），二选一")
    return body


def call_once(
    *,
    base_url: str,
    route: str,
    app_key: str,
    app_secret: str,
    body: dict[str, str],
    timeout: float,
) -> dict[str, Any]:
    path = ROUTES[route]
    payload = {
        "encryptionType": 1,
        "appKey": app_key,
        "sign": sign_body(body, app_secret),
        "body": body,
    }

    url = base_url.rstrip("/") + path
    raw_body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=raw_body,
        headers={"Content-Type": "application/json; charset=utf-8"},
        method="POST",
    )
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status = resp.status
            text = resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        status = e.code
        text = e.read().decode("utf-8", errors="replace")
    except urllib.error.URLError as e:
        raise ConnectionError(str(e.reason)) from e
    client_ms = (time.perf_counter() - t0) * 1000.0

    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        data = {"_raw": text[:500]}

    head = data.get("head") or {}
    body_resp = data.get("body") or {}
    server_ms = head.get("time")

    return {
        "client_ms": client_ms,
        "server_ms": server_ms,
        "http_status": status,
        "error_code": head.get("errorCode"),
        "error_msg": head.get("errorMsg"),
        "log_id": head.get("logId"),
        "body_code": body_resp.get("code"),
        "body_msg": body_resp.get("msg"),
        "response": data,
    }


def fmt_result(r: dict[str, Any]) -> str:
    server_part = f" server_ms={r['server_ms']}" if r.get("server_ms") is not None else ""
    return (
        f"errorCode={r.get('error_code')} body.code={r.get('body_code')} "
        f"client_ms={r['client_ms']:.1f}{server_part} logId={r.get('log_id')}"
    )


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="rlbd1/rlbd2 人脸身份证比对延迟测试")
    p.add_argument("--route", required=True, choices=sorted(ROUTES.keys()), help="路由：rlbd1 或 rlbd2")
    p.add_argument("--base-url", default=os.getenv("RLBD_BASE_URL", DEFAULT_BASE_URL), help="服务地址")
    p.add_argument("--app-key", default=None, help="appKey，或环境变量 RLBD1_APP_KEY / RLBD2_APP_KEY")
    p.add_argument("--app-secret", default=None, help="appSecret，或环境变量 RLBD1_APP_SECRET / RLBD2_APP_SECRET")
    p.add_argument("--name", required=True, help="姓名")
    p.add_argument("--id-card", required=True, dest="id_card", help="身份证号")
    p.add_argument("--photo", default="", help="本地照片文件路径（转 Base64 作为 image）")
    p.add_argument("--url", default="", help="人像照片 URL（与 --photo 二选一）")
    p.add_argument("--repeat", type=int, default=1, help="调用次数，默认 1")
    p.add_argument("--interval", type=float, default=0.0, help="两次调用间隔秒数，默认 0")
    p.add_argument("--timeout", type=float, default=15.0, help="HTTP 超时秒数，默认 15（建议 ≥10）")
    p.add_argument("--verbose", action="store_true", help="打印完整 JSON 响应")
    return p.parse_args()


def main() -> int:
    args = parse_args()
    if args.repeat < 1:
        print("错误: --repeat 必须 >= 1", file=sys.stderr)
        return 2

    try:
        app_key, app_secret = resolve_credentials(args.route, args.app_key, args.app_secret)
        image_b64 = load_image_base64(args.photo) if args.photo else ""
        body = build_body(args.name, args.id_card, image_b64, args.url)
    except ValueError as e:
        print(f"错误: {e}", file=sys.stderr)
        return 2

    print(f"=== 人脸身份证比对 ({args.route}) 延迟测试 ===")
    print(f"endpoint: {args.base_url.rstrip('/')}{ROUTES[args.route]}")
    print(f"repeat: {args.repeat}  interval: {args.interval}s  timeout: {args.timeout}s")
    if image_b64:
        print(f"photo_mode: image(base64_len={len(image_b64)})")
    else:
        print(f"photo_mode: url({args.url})")
    print()

    client_latencies: list[float] = []
    server_latencies: list[float] = []
    last: dict[str, Any] | None = None

    for i in range(args.repeat):
        try:
            last = call_once(
                base_url=args.base_url,
                route=args.route,
                app_key=app_key,
                app_secret=app_secret,
                body=body,
                timeout=args.timeout,
            )
        except (ConnectionError, TimeoutError, urllib.error.URLError) as e:
            print(f"[#{i + 1}/{args.repeat}] REQUEST ERROR: {e}")
            if i + 1 < args.repeat and args.interval > 0:
                time.sleep(args.interval)
            continue

        client_latencies.append(last["client_ms"])
        if isinstance(last.get("server_ms"), (int, float)):
            server_latencies.append(float(last["server_ms"]))

        print(f"[#{i + 1}/{args.repeat}] {fmt_result(last)}")
        if args.verbose:
            print(json.dumps(last["response"], ensure_ascii=False, indent=2))

        if i + 1 < args.repeat and args.interval > 0:
            time.sleep(args.interval)

    print()
    if client_latencies:
        print("--- 耗时汇总 client_ms (客户端全链路, 单位 ms) ---")
        print(
            f"count={len(client_latencies)}  "
            f"min={min(client_latencies):.1f}  max={max(client_latencies):.1f}  "
            f"avg={statistics.mean(client_latencies):.1f}  "
            f"p50={percentile(client_latencies, 50):.1f}  p95={percentile(client_latencies, 95):.1f}"
        )
    if server_latencies:
        print("--- 耗时汇总 server_ms (响应 head.time, 单位 ms) ---")
        print(
            f"count={len(server_latencies)}  "
            f"min={min(server_latencies):.1f}  max={max(server_latencies):.1f}  "
            f"avg={statistics.mean(server_latencies):.1f}  "
            f"p50={percentile(server_latencies, 50):.1f}  p95={percentile(server_latencies, 95):.1f}"
        )
    else:
        print("未从响应中解析到 head.time。")

    if not client_latencies:
        return 1

    if last and last.get("body_code") == "001":
        print("\n提示: body.code=001 为查得比对结论，会计费。")
    elif last and last.get("error_code") not in (None, "0"):
        print(f"\n提示: 最后一次 head.errorCode={last.get('error_code')}，请检查 appKey/签名/参数。")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
