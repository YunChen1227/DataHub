//go:build ignore

// 仅测试阿里云 relay（aiszcloud.cn:8080），参数严格按 API 手册。
// go run ./scripts/probe_grgjj_aliyun.go
package main

import (
	"fmt"
	"os"

	"github.com/datahub/relay/test/harness"
)

func main() {
	os.Setenv("RELAY_BASE_URL", "http://aiszcloud.cn:8080")

	appKey := "u4kwtvc588h5"
	secret := "88387639ef790ba28e882856a559159b"
	body := map[string]string{
		"mobile": "13809091009",
		"idCard": "330129199109094312",
		"name":   "张三",
	}

	fmt.Println("目标: http://aiszcloud.cn:8080")
	fmt.Println("接口: POST /v1/openapi/zlx/querySrmxGRGJJ")
	fmt.Printf("appKey(appid): %s\n", appKey)
	fmt.Printf("appSecret(appkey): %s\n", secret)
	fmt.Printf("body(手册示例): mobile=%s idCard=%s name=%s\n", body["mobile"], body["idCard"], body["name"])
	fmt.Printf("sign: %s\n", harness.SignX1(body, secret))
	fmt.Println()

	st, hm, _ := harness.Call("GET", "/healthz", nil, nil)
	fmt.Printf("healthz HTTP=%d body=%v\n", st, hm)

	st, qm, _ := harness.Call("GET", harness.QuotaPath("grgjj"), map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           harness.SignX1(map[string]string{}, secret),
		"body":           map[string]string{},
	}, nil)
	fmt.Printf("quota HTTP=%d body=%v\n\n", st, qm)

	r := harness.Query("grgjj", appKey, secret, body, nil)
	fmt.Printf("query HTTP=%d errorCode=%s bodyCode=%s range=%q\n", r.HTTPStatus, r.ErrorCode, r.BodyCode, r.Range)
	fmt.Println(r.Raw)
}
