#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
本地一次性测试 rlbd1 + rlbd2 全链路耗时（经 DataHub 网关 → 阿里云上游）。

用法:
  export RLBD1_APP_KEY=... RLBD1_APP_SECRET=...
  export RLBD2_APP_KEY=... RLBD2_APP_SECRET=...
  python test_rlbd_both_local.py \\
    --name 陈韫 --id-card 440303200002163115 \\
    --photo ../../test/rl.jpg --repeat 3 --interval 0.5

依赖: 同目录 test_rlbd_latency.py（仅 Python 3 标准库）
"""

from __future__ import annotations

import argparse
import importlib.util
import os
import statistics
import sys
import time
import urllib.error
from pathlib import Path
from typing import Any

DEFAULT_BASE_URL = "http://aiszcloud.cn:8080"
ROUTES = ("rlbd1", "rlbd2")

_HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("test_rlbd_latency", _HERE / "test_rlbd_latency.py")
if _spec is None or _spec.loader is None:
    raise ImportError("无法加载 test_rlbd_latency.py")
_lat = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_lat)


def percentile(values: list[float], p: float) -> float:
    return _lat.percentile(values, p)


def resolve_default_photo(explicit: str) -> str:
    if explicit:
        return explicit
    candidates = [
        Path(__file__).resolve().parents[2] / "test" / "rl.jpg",
        Path.cwd() / "test" / "rl.jpg",
        Path.cwd() / "DataHub" / "test" / "rl.jpg",
    ]
    for p in candidates:
        if p.is_file():
            return str(p)
    return ""


def run_one_route(
    *,
    route: str,
    base_url: str,
    name: str,
    id_card: str,
    photo: str,
    url: str,
    repeat: int,
    interval: float,
    timeout: float,
    verbose: bool,
    app_key: str | None,
    app_secret: str | None,
) -> dict[str, Any]:
    app_key, app_secret = _lat.resolve_credentials(route, app_key, app_secret)
    image_b64 = _lat.load_image_base64(photo) if photo else ""
    body = _lat.build_body(name, id_card, image_b64, url)

    print(f"\n{'=' * 60}")
    print(f"=== {route.upper()} 延迟测试 ===")
    print(f"endpoint: {base_url.rstrip('/')}{_lat.ROUTES[route]}")
    print(f"appKey: {app_key}")
    print(f"repeat: {repeat}  interval: {interval}s  timeout: {timeout}s")
    if image_b64:
        print(f"photo: {photo}  base64_len={len(image_b64)}")
    else:
        print(f"photo_mode: url({url})")
    print()

    client_latencies: list[float] = []
    server_latencies: list[float] = []
    last: dict[str, Any] | None = None

    for i in range(repeat):
        try:
            last = _lat.call_once(
                base_url=base_url,
                route=route,
                app_key=app_key,
                app_secret=app_secret,
                body=body,
                timeout=timeout,
            )
        except (ConnectionError, TimeoutError, urllib.error.URLError) as e:
            print(f"[#{i + 1}/{repeat}] REQUEST ERROR: {e}")
            if i + 1 < repeat and interval > 0:
                time.sleep(interval)
            continue

        client_latencies.append(last["client_ms"])
        if isinstance(last.get("server_ms"), (int, float)):
            server_latencies.append(float(last["server_ms"]))
        print(f"[#{i + 1}/{repeat}] {_lat.fmt_result(last)}")
        if verbose:
            import json

            print(json.dumps(last["response"], ensure_ascii=False, indent=2))

        if i + 1 < repeat and interval > 0:
            time.sleep(interval)

    return {
        "route": route,
        "client_latencies": client_latencies,
        "server_latencies": server_latencies,
        "last": last,
    }


def print_summary(label: str, values: list[float]) -> None:
    if not values:
        print(f"  {label}: (无数据)")
        return
    print(
        f"  {label}: count={len(values)}  min={min(values):.1f}  max={max(values):.1f}  "
        f"avg={statistics.mean(values):.1f}  p50={percentile(values, 50):.1f}  "
        f"p95={percentile(values, 95):.1f} ms"
    )


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="本地测试 rlbd1 + rlbd2 全链路耗时")
    p.add_argument("--base-url", default=os.getenv("RLBD_BASE_URL", DEFAULT_BASE_URL))
    p.add_argument("--name", required=True)
    p.add_argument("--id-card", required=True, dest="id_card")
    p.add_argument("--photo", default="", help="本地照片；默认尝试 DataHub/test/rl.jpg")
    p.add_argument("--url", default="", help="照片 URL（与 --photo 二选一）")
    p.add_argument("--repeat", type=int, default=3, help="每个路由调用次数，默认 3")
    p.add_argument("--interval", type=float, default=0.5, help="同路由两次调用间隔秒数")
    p.add_argument("--timeout", type=float, default=20.0, help="HTTP 超时秒数")
    p.add_argument("--verbose", action="store_true")
    p.add_argument("--rlbd1-app-key", default=None)
    p.add_argument("--rlbd1-app-secret", default=None)
    p.add_argument("--rlbd2-app-key", default=None)
    p.add_argument("--rlbd2-app-secret", default=None)
    return p.parse_args()


def main() -> int:
    args = parse_args()
    if args.repeat < 1:
        print("错误: --repeat 必须 >= 1", file=sys.stderr)
        return 2

    photo = resolve_default_photo(args.photo)
    if not photo and not args.url:
        print(
            "错误: 请提供 --photo 或 --url；未找到默认照片 DataHub/test/rl.jpg",
            file=sys.stderr,
        )
        return 2

    creds = {
        "rlbd1": (args.rlbd1_app_key, args.rlbd1_app_secret),
        "rlbd2": (args.rlbd2_app_key, args.rlbd2_app_secret),
    }

    print("=== 本地 → 阿里云 RLBD 全链路耗时测试 ===")
    print(f"base_url: {args.base_url}")
    print(f"name: {args.name}  idCard: {args.id_card}")
    print(f"routes: {', '.join(ROUTES)}  repeat_each: {args.repeat}")

    results: list[dict[str, Any]] = []
    for route in ROUTES:
        try:
            r = run_one_route(
                route=route,
                base_url=args.base_url,
                name=args.name,
                id_card=args.id_card,
                photo=photo,
                url=args.url,
                repeat=args.repeat,
                interval=args.interval,
                timeout=args.timeout,
                verbose=args.verbose,
                app_key=creds[route][0],
                app_secret=creds[route][1],
            )
        except ValueError as e:
            print(f"\n{route} 配置错误: {e}", file=sys.stderr)
            return 2
        results.append(r)

    print(f"\n{'=' * 60}")
    print("=== 汇总对比 ===")
    for r in results:
        route = r["route"]
        last = r.get("last") or {}
        print(f"\n[{route}]")
        if last:
            print(
                f"  最后一次: errorCode={last.get('error_code')} body.code={last.get('body_code')} "
                f"logId={last.get('log_id')}"
            )
        print_summary("client_ms (本地全链路)", r["client_latencies"])
        print_summary("server_ms (head.time)", r["server_latencies"])

    ok = all(r["client_latencies"] for r in results)
    billed = any((r.get("last") or {}).get("body_code") == "001" for r in results)
    if billed:
        print("\n提示: body.code=001 为查得比对结论，会计费。")
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
