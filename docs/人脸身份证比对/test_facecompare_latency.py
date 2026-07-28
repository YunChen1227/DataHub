#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
人脸身份证比对一所（数脉 facecompare 上游）— 延迟测试脚本

直接 POST https://api.shumaidata.com/v4/face_id_card/yisuo/compare
sign = md5(appid&timestamp&app_security)，form 提交。

用法:
  FACECOMPARE_APP_ID=xxx FACECOMPARE_APP_SECRET=xxx \\
  python3 test_facecompare_latency.py --name 张三 --id-card 330129199109094312 --photo face.jpg

依赖: 无（仅 Python 3 标准库）
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
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

DEFAULT_BASE_URL = "https://api.shumaidata.com/v4/face_id_card/yisuo/compare"


def sign_facecompare(app_id: str, timestamp: str, app_secret: str) -> str:
    raw = f"{app_id}&{timestamp}&{app_secret}"
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


def call_once(
    *,
    app_id: str,
    app_secret: str,
    name: str,
    id_card: str,
    image_b64: str,
    base_url: str,
    timeout: float,
) -> dict[str, Any]:
    timestamp = str(int(time.time() * 1000))
    form = {
        "appid": app_id,
        "timestamp": timestamp,
        "sign": sign_facecompare(app_id, timestamp, app_secret),
        "name": name,
        "idcard": id_card,
        "image": image_b64,
    }
    body = urllib.parse.urlencode(form).encode("utf-8")
    req = urllib.request.Request(
        base_url,
        data=body,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
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

    data = payload.get("data") or {}
    return {
        "client_ms": client_ms,
        "http_status": status,
        "code": payload.get("code"),
        "incorrect": data.get("incorrect"),
        "msg": payload.get("msg"),
        "response": payload,
    }


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="facecompare 上游延迟测试")
    p.add_argument("--app-id", default=os.getenv("FACECOMPARE_APP_ID"))
    p.add_argument("--app-secret", default=os.getenv("FACECOMPARE_APP_SECRET"))
    p.add_argument("--base-url", default=os.getenv("FACECOMPARE_BASE_URL", DEFAULT_BASE_URL))
    p.add_argument("--name", required=True)
    p.add_argument("--id-card", required=True, dest="id_card")
    p.add_argument("--photo", required=True, help="本地照片路径（转 base64 作为 image）")
    p.add_argument("--repeat", type=int, default=1)
    p.add_argument("--interval", type=float, default=0.0)
    p.add_argument("--timeout", type=float, default=30.0)
    p.add_argument("--verbose", action="store_true")
    return p.parse_args()


def main() -> int:
    args = parse_args()
    if not args.app_id or not args.app_secret:
        print("错误: 请设置 FACECOMPARE_APP_ID / FACECOMPARE_APP_SECRET 或 --app-id / --app-secret", file=sys.stderr)
        return 2
    if args.repeat < 1:
        print("错误: --repeat 必须 >= 1", file=sys.stderr)
        return 2
    try:
        image_b64 = load_image_base64(args.photo)
    except ValueError as e:
        print(f"错误: {e}", file=sys.stderr)
        return 2

    print("=== facecompare 上游 延迟测试 ===")
    print(f"endpoint: {args.base_url}")
    print(f"repeat: {args.repeat}  interval: {args.interval}s  image_b64_len: {len(image_b64)}")
    print()

    latencies: list[float] = []
    last: dict[str, Any] | None = None
    for i in range(args.repeat):
        try:
            last = call_once(
                app_id=args.app_id,
                app_secret=args.app_secret,
                name=args.name,
                id_card=args.id_card,
                image_b64=image_b64,
                base_url=args.base_url,
                timeout=args.timeout,
            )
        except (ConnectionError, TimeoutError, urllib.error.URLError) as e:
            print(f"[#{i + 1}/{args.repeat}] REQUEST ERROR: {e}")
            if i + 1 < args.repeat and args.interval > 0:
                time.sleep(args.interval)
            continue
        latencies.append(last["client_ms"])
        print(
            f"[#{i + 1}/{args.repeat}] code={last.get('code')} incorrect={last.get('incorrect')} "
            f"client_ms={last['client_ms']:.1f} msg={last.get('msg')}"
        )
        if args.verbose:
            print(json.dumps(last["response"], ensure_ascii=False, indent=2))
        if i + 1 < args.repeat and args.interval > 0:
            time.sleep(args.interval)

    print()
    if latencies:
        print("--- 耗时汇总 (client_ms, 单位 ms) ---")
        print(
            f"count={len(latencies)}  min={min(latencies):.1f}  max={max(latencies):.1f}  "
            f"avg={statistics.mean(latencies):.1f}  p50={percentile(latencies, 50):.1f}  "
            f"p95={percentile(latencies, 95):.1f}"
        )
    else:
        return 1
    if last and last.get("code") == 200:
        print("\n提示: code=200 且 incorrect 为收费码时会计费。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
