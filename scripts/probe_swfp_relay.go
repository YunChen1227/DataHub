//go:build ignore

// probe_swfp_relay: 对部署中的 relay 做 swfp 全链路探测。
//   RELAY_BASE_URL=http://aiszcloud.cn:8080 go run ./scripts/probe_swfp_relay.go
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

	creditCode := os.Getenv("SWFP_CREDIT_CODE")
	if creditCode == "" {
		creditCode = "91330100MA2AAAAA0X"
	}

	fmt.Printf("== SWFP relay probe @ %s creditCode=%s ==\n", base, creditCode)
	r := harness.Query("swfp", harness.AppKeyFor("swfp"), harness.Secret,
		map[string]string{"creditCode": creditCode}, nil)
	fmt.Printf("head.errorCode=%s body.code=%s http=%d\n",
		r.ErrorCode, r.BodyCode, r.HTTPStatus)
	if r.Range != "" {
		fmt.Printf("range=%s\n", r.Range)
	}
	if r.Raw != "" {
		fmt.Printf("raw=%s\n", r.Raw)
	}
}
