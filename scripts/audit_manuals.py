"""核对 docs/ 下每份对外《API 接口文档与使用手册》PDF 与代码/skill 铁律是否对齐。

检查项（对应 .claude/skills/api-doc/SKILL.md 的终检清单 + billing-scope skill 的计费口径）：
  - quota 响应是否写全 serviceUsed + totalCalls（internal/api/handler.go quotaResponse）
  - 上游隐匿：不得出现「上游 / 数据提供商 / 转发 / 转接」及上游侧状态码词
  - 内部实现禁令：不得出现缓存/自然月/同月复用等措辞
  - 版本隔离：不得出现其它路由名
  - 计费口径：999 是否计费必须与 billing.TableFor(route) 一致

用法：python scripts/audit_manuals.py
"""

import glob
import os
import re
import sys
import unicodedata

from pypdf import PdfReader

sys.stdout.reconfigure(encoding="utf-8")

ROUTES = [
    "x1", "v9", "v8", "zlf", "blk", "rlbd1", "rlbd2", "sfzhy",
    "xfjy", "tsfx", "lxf", "grgjj", "grsb", "sfsm",
]

# 与 internal/domain/billing/billing.go 的 billNotFoundRoutes 保持一致。
BILL_NOT_FOUND_ROUTES = {"blk"}

# 这些路由的数据源只有「有结论 / 无结论」两态，永远不产生 999，手册里不出现 999 属正常：
#   rlbd1/rlbd2 (facecompare) 与 tsfx (complaint) 的客户端只返回 001 或 *UpstreamError。
# 注意 sfzhy 不在此列——自「按上游 IsCharge 计费」改造起它会产生 999（见 idverify.go）。
NO_NOTFOUND_ROUTES = {"rlbd1", "rlbd2", "tsfx"}

# 注意 incorrect / whether_hit / Result 这类**确实会出现在 result.range 里的业务字段名**
# 不算泄露来源——它们是下游契约的一部分，手册必须写。只查真正暴露"本服务是转接方"
# 的措辞与来源方的内部状态码词。
UPSTREAM_WORDS = ["上游", "数据提供商", "转发", "转接", "busiCode", "resp_code", "internalErrorCode"]
CACHE_WORDS = ["缓存", "自然月", "同月复用", "结果时效性", "首查结果", "命中缓存"]

# v9v8 一份文档覆盖两条路由；swfp 属另一仓库的路由。
OWN_ROUTES = {"v9v8": {"v9", "v8"}, "swfp": {"swfp"}}


def own_of(name: str) -> set[str]:
    return OWN_ROUTES.get(name, {name})


def bill_expectation(name: str) -> str:
    """手册里 999 应该怎么写。"""
    if name in NO_NOTFOUND_ROUTES:
        return "无 999"
    if name in BILL_NOT_FOUND_ROUTES:
        return "999 计费"
    return "999 不计费"


# 计费口径只在 §4.2 业务结果码表里判定：把正文截到「4.2 业务结果码」与「五、计费说明」
# 之间再找 999 行。不扫全文是因为签名示例 hex（0528999dd55c…）里也有 999，全文扫会误判。
SECTION_42 = re.compile(r"4\.2业务结果码.*?(?=五、计费说明)", re.S)
# 该表的 999 行形如「999 查无结果 不计费」/「999 未产出核验结论（无 result 节点） 不计费」，
# 含义段限长且不含数字以免跨行吃进下一条码。「不计费」必须排在前面先匹配。
BILL_ROW = re.compile(r"999[（(]?[^0-9]{2,24}?[）)]?(不计费|计费)")


def bill_actual(flat: str, name: str) -> str:
    sec = SECTION_42.search(flat)
    if not sec:
        return "缺 §4.2 码表"
    m = BILL_ROW.search(sec.group(0))
    if not m:
        return "无 999"
    return "999 不计费" if m.group(1) == "不计费" else "999 计费"


def main() -> int:
    rows = []
    for path in sorted(glob.glob("docs/API_接口文档与使用手册_*.pdf")):
        name = os.path.basename(path).replace("API_接口文档与使用手册_", "").replace(".pdf", "")
        # NFKC 把康熙部首折回常规汉字：部分 PDF 的字体会把「无/入/门」映射成 ⽆/⼊/⻔，
        # 不归一化会让关键词匹配全线漏检。
        text = unicodedata.normalize(
            "NFKC", "\n".join((pg.extract_text() or "") for pg in PdfReader(path).pages)
        )
        flat = re.sub(r"\s+", "", text)

        quota = "OK" if "totalCalls" in flat else "缺 totalCalls"
        upstream = ",".join(w for w in UPSTREAM_WORDS if w in flat) or "OK"
        cache = ",".join(w for w in CACHE_WORDS if w in flat) or "OK"
        own = own_of(name)
        cross = ",".join(
            r for r in ROUTES
            if r not in own and re.search(r"(?<![A-Za-z0-9])" + r + r"(?![A-Za-z0-9])", text)
        ) or "OK"

        want, got = bill_expectation(name), bill_actual(flat, name)
        billing = "OK" if want == got else f"应为[{want}] 实为[{got}]"

        rows.append((name, quota, billing, upstream, cache, cross))

    hdr = ("手册", "quota", "计费口径", "上游隐匿", "缓存禁令", "版本隔离")
    widths = [max(len(str(r[i])) for r in rows + [hdr]) + 2 for i in range(len(hdr))]
    line = "".join(str(h).ljust(w) for h, w in zip(hdr, widths))
    print(line)
    print("-" * len(line))
    bad = 0
    for r in rows:
        if any(c != "OK" for c in r[1:]):
            bad += 1
        print("".join(str(c).ljust(w) for c, w in zip(r, widths)))
    print(f"\n{len(rows)} 份手册，{bad} 份存在待修项。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
