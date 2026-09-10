//go:build ignore

// 测试阿里云 relay（aiszcloud.cn:8080）sfsm 全链路。
// go run ./scripts/probe_sfsm_aliyun.go
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
	appKey         = "8yznut8j2yb3"
	secret         = "607e0591ee81828602818e28e13f35af"
	notFoundIDCard = "000000000000000404"
)

func main() {
	os.Setenv("RELAY_BASE_URL", baseURL)
	version := "sfsm"

	base := map[string]string{
		"idCard": "440303200002163115",
		"name":   "陈韫",
	}

	fmt.Println("== sfsm 阿里云全链路探测 ==")
	fmt.Printf("目标: %s\n", baseURL)
	fmt.Printf("接口: POST %s\n", harness.QueryPath(version))
	fmt.Printf("appKey: %s\n", appKey)
	fmt.Printf("sign(base): %s\n\n", harness.SignX1(base, secret))

	st, _, raw := harness.Call("GET", "/healthz", nil, nil)
	fmt.Printf("[healthz] HTTP=%d raw=%s\n\n", st, raw)
	if st != 200 {
		fmt.Println("FAIL: relay 未就绪（可能进程崩溃，常见原因：datahub_sfsm_db 未建库 / 二进制未含 sfsm 路由）")
		os.Exit(1)
	}

	st, qm, _ := harness.Call("GET", harness.QuotaPath(version), map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           harness.SignX1(map[string]string{}, secret),
		"body":           map[string]string{},
	}, nil)
	fmt.Printf("[quotaSFSM] HTTP=%d\n", st)
	if b, _ := json.Marshal(qm); len(b) > 0 {
		fmt.Println(string(b))
	}
	fmt.Println()

	r := harness.Query(version, appKey, secret, base, nil)
	fmt.Printf("[主查询] HTTP=%d errorCode=%s bodyCode=%s\n", r.HTTPStatus, r.ErrorCode, r.BodyCode)
	fmt.Printf("range=%q\n", r.Range)
	fmt.Println(r.Raw)
	fmt.Println()

	if r.ErrorCode == "0" && r.BodyCode == "001" {
		printRangeInterpretation(r.Range)
	}

	nf := map[string]string{"idCard": notFoundIDCard, "name": "陈韫"}
	r2 := harness.Query(version, appKey, secret, nf, nil)
	fmt.Printf("[查无探针] HTTP=%d errorCode=%s bodyCode=%s\n", r2.HTTPStatus, r2.ErrorCode, r2.BodyCode)
	fmt.Println(r2.Raw)
	fmt.Println()

	// 主查询：001 且 range 含 result/desc 即视为链路通（一致或不一致都算查得）。
	ok := r.ErrorCode == "0" && r.BodyCode == "001" && rangeHasBizFields(r.Range)
	nfOK := r2.ErrorCode == "0" && r2.BodyCode == "999"
	if ok && nfOK {
		fmt.Println("RESULT: PASS（查得 001 + 查无 999）")
		return
	}
	if ok {
		fmt.Println("RESULT: PARTIAL（主查询通过，查无探针未达 999——可能上游对测试号无「无记录」分支，主链路已通）")
		return
	}

	fmt.Println("RESULT: FAIL")
	diagnose(r)
	os.Exit(1)
}

func rangeHasBizFields(raw string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		fmt.Printf("  range 非合法 JSON: %v\n", err)
		return false
	}
	for _, f := range []string{"result", "desc"} {
		if _, ok := m[f]; !ok {
			fmt.Printf("  缺字段: %s\n", f)
			return false
		}
	}
	if _, leaked := m["order_no"]; leaked {
		fmt.Println("  泄漏上游 order_no（不应出现在 range）")
		return false
	}
	return true
}

func printRangeInterpretation(raw string) {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return
	}
	res, _ := m["result"].(float64) // json 数字默认 float64
	desc, _ := m["desc"].(string)
	switch int(res) {
	case 0:
		fmt.Printf("核验结论: 一致 (result=0, desc=%q) — body.code=001 计费\n", desc)
	case 1:
		fmt.Printf("核验结论: 不一致 (result=1, desc=%q) — body.code=001 计费（不一致也是查得）\n", desc)
	default:
		fmt.Printf("核验结论: result=%v desc=%q\n", res, desc)
	}
}

func diagnose(r harness.X1Result) {
	switch {
	case r.ErrorCode == "505004":
		fmt.Println("原因提示: appKey 不存在或不属于 sfsm 域 → 检查管理后台是否在 SFSM 标签下创建了该 license")
	case r.ErrorCode == "505002":
		fmt.Println("原因提示: 签名错误 → 检查 appSecret 是否与后台一致")
	case r.ErrorCode == "505007":
		fmt.Println("原因提示: 账户停用或未开通 → 检查 license 状态与有效期")
	case r.ErrorCode == "505062" && r.BodyCode == "":
		fmt.Println("原因提示: 上游侧错误 → 常见：数脉 appId/appSecret 错(400)、余额不足(603)、账户停用(602)、上游超时/不可达")
		fmt.Println("          也可 grep relay.log: upstream call failed route=sfsm")
	case strings.Contains(r.Raw, "404") || r.HTTPStatus == 404:
		fmt.Println("原因提示: 路由未注册 → 服务器 relay 二进制可能未包含 sfsm 接入代码，需重新部署")
	}
}

func init() {
	_ = strings.ToUpper
}
