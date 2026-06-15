// Convert the API manual markdown into a print-ready HTML file (UTF-8, CJK fonts,
// styled tables/code, A4 @page margins). Then Edge headless prints it to PDF.
//   node scripts/md2html.js <input.md> <output.html>
const fs = require('fs');
const path = require('path');

// marked ships inside the global `md-to-pdf` install; resolve it automatically.
let marked;
try {
  ({ marked } = require('marked'));
} catch {
  const { execSync } = require('child_process');
  const globalRoot = execSync('npm root -g').toString().trim();
  ({ marked } = require(path.join(globalRoot, 'md-to-pdf', 'node_modules', 'marked')));
}

const input = process.argv[2];
const output = process.argv[3];
const md = fs.readFileSync(input, 'utf8');
const bodyHtml = marked.parse(md);

const css = `
  @page { size: A4; margin: 18mm 16mm; }
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
  pre, table { page-break-inside: avoid; }
`;

const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>API 接口文档与使用手册</title><style>${css}</style></head>
<body>${bodyHtml}</body>
</html>`;

fs.writeFileSync(output, html, 'utf8');
console.log('HTML written:', output);
