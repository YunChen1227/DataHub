//go:build ignore

// Full-link e2e verification against a running relay (:8080) + mock 伽马 upstream.
// Drives POST /v1/openapi/zlx/querySrmxV9 per 接口文档-经济能力.doc and verifies the
// head/body envelope, billing (查到才计费), error codes, idempotency, quota
// accounting and admin audit logs.
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
	queryPath = "/v1/openapi/zlx/querySrmxV9"
	secret    = "demo-app-secret"
	appKey    = "y89098io"
	adminUser = "admin"
	adminPass = "admin12345"
)

var (
	pass int
	fail int
)

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

// query builds the envelope, signs the body, and returns (errorCode, bodyCode, range, raw).
func query(body map[string]string, overrides map[string]any) (errorCode, bodyCode, rng, raw string) {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           sign(body, secret),
		"body":           body,
	}
	for k, v := range overrides {
		payload[k] = v
	}
	_, m, raw := call(http.MethodPost, queryPath, payload, nil)
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

func serviceUsed() float64 {
	payload := map[string]any{"encryptionType": 1, "appKey": appKey, "sign": sign(map[string]string{}, secret), "body": map[string]string{}}
	_, m, _ := call(http.MethodGet, "/v1/openapi/zlx/quota", payload, nil)
	if u, ok := m["serviceUsed"].(float64); ok {
		return u
	}
	return -1
}

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	fmt.Println("== Full-link e2e (.doc querySrmxV9 -> 伽马 upstream) ==")

	st, _, b := call(http.MethodGet, "/healthz", nil, nil)
	check("GET /healthz == 200 ok", st == 200 && strings.Contains(b, "ok"), b)

	usedBefore := serviceUsed()
	fmt.Printf("  serviceUsed(before) = %v\n", usedBefore)

	// 1. success -> head.errorCode 0, body.code 001, range 7, billed
	ec, bc, rng, raw := query(base(), nil)
	check("success: head.errorCode=0 body.code=001 range=7", ec == "0" && bc == "001" && rng == "7", raw)

	// 2. not found (伽马 1000) -> head 0, body.code 999, NOT billed
	nf := base()
	nf["mobile"] = "13800000000"
	ec, bc, _, raw = query(nf, nil)
	check("not-found: head.errorCode=0 body.code=999", ec == "0" && bc == "999", raw)

	// 3. bad signature -> head.errorCode 505002 (账号信息异常), no body
	ec, bc, _, raw = query(base(), map[string]any{"sign": "deadbeef"})
	check("bad-sign: head.errorCode=505002, no body", ec == "505002" && bc == "", raw)

	// 4. unknown appKey -> 505004 (账户信息不存在)
	{
		body := base()
		payload := map[string]any{"encryptionType": 1, "appKey": "nonexistent", "sign": sign(body, secret), "body": body}
		_, m, r := call(http.MethodPost, queryPath, payload, nil)
		ec = ""
		if h, ok := m["head"].(map[string]any); ok {
			ec, _ = h["errorCode"].(string)
		}
		check("unknown-appKey: head.errorCode=505004", ec == "505004", r)
	}

	// 5. missing appKey -> 505001 (appKey 异常)
	{
		body := base()
		payload := map[string]any{"encryptionType": 1, "appKey": "", "sign": sign(body, secret), "body": body}
		_, m, r := call(http.MethodPost, queryPath, payload, nil)
		ec = ""
		if h, ok := m["head"].(map[string]any); ok {
			ec, _ = h["errorCode"].(string)
		}
		check("missing-appKey: head.errorCode=505001", ec == "505001", r)
	}

	// 6. param invalid (bad mobile) -> 505062 (数据请求异常)
	bad := base()
	bad["mobile"] = "139xx"
	ec, _, _, raw = query(bad, nil)
	check("param-invalid: head.errorCode=505062", ec == "505062", raw)

	// 7. another success -> billed
	ec, bc, rng, raw = query(base(), nil)
	check("success#2: body.code=001 range=7", ec == "0" && bc == "001" && rng == "7", raw)

	usedAfter := serviceUsed()
	fmt.Printf("  serviceUsed(after) = %v\n", usedAfter)
	check("billing: 维度① only +2 (success-only)", usedAfter-usedBefore == 2,
		fmt.Sprintf("delta=%v (want 2)", usedAfter-usedBefore))

	// 8. admin: login + audits
	st, m, r := call(http.MethodPost, "/admin/api/login",
		map[string]string{"username": adminUser, "password": adminPass}, nil)
	token, _ := m["token"].(string)
	check("admin login -> token", st == 200 && token != "", r)

	if token != "" {
		auth := map[string]string{"Authorization": "Bearer " + token}
		_, am, ar := call(http.MethodGet, "/admin/api/audits?appKey="+appKey+"&limit=200", nil, auth)
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
		check("audit: records present", len(audits) > 0, ar)
		check("audit: has busiCode=10 (查得数据)", seen10, "")
		check("audit: has busiCode=1000 (查无)", seen1000, "")
		check("audit: PII masked", masked, "")
		stN, _, _ := call(http.MethodGet, "/admin/api/audits", nil, nil)
		check("admin audits without token -> 401", stN == 401, fmt.Sprintf("status=%d", stN))
	}

	fmt.Printf("\n== Result: %d passed, %d failed ==\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
