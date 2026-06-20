// Package harness provides shared helpers for the DataHub fixed test suite under
// test/cases/*.go. It centralizes the downstream signing schemes (x1 lowercase
// sorted-body MD5; v9 uppercase account+idCard+mobile+reqid+key MD5), an HTTP
// client against the running relay, admin login, and the structured result
// recorder that each case writes to $RESULT_DIR/<suite>.json.
package harness

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Primary test client credentials (admin-created license).
const (
	UserName       = "chenyun"
	AppKey         = "mqh8zjh26ra6"
	Secret         = "9c1f0fcdaab59fd08ae445d0379520d0"
	ClientPublicIP = "121.35.187.243" // per-user IP whitelist entry
	AdminUser      = "admin"
	AdminPass      = "admin12345"

	X1Path    = "/v1/openapi/zlx/querySrmxX1"
	QuotaPath = "/v1/openapi/zlx/quota"
	V9Path    = "/yrzx/finan/net/10w/v9"
)

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

// SignV9 builds the旧版 v9 request signature: MD5(account+idCard+mobile+reqid+key) 大写。
func SignV9(account, idCard, mobile, reqid, key string) string {
	sum := md5.Sum([]byte(account + idCard + mobile + reqid + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
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

// X1Result is a parsed x1 response.
type X1Result struct {
	HTTPStatus int
	ErrorCode  string // head.errorCode
	BodyCode   string // body.code (001/999)
	Range      string // body.result.range
	Raw        string
}

// QueryX1 builds the信封, signs the body, optionally overrides envelope fields
// (e.g. {"sign":"bad"} or {"appKey":""}), and returns the parsed response.
func QueryX1(appKey, secret string, body map[string]string, overrides map[string]any) X1Result {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           SignX1(body, secret),
		"body":           body,
	}
	for k, v := range overrides {
		payload[k] = v
	}
	st, m, raw := Call(http.MethodPost, X1Path, payload, nil)
	r := X1Result{HTTPStatus: st, Raw: raw}
	if head, ok := m["head"].(map[string]any); ok {
		r.ErrorCode, _ = head["errorCode"].(string)
	}
	if b, ok := m["body"].(map[string]any); ok {
		r.BodyCode, _ = b["code"].(string)
		if res, ok := b["result"].(map[string]any); ok {
			r.Range, _ = res["range"].(string)
		}
	}
	return r
}

// V9Result is a parsed v9 response.
type V9Result struct {
	HTTPStatus int
	Code       string
	Range      string
	Verify     string
	Raw        string
}

// QueryV9 issues GET /yrzx/finan/net/10w/v9 with the given params (verify is the
// caller-supplied signature so bad-signature cases can be exercised).
func QueryV9(account, idCard, name, mobile, reqid, verify string) V9Result {
	q := url.Values{}
	q.Set("account", account)
	q.Set("idCard", idCard)
	if name != "" {
		q.Set("name", name)
	}
	q.Set("mobile", mobile)
	q.Set("reqid", reqid)
	q.Set("verify", verify)
	st, m, raw := Call(http.MethodGet, V9Path+"?"+q.Encode(), nil, nil)
	r := V9Result{HTTPStatus: st, Raw: raw}
	r.Code, _ = m["code"].(string)
	r.Verify, _ = m["verify"].(string)
	if res, ok := m["result"].(map[string]any); ok {
		r.Range, _ = res["range"].(string)
	}
	return r
}

// ServiceUsed reads the cumulative 成功查得数 via /quota for the demo appKey.
// Returns -1 when the field is absent (error path).
func ServiceUsed(appKey, secret string) float64 {
	payload := map[string]any{
		"encryptionType": 1,
		"appKey":         appKey,
		"sign":           SignX1(map[string]string{}, secret),
		"body":           map[string]string{},
	}
	_, m, _ := Call(http.MethodGet, QuotaPath, payload, nil)
	if u, ok := m["serviceUsed"].(float64); ok {
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

// ShortReqid builds a unique v9 reqid (≤20 chars) for idempotency-sensitive cases.
func ShortReqid(prefix string) string {
	r := prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(r) > 20 {
		r = r[len(r)-20:]
	}
	return r
}
