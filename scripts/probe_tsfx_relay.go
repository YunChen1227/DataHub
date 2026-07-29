//go:build ignore

// probe_tsfx_relay: 对 relay 的 tsfx 路由做 HTTP 全链路探测 (投诉分析识别名单)。
// 用法:
//   RELAY_BASE_URL=http://localhost:8080 go run ./scripts/probe_tsfx_relay.go
//   RELAY_BASE_URL=http://aiszcloud.cn:8080 TSFX_APP_KEY=... TSFX_SECRET=... go run ./scripts/probe_tsfx_relay.go
// 未设置 TSFX_APP_KEY 时尝试 demo appKey y89tsfx + demo-app-secret。
package main

import (
	"fmt"
	"os"

	"github.com/datahub/relay/test/harness"
)

func main() {
	base := os.Getenv("RELAY_BASE_URL")
	if base == "" {
		base = "http://aiszcloud.cn:8080"
	}
	os.Setenv("RELAY_BASE_URL", base)

	appKey := os.Getenv("TSFX_APP_KEY")
	secret := os.Getenv("TSFX_SECRET")
	if appKey == "" {
		appKey = harness.AppKeyFor("tsfx")
	}
	if secret == "" {
		secret = harness.Secret
	}

	fmt.Println("== tsfx 线上 relay 全链路探测 (投诉分析识别名单) ==")
	fmt.Printf("  baseURL=%s\n", base)
	fmt.Printf("  appKey=%s\n", appKey)

	st, _, raw := harness.Call("GET", "/healthz", nil, nil)
	fmt.Printf("  healthz: HTTP %d body=%q\n", st, raw)

	poly := os.Getenv("TSFX_POLY")
	if poly == "" {
		poly = "C1"
	}
	body := map[string]string{
		"mobile": "13809091009",
		"poly":   poly, // 命中级别策略 C1/C2/C3 (必填)
	}
	r := harness.Query("tsfx", appKey, secret, body, nil)
	fmt.Printf("  querySrmxTSFX: HTTP %d errorCode=%s bodyCode=%s\n", r.HTTPStatus, r.ErrorCode, r.BodyCode)
	if r.Range != "" {
		fmt.Printf("  range=%s\n", trunc(r.Range, 200))
	}
	if len(r.Raw) < 500 {
		fmt.Printf("  raw=%s\n", r.Raw)
	} else {
		fmt.Printf("  raw=%s...\n", r.Raw[:500])
	}

	switch {
	case r.HTTPStatus == 404:
		fmt.Println("FAIL: 路由未注册（404），服务器可能尚未部署含 tsfx 的版本")
		os.Exit(1)
	case r.ErrorCode == "505004":
		fmt.Println("WARN: 鉴权失败 505004（appKey 未在该域开户或凭证不对）——路由已通，需用正式 tsfx license")
		os.Exit(2)
	case r.ErrorCode == "505062":
		fmt.Println("WARN: 505062（参数/上游/服务异常）——路由与鉴权可能已通，请检查上游 apiKey/aesKey/sign 或参数")
		os.Exit(2)
	case r.ErrorCode == "0" && r.BodyCode == "001":
		fmt.Println("OK: 全链路已通（调用成功计费 001，命中状态见 range.forbid）")
	default:
		fmt.Printf("WARN: 未预期的响应 errorCode=%s bodyCode=%s\n", r.ErrorCode, r.BodyCode)
		os.Exit(2)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
