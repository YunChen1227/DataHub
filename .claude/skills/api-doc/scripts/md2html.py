# -*- coding: utf-8 -*-
"""极简 Markdown -> HTML，仅覆盖本项目使用手册用到的语法：
标题(#..####)、表格、围栏代码块(```lang)、有序/无序列表、blockquote(>)、
粗体(**)、行内代码(`)、水平线(---)、段落。套用 x1.html 的样式模板。
"""
import sys, re, html

STYLE = """  @page { size: A4; margin: 18mm 16mm; }
  body { font-family: "Microsoft YaHei", "Segoe UI", sans-serif; font-size: 13px; line-height: 1.6; color: #24292e; }
  h1 { font-size: 24px; border-bottom: 2px solid #eaecef; padding-bottom: 8px; }
  h2 { font-size: 19px; border-bottom: 1px solid #eaecef; padding-bottom: 6px; margin-top: 26px; }
  h3 { font-size: 16px; margin-top: 20px; }
  h4 { font-size: 14px; }
  table { border-collapse: collapse; width: 100%; margin: 10px 0; }
  th, td { border: 1px solid #d0d7de; padding: 6px 10px; text-align: left; vertical-align: top; }
  th { background: #f6f8fa; }
  code { background: #f6f8fa; padding: 1px 5px; border-radius: 4px; font-family: Consolas, "Courier New", monospace; }
  pre { background: #f6f8fa; padding: 12px; border-radius: 6px; overflow-x: auto; }
  pre code { background: none; padding: 0; }
  blockquote { color: #57606a; border-left: 4px solid #d0d7de; padding-left: 12px; margin-left: 0; }
  h1, h2, h3, h4 { page-break-after: avoid; }
  pre, table { page-break-inside: avoid; }"""


def inline(t):
    # 先转义 HTML 特殊字符，再恢复我们要保留的行内标记
    # 行内代码优先：用占位符保护
    codes = []
    def stash(m):
        codes.append(m.group(1))
        return f"\x00{len(codes)-1}\x00"
    t = re.sub(r'`([^`]+)`', stash, t)
    t = html.escape(t, quote=False)
    # 链接 [文本](#锚点) —— 文档内锚点，转成普通文本的加粗引用（PDF 内锚点无意义，保留可读文字）
    t = re.sub(r'\[([^\]]+)\]\(#[^)]*\)', r'「\1」', t)
    # 链接 [文本](http...) —— 保留为可点击链接
    t = re.sub(r'\[([^\]]+)\]\((https?://[^)]+)\)', r'<a href="\2">\1</a>', t)
    # 粗体
    t = re.sub(r'\*\*([^*]+)\*\*', r'<strong>\1</strong>', t)
    # 恢复行内代码
    def restore(m):
        return f"<code>{html.escape(codes[int(m.group(1))], quote=False)}</code>"
    t = re.sub(r'\x00(\d+)\x00', restore, t)
    return t


def convert(md):
    lines = md.split('\n')
    out = []
    i = 0
    n = len(lines)
    while i < n:
        line = lines[i]
        # 围栏代码块
        m = re.match(r'^```(\w*)\s*$', line)
        if m:
            lang = m.group(1)
            i += 1
            buf = []
            while i < n and not re.match(r'^```\s*$', lines[i]):
                buf.append(lines[i])
                i += 1
            i += 1  # 跳过结束 ```
            cls = f' class="language-{lang}"' if lang else ''
            code = html.escape('\n'.join(buf), quote=False)
            out.append(f'<pre><code{cls}>{code}\n</code></pre>')
            continue
        # 标题
        m = re.match(r'^(#{1,4})\s+(.*)$', line)
        if m:
            lvl = len(m.group(1))
            out.append(f'<h{lvl}>{inline(m.group(2).strip())}</h{lvl}>')
            i += 1
            continue
        # 水平线
        if re.match(r'^---+\s*$', line):
            out.append('<hr>')
            i += 1
            continue
        # 表格：当前行含 | 且下一行是分隔行
        if '|' in line and i + 1 < n and re.match(r'^\s*\|?[\s:|-]+\|[\s:|-]*$', lines[i+1]) and '-' in lines[i+1]:
            header = [c.strip() for c in line.strip().strip('|').split('|')]
            i += 2  # 跳过表头和分隔行
            rows = []
            while i < n and '|' in lines[i] and lines[i].strip():
                rows.append([c.strip() for c in lines[i].strip().strip('|').split('|')])
                i += 1
            th = ''.join(f'<th>{inline(c)}</th>' for c in header)
            trs = []
            for r in rows:
                tds = ''.join(f'<td>{inline(c)}</td>' for c in r)
                trs.append(f'<tr>{tds}</tr>')
            out.append(f'<table><thead><tr>{th}</tr></thead><tbody>{"".join(trs)}</tbody></table>')
            continue
        # blockquote（连续多行 >）
        if re.match(r'^>\s?', line):
            buf = []
            while i < n and re.match(r'^>\s?', lines[i]):
                buf.append(re.sub(r'^>\s?', '', lines[i]))
                i += 1
            inner = '<br>'.join(inline(b) for b in buf if b.strip() != '')
            out.append(f'<blockquote><p>{inner}</p></blockquote>')
            continue
        # 有序列表
        if re.match(r'^\d+\.\s+', line):
            buf = []
            while i < n and re.match(r'^\d+\.\s+', lines[i]):
                buf.append(re.sub(r'^\d+\.\s+', '', lines[i]))
                i += 1
            items = ''.join(f'<li>{inline(b)}</li>' for b in buf)
            out.append(f'<ol>{items}</ol>')
            continue
        # 无序列表（- 或 * ），含 [ ] 复选框
        if re.match(r'^[-*]\s+', line):
            buf = []
            while i < n and re.match(r'^[-*]\s+', lines[i]):
                item = re.sub(r'^[-*]\s+', '', lines[i])
                cb = re.match(r'^\[( |x)\]\s+(.*)$', item)
                if cb:
                    checked = ' checked' if cb.group(1) == 'x' else ''
                    buf.append(f'<input disabled type="checkbox"{checked}> ' + inline(cb.group(2)))
                else:
                    buf.append(inline(item))
                i += 1
            items = ''.join(f'<li>{b}</li>' for b in buf)
            out.append(f'<ul>{items}</ul>')
            continue
        # 空行
        if line.strip() == '':
            i += 1
            continue
        # 普通段落
        out.append(f'<p>{inline(line.strip())}</p>')
        i += 1
    return '\n'.join(out)


def main():
    src, dst = sys.argv[1], sys.argv[2]
    with open(src, encoding='utf-8') as f:
        md = f.read()
    body = convert(md)
    doc = f'''<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>API 接口文档与使用手册</title><style>
{STYLE}
</style></head>
<body>{body}</body>
</html>'''
    with open(dst, 'w', encoding='utf-8') as f:
        f.write(doc)
    print(f"OK: {dst}")


if __name__ == '__main__':
    main()
