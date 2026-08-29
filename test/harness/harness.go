// Package harness provides shared helpers for the DataHub fixed test suite under
// test/cases/*.go. 各版本 (x1/v9/v8/zlf/blk) 对外接口完全一致 (x1 信封格式:
// 小写 sorted-body MD5 加签)，仅靠路由名区分。It centralizes the x1 signing scheme,
// an HTTP client against the running relay, version-scoped admin helpers, and the
// result recorder that each case writes to $RESULT_DIR/<suite>.json.
package harness

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
	"strconv"
	"strings"
	"time"
)

// Primary test client credentials: 每个「域」的存储各自播种一个独立的 demo license
// (memory seedDemo / postgres SeedDemo)，appKey 按域各不相同 (model.DemoAppKey)，
// secret 相同。v8 与 v9 同属 v8v9 域，共用同一个 demo appKey（license 共享，但
// 统计/日志按路由独立）；任何域的 demo appKey 在其它域的路由上都会鉴权失败 (505004)。
const (
	AppKey    = "y89098io" // x1 域的 demo appKey（QueryX1 等 x1 用例向后兼容）
	Secret    = "demo-app-secret"
	AdminUser = "admin"
	AdminPass = "admin12345"
)

// Versions is the ordered list of service versions under test.
var Versions = []string{"x1", "v9", "v8", "zlf", "blk", "rlbd1", "rlbd2", "sfzhy", "xfjy", "tsfx", "lxf", "grgjj", "grsb", "sfsm"}

// demoAppKeys mirrors model.DemoAppKey：按域独立的 demo appKey（v8/v9 共用）。
var demoAppKeys = map[string]string{
	"x1":    "y89098io",
	"v9":    "y890v8v9",
	"v8":    "y890v8v9",
	"zlf":   "y8909zlf",
	"blk":   "y8909blk",
	"rlbd1": "y89rlbd1",
	"rlbd2": "y89rlbd2",
	"sfzhy": "y89sfzhy",
	"xfjy":  "y890xfjy",
	"tsfx":  "y89tsfx",
	"lxf":   "y8909lxf",
	"grgjj": "y89grgjj",
	"grsb":  "y890grsb",
	"sfsm":  "y890sfsm",
}

// AppKeyFor returns the demo appKey seeded for the given route's 域.
func AppKeyFor(version string) string {
	if k, ok := demoAppKeys[version]; ok {
		return k
	}
	return "demo-" + version
}

// QueryPath returns the public query route for a version (统一 x1 信封, POST)。
func QueryPath(version string) string {
	return "/v1/openapi/zlx/querySrmx" + strings.ToUpper(version)
}

// QuotaPath returns the per-version quota route (GET, 同主接口鉴权)。
func QuotaPath(version string) string {
	return "/v1/openapi/zlx/quota" + strings.ToUpper(version)
}

// AdminBase returns the version-scoped admin API prefix (/admin/api/{ver})。
func AdminBase(version string) string {
	return "/admin/api/" + version
}

// BaseURL is the relay address (override via RELAY_BASE_URL).
func BaseURL() string {
	if v := os.Getenv("RELAY_BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

// SignX1 builds the x1 client signature: body 非空业务参数按键 ASCII 升序拼接
// (name+value)…，末尾追加 secret，再 MD5 小写 hex（appKey/sign/encryptionType 不参与）。
func SignX1(params map[string]string, secret string) string {
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
	sb.WriteString(secret)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// Call issues an HTTP request and returns (status, decoded-json-map, raw-body).
func Call(method, path string, body any, headers map[string]string) (int, map[string]any, string) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, BaseURL()+path, rdr)
	if err != nil {
		return 0, nil, err.Error()
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

// X1Result is a parsed query response (统一 x1 信封, 三版本通用)。
type X1Result struct {
	HTTPStatus int
	ErrorCode  string // head.errorCode
	LogID      string // head.logId (= requestId，可据此在管理端审计里定位本次请求)
	LatencyMs  float64
	BodyCode   string // body.code (001/999)
	Range      string // body.result.range
	// UID 是上游流水号。自然月缓存命中时它保持**首次回源**的原值（对账用），故
	// uid 不变而 Reqid 变 = 一次缓存回放（mock_income 的 uid 含 reqid，可直接观测）。
	UID string
	// Reqid 是 body.reqid，每次请求都应唯一（缓存命中也换新值）。
	Reqid string
	Raw   string
}

// Query builds the信封, signs the body, optionally overrides envelope fields
// (e.g. {"sign":"bad"} or {"appKey":""}), POSTs to the given version's route,
// and returns the parsed response.
func Query(version, appKey, secret string, body map[string]string, overrides map[string]any) X1Result {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           SignX1(body, secret),
		"body":           body,
	}
	for k, v := range overrides {
		payload[k] = v
	}
	st, m, raw := Call(http.MethodPost, QueryPath(version), payload, nil)
	r := X1Result{HTTPStatus: st, Raw: raw}
	if head, ok := m["head"].(map[string]any); ok {
		r.ErrorCode, _ = head["errorCode"].(string)
		r.LogID, _ = head["logId"].(string)
		r.LatencyMs, _ = head["time"].(float64)
	}
	if b, ok := m["body"].(map[string]any); ok {
		r.BodyCode, _ = b["code"].(string)
		r.UID, _ = b["uid"].(string)
		r.Reqid, _ = b["reqid"].(string)
		if res, ok := b["result"].(map[string]any); ok {
			r.Range, _ = res["range"].(string)
		}
	}
	return r
}

// QueryX1 is a convenience wrapper for the x1 version (backwards-compatible).
func QueryX1(appKey, secret string, body map[string]string, overrides map[string]any) X1Result {
	return Query("x1", appKey, secret, body, overrides)
}

// ServiceUsed reads the cumulative 成功查得数 via the version's /quota route.
// Returns -1 when the field is absent (error path).
func ServiceUsed(version, appKey, secret string) float64 {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           SignX1(map[string]string{}, secret),
		"body":           map[string]string{},
	}
	_, m, _ := Call(http.MethodGet, QuotaPath(version), payload, nil)
	if u, ok := m["serviceUsed"].(float64); ok {
		return u
	}
	return -1
}

// TotalCalls reads the cumulative 调用上游次数 via the version's /quota route.
// Returns -1 when the field is absent (error path). 计数按路由独立。
func TotalCalls(version, appKey, secret string) float64 {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           SignX1(map[string]string{}, secret),
		"body":           map[string]string{},
	}
	_, m, _ := Call(http.MethodGet, QuotaPath(version), payload, nil)
	if u, ok := m["totalCalls"].(float64); ok {
		return u
	}
	return -1
}

// AdminLogin returns a bearer token for the bootstrap admin (empty on failure).
func AdminLogin() (string, string) {
	st, m, raw := Call(http.MethodPost, "/admin/api/login",
		map[string]string{"username": AdminUser, "password": AdminPass}, nil)
	if st != 200 {
		return "", raw
	}
	tok, _ := m["token"].(string)
	return tok, raw
}

// AuthHeader builds the bearer auth header map.
func AuthHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// SettleWait 等异步记账落地（结算 + 审计 + 写结果缓存都在响应写回后由 Bookkeeper
// 的 worker 完成）。断言 /quota 计数、审计行或缓存命中前必须先等一等，否则读到的是
// 尚未落库的中间态。可用 SETTLE_WAIT_MS 调大（慢库/跨地域时）。
func SettleWait() {
	ms := 500
	if v := os.Getenv("SETTLE_WAIT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ms = n
		}
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// AuditByRequestID 在某路由的管理端审计列表里按 requestId (= head.logId) 找到对应
// 行。审计过滤器不支持按 requestId 查，故拉一页再扫。未找到返回 (nil, raw)。
func AuditByRequestID(version, token, requestID string) (map[string]any, string) {
	_, m, raw := Call(http.MethodGet,
		AdminBase(version)+"/audits?limit=200", nil, AuthHeader(token))
	rows, _ := m["audits"].([]any)
	for _, row := range rows {
		r, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := r["requestId"].(string); id == requestID {
			return r, raw
		}
	}
	return nil, raw
}

// AuditFlag 读审计行上的布尔列 (fromCache / calledUpstream / billed / foundData)。
func AuditFlag(row map[string]any, field string) bool {
	if row == nil {
		return false
	}
	v, _ := row[field].(bool)
	return v
}

// UniqueIdentity 造一个本次运行独一无二的个人三要素，保证首查必然是缓存未命中
// （自然月缓存按 name+idCard+mobile 归一后取指纹，复用旧身份会命中上一次的条目）。
// mobile 可指定：传 "" 用随机号；传 13800000000 可命中各 mock 的「查无」分支。
func UniqueIdentity(namePrefix, mobile string) map[string]string {
	n := time.Now().UnixNano()
	if mobile == "" {
		// 138 后 8 位取纳秒时间戳低位，避开 mock 的查无专用号 13800000000。
		mobile = "138" + fmt.Sprintf("%08d", n%100000000)
		if mobile == "13800000000" {
			mobile = "13800000001"
		}
	}
	// 身份证前 17 位数字 + 校验位占位 X（网关只校验格式 ^\d{17}[\dX]$）。
	idCard := fmt.Sprintf("3301291991%07dX", n%10000000)
	return map[string]string{
		"name":   namePrefix + strconv.FormatInt(n%1000000, 10),
		"idCard": idCard,
		"mobile": mobile,
	}
}

// ShortReqid builds a unique reqid (≤20 chars) for idempotency-sensitive cases.
func ShortReqid(prefix string) string {
	r := prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(r) > 20 {
		r = r[len(r)-20:]
	}
	return r
}
