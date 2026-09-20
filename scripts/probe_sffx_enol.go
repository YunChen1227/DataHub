//go:build ignore

// probe_sffx_enol: 直连应诺尔 enol「身份风险 V107」doCheck，验证 appId/appSecret 与加签。
// 在阿里云 ECS 上跑即可（出口 IP 须已在上游加白）。
//
// 用法:
//   go run ./scripts/probe_sffx_enol.go
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 生产环境（身份风险V107 文档 §3.2；与 x1/gama、blk 共用应诺尔账号）
const (
	enolURL    = "https://api.enolfax.com/enol/api/v1/doCheck"
	enolAppID  = "3QhkJCiC"
	enolSecret = "a44ff24166206291f95e214a5f9f9aa2c7a5616d"
	apiKey     = "idRiskTagV107"
	encType    = 2 // name/idCard 走 MD5 摘要

	queryName   = "陈韫"
	queryIDCard = "440303200002163115"
)

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// signGama: body 非空参数按 key ASCII 升序拼「键值」+ secret → MD5 小写 hex。
func signGama(params map[string]string, secret string) string {
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
	return md5Hex(sb.String())
}

func encodePII(v string, encryptionType int) string {
	if encryptionType != 2 {
		return v
	}
	return md5Hex(v)
}

func call(url, appID, secret, apiKey string, encType int, name, idCard, label string) {
	body := map[string]string{}
	if name != "" {
		body["name"] = encodePII(name, encType)
	}
	if idCard != "" {
		body["idCard"] = encodePII(idCard, encType)
	}
	env := map[string]any{
		"encryptionType": encType,
		"appId":          appID,
		"sign":           signGama(body, secret),
		"apiKey":         apiKey,
		"body":           body,
	}
	payload, _ := json.Marshal(env)

	fmt.Printf("\n== [%s] ==\n", label)
	fmt.Printf("POST %s\n", url)
	fmt.Printf("encryptionType=%d apiKey=%s\n", encType, apiKey)
	fmt.Printf("body(明文): name=%q idCard=%q\n", name, idCard)
	fmt.Printf("body(实际上游): %s\n", mustJSON(body))
	fmt.Printf("sign=%s\n", env["sign"])

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Printf("build request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	t0 := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(t0)
	if err != nil {
		fmt.Printf("HTTP error (%v): %v\n", elapsed, err)
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("HTTP %d (%v)\n", resp.StatusCode, elapsed)
	fmt.Printf("响应: %s\n", prettyJSON(raw))

	var parsed struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		SeqNo string `json:"seqNo"`
		Data  struct {
			BusiCode int             `json:"busiCode"`
			BusiMsg  string          `json:"busiMsg"`
			Result   json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		switch {
		case parsed.Code != 0:
			fmt.Printf("解读: 全局 code=%d → 响应异常\n", parsed.Code)
		case parsed.Data.BusiCode == 10:
			fmt.Printf("解读: busiCode=10 → 查得（含无风险 A0）\n")
		case parsed.Data.BusiCode == 1000:
			fmt.Printf("解读: busiCode=1000 → 数据未查得\n")
		case parsed.Data.BusiCode == 1001:
			fmt.Printf("解读: busiCode=1001 → 账户余额不足\n")
		case parsed.Data.BusiCode == 1005:
			fmt.Printf("解读: busiCode=1005 → 签名/账号异常（检查 appSecret 与 body 字段）\n")
		default:
			fmt.Printf("解读: busiCode=%d msg=%s\n", parsed.Data.BusiCode, parsed.Data.BusiMsg)
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func prettyJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

func main() {
	fmt.Println("== sffx 直连应诺尔 enol 上游探测（身份风险 V107）==")
	fmt.Printf("url=%s\n", enolURL)
	fmt.Printf("appId=%s\n", enolAppID)
	fmt.Printf("appSecret=%s...%s\n", enolSecret[:6], enolSecret[len(enolSecret)-4:])
	fmt.Printf("name=%s idCard=%s\n", queryName, queryIDCard)

	call(enolURL, enolAppID, enolSecret, apiKey, encType, queryName, queryIDCard, "主查")
}
