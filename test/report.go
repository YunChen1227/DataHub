//go:build ignore

// report aggregates every <suite>.json under $RESULT_DIR (or the first CLI arg)
// into a human-readable REPORT.md: an overall PASS/FAIL/SKIP summary, a per-suite
// table (name / status / expected / actual / reason) and a consolidated failure
// list. Run by test/run.ps1 after all case scripts finish.
//
// Run: RESULT_DIR=test_res/2026-06-19 go run test/report.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type tcase struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Reason   string `json:"reason"`
}

type suite struct {
	Suite     string    `json:"suite"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Cases     []tcase   `json:"cases"`
}

func main() {
	dir := os.Getenv("RESULT_DIR")
	if len(os.Args) > 1 && os.Args[1] != "" {
		dir = os.Args[1]
	}
	if dir == "" {
		dir = "."
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read result dir %s: %v\n", dir, err)
		os.Exit(1)
	}
	var suites []suite
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s suite
		if err := json.Unmarshal(raw, &s); err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", e.Name(), err)
			continue
		}
		suites = append(suites, s)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Suite < suites[j].Suite })

	var b strings.Builder
	tot, totP, totF, totS := 0, 0, 0, 0
	type failItem struct{ suite, name, expected, actual, reason string }
	var fails []failItem

	for _, s := range suites {
		for _, c := range s.Cases {
			tot++
			switch c.Status {
			case "PASS":
				totP++
			case "FAIL":
				totF++
				fails = append(fails, failItem{s.Suite, c.Name, c.Expected, c.Actual, c.Reason})
			case "SKIP":
				totS++
			}
		}
	}

	b.WriteString("# DataHub 测试汇总报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间：%s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("- 结果目录：`%s`\n", dir))
	b.WriteString(fmt.Sprintf("- 套件数：%d\n", len(suites)))
	overall := "✅ 全部通过"
	if totF > 0 {
		overall = "❌ 存在失败"
	}
	b.WriteString(fmt.Sprintf("- 总用例：%d　通过：%d　失败：%d　跳过：%d　→ **%s**\n\n", tot, totP, totF, totS, overall))

	// 套件总览
	b.WriteString("## 套件总览\n\n")
	b.WriteString("| 套件 | 说明 | 通过 | 失败 | 跳过 | 结果 |\n|---|---|---:|---:|---:|---|\n")
	for _, s := range suites {
		p, f, sk := count(s)
		res := "✅"
		if f > 0 {
			res = "❌"
		} else if p == 0 && sk > 0 {
			res = "⏭️"
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %d | %d | %d | %s |\n", s.Suite, s.Title, p, f, sk, res))
	}
	b.WriteString("\n")

	// 失败清单
	if len(fails) > 0 {
		b.WriteString("## 失败清单（需关注）\n\n")
		b.WriteString("| 套件 | 用例 | 期望 | 实际 | 原因 |\n|---|---|---|---|---|\n")
		for _, f := range fails {
			b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s |\n",
				f.suite, f.name, md(f.expected), md(truncate(f.actual, 160)), md(truncate(f.reason, 200))))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("## 失败清单\n\n无失败用例。\n\n")
	}

	// 逐套件明细
	b.WriteString("## 逐套件明细\n\n")
	for _, s := range suites {
		b.WriteString(fmt.Sprintf("### %s — %s\n\n", s.Suite, s.Title))
		b.WriteString("| 用例 | 状态 | 期望 | 实际/原因 |\n|---|---|---|---|\n")
		for _, c := range s.Cases {
			icon := map[string]string{"PASS": "✅ PASS", "FAIL": "❌ FAIL", "SKIP": "⏭️ SKIP"}[c.Status]
			detail := c.Actual
			if c.Status != "PASS" && c.Reason != "" {
				detail = c.Reason
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				md(c.Name), icon, md(c.Expected), md(truncate(detail, 160))))
		}
		b.WriteString("\n")
	}

	out := filepath.Join(dir, "REPORT.md")
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("report written: %s (total=%d pass=%d fail=%d skip=%d)\n", out, tot, totP, totF, totS)
}

func count(s suite) (p, f, sk int) {
	for _, c := range s.Cases {
		switch c.Status {
		case "PASS":
			p++
		case "FAIL":
			f++
		case "SKIP":
			sk++
		}
	}
	return
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// md escapes characters that would break a Markdown table cell.
func md(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if s == "" {
		return "-"
	}
	return s
}
