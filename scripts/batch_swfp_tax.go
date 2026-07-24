//go:build ignore

// batch_swfp_tax: 批量查询 Excel 税号在 swfp 是否有数据。
// Usage:
//   RELAY_BASE_URL=http://aiszcloud.cn:8080 SWFP_APP_KEY=... SWFP_SECRET=... go run scripts/batch_swfp_tax.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/datahub/relay/test/harness"
)

var creditCodes = []string{
	"911101055695184024",
	"914413035645245121",
	"91441381MAD7PP0M1U",
	"91441303MA52HE1729",
	"91441300MA530DHF52",
	"91330200563871993X",
	"913205067615027420",
	"913101165647980623",
	"91320583MA1PCENB13",
	"91320583MA1YLAK333",
	"91330201MA290X463B",
	"913302015670062191",
	"91310115MA1K41YN6W",
	"91320594398379945T",
	"91320594MA27DUCL8R",
}

type section struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

func main() {
	appKey := os.Getenv("SWFP_APP_KEY")
	secret := os.Getenv("SWFP_SECRET")
	if appKey == "" || secret == "" {
		fmt.Println("need SWFP_APP_KEY and SWFP_SECRET")
		os.Exit(1)
	}
	base := os.Getenv("RELAY_BASE_URL")
	if base == "" {
		base = "http://aiszcloud.cn:8080"
	}
	os.Setenv("RELAY_BASE_URL", base)

	fmt.Printf("SWFP batch probe @ %s\n\n", base)
	fmt.Printf("%-22s | %-8s | %-8s | sections (invoice1/invoice2/tax1/tax2)\n", "creditCode", "head", "body")
	fmt.Println(strings.Repeat("-", 90))

	hasData := 0
	for i, cc := range creditCodes {
		r := harness.Query("swfp", appKey, secret, map[string]string{"creditCode": cc}, nil)
		if i == 0 {
			fmt.Printf("sample raw: %s\n\n", r.Raw)
		}
		secs := summarizeRange(r.Range)
		fmt.Printf("%-22s | %-8s | %-8s | %s\n", cc, r.ErrorCode, r.BodyCode, secs)
		if r.ErrorCode == "0" && (r.BodyCode == "001" || r.BodyCode == "002") {
			hasData++
		}
	}
	fmt.Printf("\n合计: %d/%d 有数据 (body.code=001 或 002)\n", hasData, len(creditCodes))
}

func summarizeRange(raw string) string {
	if raw == "" {
		return "-"
	}
	var m map[string]section
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "parse_err"
	}
	parts := make([]string, 0, 4)
	for _, k := range []string{"invoice1", "invoice2", "tax1", "tax2"} {
		s, ok := m[k]
		if !ok {
			parts = append(parts, k+":?")
			continue
		}
		tag := s.Status
		if tag == "ok" && len(s.Data) > 2 {
			tag = "ok+data"
		}
		if tag == "error" && s.Error != "" {
			tag = "err:" + truncate(s.Error, 20)
		}
		parts = append(parts, k+":"+tag)
	}
	return strings.Join(parts, " | ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
