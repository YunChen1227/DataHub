//go:build ignore

// 阿里云 relay 全链路：dtjd / snhmd / dtly 三条路由（网关鉴权 + 上游）。
//
// 凭证勿写进本文件，用环境变量传入：
//   set RELAY_BASE_URL=http://aiszcloud.cn:8080
//   set DTJD_APP_KEY=... & set DTJD_APP_SECRET=...
//   set SNHMD_APP_KEY=... & set SNHMD_APP_SECRET=...
//   set DTLY_APP_KEY=... & set DTLY_APP_SECRET=...
// 可选：PROBE_NAME、PROBE_IDCARD、PROBE_MOBILE
//
// go run ./scripts/probe_shouxin_routes_aliyun.go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/datahub/relay/test/harness"
)

type routeCred struct {
	version string
	label   string
	appKey  string
	secret  string
}

func envOrFail(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		fmt.Fprintf(os.Stderr, "缺少环境变量 %s\n", key)
		os.Exit(2)
	}
	return v
}

func main() {
	os.Setenv("RELAY_BASE_URL", firstNonEmpty(os.Getenv("RELAY_BASE_URL"), "http://aiszcloud.cn:8080"))

	routes := []routeCred{
		{"dtjd", "DTJD 多头借贷", envOrFail("DTJD_APP_KEY"), envOrFail("DTJD_APP_SECRET")},
		{"snhmd", "SNHMD 司南黑名单", envOrFail("SNHMD_APP_KEY"), envOrFail("SNHMD_APP_SECRET")},
		{"dtly", "DTLY 多头履约", envOrFail("DTLY_APP_KEY"), envOrFail("DTLY_APP_SECRET")},
	}

	name := os.Getenv("PROBE_NAME")
	if name == "" {
		name = "陈韫"
	}
	idCard := os.Getenv("PROBE_IDCARD")
	if idCard == "" {
		idCard = "440303200002163115"
	}
	mobile := os.Getenv("PROBE_MOBILE")
	if mobile == "" {
		mobile = "13670010670"
	}
	body := map[string]string{"name": name, "idCard": idCard, "mobile": mobile}

	fmt.Println("== 守信三条路由 · 阿里云 relay 全链路 ==")
	fmt.Printf("目标: %s\n", harness.BaseURL())
	fmt.Printf("三要素: name=%s idCard=%s mobile=%s\n\n", name, maskID(idCard), maskMobile(mobile))

	st, _, raw := harness.Call("GET", "/healthz", nil, nil)
	fmt.Printf("[healthz] HTTP=%d raw=%s\n\n", st, raw)
	if st != 200 {
		fmt.Println("FAIL: relay 未就绪")
		os.Exit(1)
	}

	allOK := true
	for _, rc := range routes {
		fmt.Println(strings.Repeat("-", 60))
		fmt.Printf("[%s] POST %s\n", rc.label, harness.QueryPath(rc.version))
		fmt.Printf("appKey=%s\n", rc.appKey)

		r := harness.Query(rc.version, rc.appKey, rc.secret, body, nil)
		fmt.Printf("HTTP=%d head.errorCode=%s body.code=%s logId=%s\n", r.HTTPStatus, r.ErrorCode, r.BodyCode, r.LogID)
		if r.Range != "" {
			preview := r.Range
			if len(preview) > 400 {
				preview = preview[:400] + "..."
			}
			fmt.Printf("result.range (preview): %s\n", preview)
		}
		fmt.Println(r.Raw)

		ok := r.ErrorCode == "0" && (r.BodyCode == "001" || r.BodyCode == "999")
		if !ok {
			allOK = false
			hint(r)
		} else {
			fmt.Printf("→ %s OK (001查得 / 999查无 均属网关正常)\n", rc.version)
		}
		fmt.Println()
	}

	if allOK {
		fmt.Println("RESULT: PASS（三条路由网关鉴权通过且上游有正常业务响应）")
		return
	}
	fmt.Println("RESULT: FAIL")
	os.Exit(1)
}

func hint(r harness.X1Result) {
	switch r.ErrorCode {
	case "505004":
		fmt.Println("提示: appKey 不存在或不属于该路由域 → 管理后台在该域创建 license")
	case "505002":
		fmt.Println("提示: 签名错误 → 检查 appSecret")
	case "505062":
		fmt.Println("提示: 上游侧错误 → IP 白名单、AES 密钥、库未建、上游不可达等")
	case "505001":
		fmt.Println("提示: 参数错误 → 三要素是否齐全")
	default:
		if r.BodyCode == "002" {
			fmt.Println("提示: body.code=002 → 看 relay 日志里上游 resp_code")
		}
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}

func maskID(s string) string {
	if len(s) < 8 {
		return "***"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

func maskMobile(s string) string {
	if len(s) < 7 {
		return "***"
	}
	return s[:3] + "****" + s[len(s)-4:]
}
