//go:build ignore

// Full-link e2e verification against a running relay (:8080) + mock upstreams
// (mock_gama for x1, mock_income for v9/v8). 三版本对外统一为 x1 信封格式，仅靠
// 路由名区分。Drives POST querySrmx{X1,V9,V8} and verifies the head/body envelope,
// billing (查到才计费), error codes, per-version quota accounting, version
// isolation, and version-scoped admin audit logs.
//
// Run: go run scripts/e2e.go
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

const (
	baseURL   = "http://localhost:8080"
	secret    = "demo-app-secret"
	appKey    = "y89098io"
	adminUser = "admin"
	adminPass = "admin12345"
)

var versions = []string{"x1", "v9", "v8"}

var (
	pass int
	fail int
)

func queryPath(v string) string { return "/v1/openapi/zlx/querySrmx" + strings.ToUpper(v) }
func quotaPath(v string) string { return "/v1/openapi/zlx/quota" + strings.ToUpper(v) }
func adminBase(v string) string { return "/admin/api/" + v }

func sign(params map[string]string, sec string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(sec)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func call(method, path string, body any, headers map[string]string) (int, map[string]any, string) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func query(version, key string, body map[string]string, overrides map[string]any) (errorCode, bodyCode, rng, raw string) {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         key,
		"sign":           sign(body, secret),
		"body":           body,
	}
	for k, v := range overrides {
		payload[k] = v
	}
	_, m, raw := call(http.MethodPost, queryPath(version), payload, nil)
	if m == nil {
		return "", "", "", raw
	}
	if head, ok := m["head"].(map[string]any); ok {
		errorCode, _ = head["errorCode"].(string)
	}
	if b, ok := m["body"].(map[string]any); ok {
		bodyCode, _ = b["code"].(string)
		if res, ok := b["result"].(map[string]any); ok {
			rng, _ = res["range"].(string)
		}
	}
	return errorCode, bodyCode, rng, raw
}

func check(name string, cond bool, detail string) {
	if cond {
		pass++
		fmt.Printf("  [PASS] %s\n", name)
	} else {
		fail++
		fmt.Printf("  [FAIL] %s -> %s\n", name, detail)
	}
}

func serviceUsed(version string) float64 {
	payload := map[string]any{"encryptionType": 1, "appKey": appKey, "sign": sign(map[string]string{}, secret), "body": map[string]string{}}
	_, m, _ := call(http.MethodGet, quotaPath(version), payload, nil)
	if u, ok := m["serviceUsed"].(float64); ok {
		return u
	}
	return -1
}

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	fmt.Println("== Full-link e2e (querySrmx{X1,V9,V8} -> 各版本独立上游) ==")

	st, _, b := call(http.MethodGet, "/healthz", nil, nil)
	check("GET /healthz == 200 ok", st == 200 && strings.Contains(b, "ok"), b)

	// 逐版本：成功/查无/错签/未知账户/缺 appKey/参数非法/二次成功 + 配额增量。
	for _, v := range versions {
		fmt.Printf("\n-- version %s --\n", v)
		usedBefore := serviceUsed(v)
		fmt.Printf("  %s serviceUsed(before) = %v\n", v, usedBefore)

		ec, bc, rng, raw := query(v, appKey, base(), nil)
		check(v+" success: head.errorCode=0 body.code=001 range=7", ec == "0" && bc == "001" && rng == "7", raw)

		nf := base()
		nf["mobile"] = "13800000000"
		ec, bc, _, raw = query(v, appKey, nf, nil)
		check(v+" not-found: head.errorCode=0 body.code=999", ec == "0" && bc == "999", raw)

		ec, bc, _, raw = query(v, appKey, base(), map[string]any{"sign": "deadbeef"})
		check(v+" bad-sign: head.errorCode=505002, no body", ec == "505002" && bc == "", raw)

		ec, _, _, raw = query(v, "nonexistent", base(), nil)
		check(v+" unknown-appKey: head.errorCode=505004", ec == "505004", raw)

		ec, _, _, raw = query(v, "", base(), map[string]any{"appKey": ""})
		check(v+" missing-appKey: head.errorCode=505001", ec == "505001", raw)

		bad := base()
		bad["mobile"] = "139xx"
		ec, _, _, raw = query(v, appKey, bad, nil)
		check(v+" param-invalid: head.errorCode=505062", ec == "505062", raw)

		ec, bc, rng, raw = query(v, appKey, base(), nil)
		check(v+" success#2: body.code=001 range=7", ec == "0" && bc == "001" && rng == "7", raw)

		usedAfter := serviceUsed(v)
		fmt.Printf("  %s serviceUsed(after) = %v\n", v, usedAfter)
		check(v+" billing: 成功查得 only +2 (查无不计)", usedAfter-usedBefore == 2,
			fmt.Sprintf("delta=%v (want 2)", usedAfter-usedBefore))
	}

	// 版本隔离：对各版本各发了等量流量，三版本计数应相等且互不串。
	x1Used, v9Used, v8Used := serviceUsed("x1"), serviceUsed("v9"), serviceUsed("v8")
	check("version isolation: x1==v9==v8 成功查得数 (各自独立累加)",
		x1Used == v9Used && v9Used == v8Used,
		fmt.Sprintf("x1=%v v9=%v v8=%v", x1Used, v9Used, v8Used))

	// admin: 统一登录 + 版本作用域审计。
	st, m, r := call(http.MethodPost, "/admin/api/login",
		map[string]string{"username": adminUser, "password": adminPass}, nil)
	token, _ := m["token"].(string)
	check("admin login -> token", st == 200 && token != "", r)

	if token != "" {
		auth := map[string]string{"Authorization": "Bearer " + token}
		for _, v := range versions {
			_, am, ar := call(http.MethodGet, adminBase(v)+"/audits?appKey="+appKey+"&limit=200", nil, auth)
			audits, _ := am["audits"].([]any)
			var seen10, seen1000, masked bool
			for _, a := range audits {
				rec, _ := a.(map[string]any)
				if bcd, _ := rec["busiCode"].(float64); bcd == 10 {
					seen10 = true
				} else if bcd == 1000 {
					seen1000 = true
				}
				if nm, _ := rec["nameMask"].(string); strings.Contains(nm, "*") {
					masked = true
				}
			}
			check(v+" audit: records present", len(audits) > 0, ar)
			check(v+" audit: has busiCode=10 (查得数据)", seen10, "")
			check(v+" audit: has busiCode=1000 (查无)", seen1000, "")
			check(v+" audit: PII masked", masked, "")
		}
		stN, _, _ := call(http.MethodGet, adminBase("x1")+"/audits", nil, nil)
		check("admin audits without token -> 401", stN == 401, fmt.Sprintf("status=%d", stN))
	}

	fmt.Printf("\n== Result: %d passed, %d failed ==\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
