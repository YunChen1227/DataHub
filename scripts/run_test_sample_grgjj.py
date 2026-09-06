#!/usr/bin/env python3
"""Query GRGJJ for 测试样本(1).xlsx and export 测试样本_公积金.xlsx."""

from __future__ import annotations

import hashlib
import io
import json
import msoffcrypto
import os
import sys
import time
from pathlib import Path

import requests
from openpyxl import Workbook, load_workbook

sys.stdout.reconfigure(encoding="utf-8")

BASE_URL = os.environ.get("RELAY_BASE_URL", "http://aiszcloud.cn:8080")
APP_KEY = os.environ.get("GRGJJ_APP_KEY", "u4kwtvc588h5")
APP_SECRET = os.environ.get("GRGJJ_APP_SECRET", "e1f1dd22d79d948e3e9dfc9a1d1818cf")
# grgjj 网关要求 mobile 必填且格式合法；源表无手机号列时用占位号过校验（上游按三要素查）。
PLACEHOLDER_MOBILE = os.environ.get("GRGJJ_PLACEHOLDER_MOBILE", "13800138000")

SRC = Path(
    os.environ.get(
        "GRGJJ_SRC_XLSX",
        r"c:\Users\XMMXYY\Documents\xwechat_files\wxid_t7vgbilml8m511_c2b2\msg\file\2026-09\测试样本(1).xlsx",
    )
)
SRC_PASSWORD = os.environ.get("GRGJJ_SRC_PASSWORD", "")

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "test_res" / "测试样本_公积金.xlsx"
JSON_OUT = ROOT / "test_res" / "测试样本_grgjj_batch.json"

HEADERS = ("姓名", "身份证号", "缴费时间", "缴费状态", "缴费基数")
CBJFZT = {"1": "正常参保", "2": "暂停参保"}


def sign(body: dict[str, str], secret: str) -> str:
    items = sorted((k, v) for k, v in body.items() if v)
    return hashlib.md5(("".join(k + v for k, v in items) + secret).encode()).hexdigest()


def open_workbook(path: Path, password: str):
    if password:
        with path.open("rb") as f:
            ms = msoffcrypto.OfficeFile(f)
            ms.load_key(password=password)
            buf = io.BytesIO()
            ms.decrypt(buf)
            buf.seek(0)
            return load_workbook(buf, read_only=True, data_only=True)
    return load_workbook(path, read_only=True, data_only=True)


def load_rows() -> list[dict]:
    wb = open_workbook(SRC, SRC_PASSWORD)
    sh = wb.active
    rows: list[dict] = []
    for i, row in enumerate(sh.iter_rows(values_only=True), 1):
        if i == 1:
            h0 = str(row[0] or "").strip().lower()
            h1 = str(row[1] or "").strip().lower() if len(row) > 1 else ""
            if h0 in ("name", "姓名") and h1 in ("idcard", "id_card", "身份证号", "证件号"):
                continue
        if not row or len(row) < 2:
            continue
        name = str(row[0]).strip() if row[0] is not None else ""
        id_card = str(row[1]).strip().upper() if row[1] is not None else ""
        mobile = ""
        if len(row) > 2 and row[2] is not None:
            mobile = str(row[2]).strip()
        if not name or not id_card:
            continue
        if not mobile:
            mobile = PLACEHOLDER_MOBILE
        rows.append({"row": i, "name": name, "id_card": id_card, "mobile": mobile})
    return rows


def query_one(rec: dict, session: requests.Session) -> dict:
    body = {"name": rec["name"], "idCard": rec["id_card"], "mobile": rec["mobile"]}
    payload = {
        "encryptionType": 1,
        "appKey": APP_KEY,
        "sign": sign(body, APP_SECRET),
        "body": body,
    }
    t0 = time.time()
    try:
        resp = session.post(
            f"{BASE_URL}/v1/openapi/zlx/querySrmxGRGJJ",
            json=payload,
            timeout=30,
        )
        raw = resp.text
        data = resp.json()
        head = data.get("head") or {}
        body_obj = data.get("body") or {}
        ec = str(head.get("errorCode", ""))
        bc = str(body_obj.get("code", ""))
        lat = float(head.get("time") or (time.time() - t0) * 1000)
        if ec == "0" and bc == "001":
            outcome = "found"
        elif ec == "0" and bc == "999":
            outcome = "not_found"
        elif ec == "0":
            outcome = "other_ok"
        else:
            outcome = "error"
        return {
            **rec,
            "http_status": resp.status_code,
            "error_code": ec,
            "body_code": bc,
            "latency_ms": lat,
            "outcome": outcome,
            "raw": raw,
        }
    except Exception as exc:  # noqa: BLE001
        return {
            **rec,
            "http_status": 0,
            "error_code": "NETWORK",
            "body_code": "",
            "latency_ms": None,
            "outcome": "error",
            "raw": str(exc),
        }


def parse_range(raw: str) -> dict | None:
    try:
        body = json.loads(raw).get("body") or {}
        rs = (body.get("result") or {}).get("range") or ""
        if not str(rs).strip():
            return None
        obj = json.loads(rs)
        return obj[0] if isinstance(obj, list) else obj
    except Exception:
        return None


def map_cbjfzt(value: object) -> str | None:
    if value is None or value == "":
        return None
    text = str(value).strip()
    return CBJFZT.get(text, text)


def result_row(rec: dict, raw: str | None) -> tuple:
    if not raw:
        return (rec["name"], rec["id_card"], None, None, None)
    data = parse_range(raw)
    if not data:
        return (rec["name"], rec["id_card"], None, None, None)
    return (
        rec["name"],
        rec["id_card"],
        data.get("jfsj") or None,
        map_cbjfzt(data.get("cbjfzt")),
        data.get("jfjs") or None,
    )


def main() -> int:
    health = requests.get(f"{BASE_URL}/healthz", timeout=10)
    print(f"healthz: {health.status_code} {health.text.strip()}")
    if health.status_code != 200:
        return 1

    rows = load_rows()
    print(f"loaded {len(rows)} records from {SRC.name}")
    print(f"target: {BASE_URL} appKey={APP_KEY}")
    print(f"mobile: 源表无手机号列，使用占位号 {PLACEHOLDER_MOBILE}")

    results: list[dict] = []
    with requests.Session() as session:
        for i, rec in enumerate(rows, 1):
            r = query_one(rec, session)
            results.append(r)
            print(
                f"[{i}/{len(rows)}] {rec['name']} {rec['id_card']} -> "
                f"{r['outcome']} ec={r['error_code']} bc={r['body_code']}"
            )
            time.sleep(0.2)

    results.sort(key=lambda x: x["row"])
    total = len(results)
    found = sum(1 for r in results if r["outcome"] == "found")
    not_found = sum(1 for r in results if r["outcome"] == "not_found")
    errors = total - found - not_found
    summary = {
        "total": total,
        "found": found,
        "not_found": not_found,
        "error": errors,
        "found_rate": round(found / total * 100, 2) if total else 0,
        "not_found_rate": round(not_found / total * 100, 2) if total else 0,
        "error_rate": round(errors / total * 100, 2) if total else 0,
        "placeholder_mobile": PLACEHOLDER_MOBILE,
    }

    JSON_OUT.parent.mkdir(parents=True, exist_ok=True)
    JSON_OUT.write_text(
        json.dumps({"summary": summary, "results": results}, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )

    wb = Workbook()
    ws = wb.active
    ws.title = "Sheet1"
    ws.append(HEADERS)
    with_data = 0
    for rec, r in zip(rows, results):
        out = result_row(rec, r["raw"])
        ws.append(out)
        if out[2] is not None:
            with_data += 1
    wb.save(OUT)

    print("--- summary ---")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    print(f"with_data: {with_data}/{len(rows)}")
    print(f"saved json: {JSON_OUT}")
    print(f"saved xlsx: {OUT}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
