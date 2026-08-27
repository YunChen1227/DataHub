//go:build ignore

// 测试阿里云 relay（aiszcloud.cn:8080）grsb 全链路。
// go run ./scripts/probe_grsb_aliyun.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const (
	baseURL        = "http://aiszcloud.cn:8080"
	appKey         = "bhiuvx5m4ug9"
	secret         = "43d3d18bfc5dd42ca73a2d96ac99e01e"
	notFoundIDCard = "000000000000000404"
)

func main() {
	os.Setenv("RELAY_BASE_URL", baseURL)
	version := "grsb"

	base := map[string]string{
		"idCard": "440303200002163115",
		"name":   "陈韫",
	}

	fmt.Println("== grsb 阿里云全链路探测 ==")
	fmt.Printf("目标: %s\n", baseURL)
	fmt.Printf("接口: POST %s\n", harness.QueryPath(version))
	fmt.Printf("appKey: %s\n", appKey)
	fmt.Printf("sign(base): %s\n\n", harness.SignX1(base, secret))

	st, hm, raw := harness.Call("GET", "/healthz", nil, nil)
	fmt.Printf("[healthz] HTTP=%d body=%v raw=%s\n\n", st, hm, raw)
	if st != 200 {
		fmt.Println("FAIL: relay 未就绪（可能进程崩溃，常见原因：datahub_grsb_db 未建库）")
		os.Exit(1)
	}

	st, qm, _ := harness.Call("GET", harness.QuotaPath(version), map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           harness.SignX1(map[string]string{}, secret),
		"body":           map[string]string{},
	}, nil)
	fmt.Printf("[quotaGRSB] HTTP=%d\n", st)
	if b, _ := json.Marshal(qm); len(b) > 0 {
		fmt.Println(string(b))
	}
	fmt.Println()

	r := harness.Query(version, appKey, secret, base, nil)
	fmt.Printf("[查得] HTTP=%d errorCode=%s bodyCode=%s\n", r.HTTPStatus, r.ErrorCode, r.BodyCode)
	fmt.Printf("range=%q\n", r.Range)
	fmt.Println(r.Raw)
	fmt.Println()

	nf := map[string]string{"idCard": notFoundIDCard, "name": "陈韫"}
	r2 := harness.Query(version, appKey, secret, nf, nil)
	fmt.Printf("[查无] HTTP=%d errorCode=%s bodyCode=%s\n", r2.HTTPStatus, r2.ErrorCode, r2.BodyCode)
	fmt.Println(r2.Raw)
	fmt.Println()

	ok := r.ErrorCode == "0" && r.BodyCode == "001" && rangeHasAllFields(r.Range)
	nfOK := r2.ErrorCode == "0" && r2.BodyCode == "999"
	if ok && nfOK {
		fmt.Println("RESULT: PASS（查得 001 + 查无 999）")
		return
	}
	fmt.Println("RESULT: FAIL")
	if r.ErrorCode == "505004" {
		fmt.Println("原因提示: appKey 不存在或不属于 grsb 域 → 检查管理后台是否在 grsb 域创建了该 license")
	}
	if r.ErrorCode == "505002" {
		fmt.Println("原因提示: 签名错误 → 检查 appSecret 是否与后台一致")
	}
	if r.ErrorCode == "505062" && r.BodyCode == "" {
		fmt.Println("原因提示: 上游侧错误 → 常见：IP 未加白(2-508)、encryptKey 错(2-501)、accountId 错(2-507)、上游超时/不可达")
	}
	if r.ErrorCode == "0" && r.BodyCode == "002" {
		fmt.Println("原因提示: 上游返回失败码 → 看 relay.log 里 bgpg 上游原始 code/retMsg")
	}
	os.Exit(1)
}

func rangeHasAllFields(raw string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	for _, f := range []string{"xm", "sfz", "jfdw", "grsf", "jfjs", "cbjfzt", "jfsj"} {
		if _, ok := m[f]; !ok {
			fmt.Printf("  缺字段: %s\n", f)
			return false
		}
	}
	return true
}

func init() {
	_ = strings.ToUpper
}
