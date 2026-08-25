//go:build ignore

// Mock 备用公积金源 (grgjj 的备源 / bgjj / jeoho) upstream for full-link testing.
// Serves the JSON POST contract on /api/nlv2/zl4. Run: go run scripts/mock_bgjj.go
//
// 协议对齐 docs/备用公积金1/ 官方 demo (SignUtil/TestDemo)：
//   - 请求体 {merchant_id, timestamp, dsorderid, params(明文对象), sign}；
//   - sign=MD5("k1=v1&…&params={name=.., idcard=.., mobile=..}&…&key=merchantKey")，
//     顶层键 ASCII 升序、剔空值与 sign，params 段为 Java map toString 形态；
//   - 响应 {code, message, data, orderid, dsorderid}。
//
// 场景 (用 params.mobile 驱动，与其它 mock 的查无惯例一致)：
//   - merchant_id 不匹配   -> code 401 (商户不存在)
//   - sign 不匹配          -> code 203 (签名错误)
//   - mobile == 13800000000 -> code 201 (查无记录)
//   - mobile == 13600000000 -> code 301 (非白名单IP，模拟上游侧错误)
//   - otherwise            -> code 100 (查询成功) + data{date,score,jfzt}
//
// 另提供 GET /__count 返回累计业务查询次数 (供"命中即停：备源零调用"断言)。
package main

import (
	"bytes"
	"crypto/md5" //nolint:gosec
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// orderedPair 保留 params 对象的 JSON 键序 (加签依赖 Java map toString 的键序)。
type orderedPair struct{ k, v string }

// decodeOrderedObject 按出现顺序解析一个扁平 JSON 对象 (值均为字符串)。
func decodeOrderedObject(raw json.RawMessage) ([]orderedPair, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("params 不是对象")
	}
	var out []orderedPair
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, orderedPair{k: key, v: fmt.Sprintf("%v", val)})
	}
	return out, nil
}

func javaMapString(pairs []orderedPair) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	b.WriteByte('}')
	return b.String()
}

func main() {
	addr := env("MOCK_BGJJ_ADDR", ":9125")
	merchantID := env("BGJJ_MERCHANT_ID", "0000000000005077")
	merchantKey := env("BGJJ_MERCHANT_KEY", "P8rT2wXyZ9aBcDeFgHiJkLmNoPqRsTuV")

	var count int64

	http.HandleFunc("/__count", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%d", atomic.LoadInt64(&count))
	})

	http.HandleFunc("/api/nlv2/zl4", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		raw, _ := io.ReadAll(r.Body)

		var req struct {
			MerchantID string          `json:"merchant_id"`
			Timestamp  json.Number     `json:"timestamp"`
			DsOrderID  string          `json:"dsorderid"`
			Params     json.RawMessage `json:"params"`
			Sign       string          `json:"sign"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&req)

		resp := map[string]any{"orderid": "bgjj-mock-" + req.DsOrderID, "dsorderid": req.DsOrderID}

		if req.MerchantID != merchantID {
			resp["code"], resp["message"] = "401", "商户不存在"
			writeJSON(w, resp)
			return
		}

		pairs, perr := decodeOrderedObject(req.Params)
		if perr != nil {
			resp["code"], resp["message"] = "202", "参数格式错误"
			writeJSON(w, resp)
			return
		}

		// 重算 sign：顶层键 dsorderid/merchant_id/params/timestamp 升序拼接。
		top := map[string]string{
			"dsorderid":   req.DsOrderID,
			"merchant_id": req.MerchantID,
			"params":      javaMapString(pairs),
			"timestamp":   req.Timestamp.String(),
		}
		keys := make([]string, 0, len(top))
		for k := range top {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			if top[k] == "" {
				continue
			}
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(top[k])
			sb.WriteByte('&')
		}
		sb.WriteString("key=")
		sb.WriteString(merchantKey)
		want := md5hex(sb.String())
		if !strings.EqualFold(req.Sign, want) {
			resp["code"], resp["message"] = "203", "签名错误"
			log.Printf("bgjj sign mismatch got=%s want=%s src=%s", req.Sign, want, sb.String())
			writeJSON(w, resp)
			return
		}

		var mobile string
		for _, p := range pairs {
			if p.k == "mobile" {
				mobile = p.v
			}
		}

		switch mobile {
		case "13800000000":
			resp["code"], resp["message"], resp["data"] = "201", "查无记录", map[string]string{}
		case "13600000000":
			resp["code"], resp["message"] = "301", "非白名单IP"
		default:
			resp["code"], resp["message"] = "100", "查询成功"
			resp["data"] = map[string]string{
				"date":  "202606", // → jfsj
				"score": "13",     // → jfjs (缴存基数评分)
				"jfzt":  "1",      // → cbjfzt (缴费状态正常)
			}
		}
		log.Printf("bgjj <- dsorderid=%s mobile=%s -> code=%v", req.DsOrderID, mobile, resp["code"])
		writeJSON(w, resp)
	})

	fmt.Printf("mock 备用公积金源 (bgjj) listening on %s (/api/nlv2/zl4 + /__count)\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
