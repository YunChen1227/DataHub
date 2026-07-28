#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
身份证三要素核验接口 — 延迟测试脚本

直接调用上游 POST /api/idCardThreeElements，统计单次/多次调用耗时。

用法:
  export IDVERIFY_APP_ID="your_app_id"
  export IDVERIFY_APP_SECRET="your_app_secret"

  # 单次
  python test_idverify_latency.py --name 张三 --id-card 420101198012010011 --photo face.jpg

  # 多次统计 min/max/avg/p50/p95
  python test_idverify_latency.py --name 张三 --id-card 420101198012010011 --photo face.jpg --repeat 10 --interval 0.5

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
import uuid
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

DEFAULT_BASE_URL = "https://api.cqcucc.com:8443/api/idCardThreeElements"

# 1x1 PNG，仅用于 --dry-run 验证签名与连通性（大概率返回 462/461，不计完整业务成功）
TINY_PNG_B64 = (
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
)


def sign_idverify(params: dict[str, str], app_secret: str) -> str:
    """SHA256(升序 k=v&... + &AppSecret=密钥) 小写 hex。"""
    items = sorted(params.items(), key=lambda x: x[0])
    raw = "&".join(f"{k}={v}" for k, v in items) + f"&AppSecret={app_secret}"
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def load_profile_picture(photo: str) -> str:
    """支持文件路径或已是 Base64 的字符串。"""
    p = Path(photo)
    if p.is_file():
        raw = p.read_bytes()
        if len(raw) > 50 * 1024:
            raise ValueError(f"照片原始大小 {len(raw)} 字节，超过 50K 限制")
        b64 = base64.b64encode(raw).decode("ascii")
        if len(b64) > 50 * 1024:
            raise ValueError(f"Base64 编码后 {len(b64)} 字节，超过 50K 限制")
        return b64
    # 视为已是 base64；去掉常见 data URI 前缀
    s = photo.strip()
    if s.startswith("data:"):
        _, _, s = s.partition(",")
    return s


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


def call_once(
    *,
    app_id: str,
    app_secret: str,
    name: str,
    id_card: str,
    profile_picture_b64: str,
    base_url: str,
    out_biz_no: str,
    timeout: float,
) -> dict[str, Any]:
    ts = int(time.time() * 1000)
    sign_params = {
        "appId": app_id,
        "outBizNo": out_biz_no,
        "name": name,
        "idCard": id_card,
        "profilePicture": profile_picture_b64,
        "timestamp": str(ts),
    }
    body = {
        "appId": app_id,
        "outBizNo": out_biz_no,
        "name": name,
        "idCard": id_card,
        "profilePicture": profile_picture_b64,
        "timestamp": ts,
        "signature": sign_idverify(sign_params, app_secret),
    }

    raw_body = json.dumps(body, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(
        base_url,
        data=raw_body,
        headers={"Content-Type": "application/json;charset=UTF-8"},
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
        payload = json.loads(text)
    except json.JSONDecodeError:
        payload = {"_raw": text[:500]}

    return {
        "client_ms": client_ms,
        "http_status": status,
        "response": payload,
        "out_biz_no": out_biz_no,
    }


def fmt_result(resp: dict[str, Any]) -> str:
    r = resp.get("response") or {}
    code = r.get("Code", "?")
    charge = r.get("IsCharge", "?")
    msg = r.get("Message", "")
    data = r.get("Data") or {}
    result = data.get("Result", "")
    extra = f" Result={result}" if result != "" else ""
    return (
        f"Code={code} IsCharge={charge} client_ms={resp['client_ms']:.1f}{extra} Message={msg}"
    )


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="身份证三要素核验接口延迟测试")
    p.add_argument("--app-id", default=os.getenv("IDVERIFY_APP_ID"), help="上游 appId，或环境变量 IDVERIFY_APP_ID")
    p.add_argument("--app-secret", default=os.getenv("IDVERIFY_APP_SECRET"), help="上游 AppSecret，或环境变量 IDVERIFY_APP_SECRET")
    p.add_argument("--base-url", default=os.getenv("IDVERIFY_BASE_URL", DEFAULT_BASE_URL))
    p.add_argument("--name", required=True, help="姓名")
    p.add_argument("--id-card", required=True, dest="id_card", help="身份证号")
    p.add_argument("--photo", default="", help="人像照片文件路径，或 Base64 字符串；省略时用内置 1x1 探针图")
    p.add_argument("--repeat", type=int, default=1, help="调用次数，默认 1")
    p.add_argument("--interval", type=float, default=0.0, help="两次调用间隔秒数，默认 0")
    p.add_argument("--timeout", type=float, default=30.0, help="HTTP 超时秒数，默认 30")
    p.add_argument("--out-biz-no-prefix", default="latency-test", help="outBizNo 前缀")
    p.add_argument("--verbose", action="store_true", help="打印完整 JSON 响应")
    return p.parse_args()


def main() -> int:
    args = parse_args()
    if not args.app_id or not args.app_secret:
        print("错误: 请设置 --app-id / --app-secret 或环境变量 IDVERIFY_APP_ID / IDVERIFY_APP_SECRET", file=sys.stderr)
        return 2

    if args.repeat < 1:
        print("错误: --repeat 必须 >= 1", file=sys.stderr)
        return 2

    try:
        photo_b64 = load_profile_picture(args.photo) if args.photo else TINY_PNG_B64
    except ValueError as e:
        print(f"错误: {e}", file=sys.stderr)
        return 2

    print("=== 身份证三要素核验 延迟测试 ===")
    print(f"endpoint: {args.base_url}")
    print(f"repeat: {args.repeat}  interval: {args.interval}s  photo_b64_len: {len(photo_b64)}")
    print()

    latencies: list[float] = []
    last: dict[str, Any] | None = None

    for i in range(args.repeat):
        out_biz_no = f"{args.out_biz_no_prefix}-{uuid.uuid4().hex[:16]}"
        try:
            last = call_once(
                app_id=args.app_id,
                app_secret=args.app_secret,
                name=args.name,
                id_card=args.id_card,
                profile_picture_b64=photo_b64,
                base_url=args.base_url,
                out_biz_no=out_biz_no,
                timeout=args.timeout,
            )
        except (ConnectionError, TimeoutError, urllib.error.URLError) as e:
            print(f"[#{i + 1}/{args.repeat}] REQUEST ERROR: {e}")
            if i + 1 < args.repeat and args.interval > 0:
                time.sleep(args.interval)
            continue

        latencies.append(last["client_ms"])
        print(f"[#{i + 1}/{args.repeat}] {fmt_result(last)}")
        if args.verbose:
            print(json.dumps(last["response"], ensure_ascii=False, indent=2))

        if i + 1 < args.repeat and args.interval > 0:
            time.sleep(args.interval)

    print()
    if latencies:
        print("--- 耗时汇总 (client_ms, 单位 ms) ---")
        print(
            f"count={len(latencies)}  "
            f"min={min(latencies):.1f}  max={max(latencies):.1f}  "
            f"avg={statistics.mean(latencies):.1f}  "
            f"p50={percentile(latencies, 50):.1f}  p95={percentile(latencies, 95):.1f}"
        )
    else:
        print("未获得任何成功发起的请求耗时。")
        return 1

    if last and isinstance(last.get("response"), dict):
        code = last["response"].get("Code")
        if code != 0:
            print(f"\n提示: 最后一次响应 Code={code}，若为 462/461 等属照片/业务原因，延迟数据仍可用于网络评估。")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
