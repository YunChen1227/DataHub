//go:build ignore

// Mock 证通 entcreditapi 聚合平台 implementing /ectcispserver/api/entcreditapi/query
// for swfp full-link testing. Run: go run scripts/mock_entcredit.go
//
// 严格复刻真实协议 (docs/java-api-demo)：application/x-www-form-urlencoded 表单，
// HMAC-SHA256 签名（SignedRequestsHelper.java），args/signature 双重 URLEncode。
// 按 entInfo（统一社会信用代码）驱动场景（与单上游 mock 用 13800000000 触发查无
// 同一惯例）：
//   - 92500233MA60R5KW8M → 四产品全部查得 (resultCode=00000, Status=4)
//   - 91110000EMPTYEMPT0 → 四产品全部查无 (resultCode=00000, Status=1)
//   - 91110000PARTFA0001 → P0130083 返回错误，其余查得（下游聚合为 002）
//   - 验签失败 / 版本号缺失 → 对应文档附录错误码 (E1010 / E1005)
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	orgCode         = env("ENTCREDIT_ORG_CODE", "0100600007")
	accessKeyID     = env("ENTCREDIT_ACCESS_KEY_ID", "demo-swfp-ak")
	secretAccessKey = env("ENTCREDIT_SECRET_ACCESS_KEY", "ZGVtby1zd2ZwLXNrLTMyLWJ5dGVzLWxvbmctc2VjcmV0")
	// endpoint 必须与客户端 config 里的 baseURL 完全一致（参与签名拼接），
	// 与 config.local.mem.yaml 的 versions.swfp.upstream.baseURL 保持同步。
	endpoint = env("ENTCREDIT_ENDPOINT", "http://localhost:9116")
	addr     = env("ENTCREDIT_ADDR", ":9116")
)

const (
	entInfoNormal  = "92500233MA60R5KW8M"
	entInfoEmpty   = "91110000EMPTYEMPT0"
	entInfoPartial = "91110000PARTFA0001"

	requestURI = "/ectcispserver/api/entcreditapi/query"
)

// sampleData 按产品码返回一段明细样例（结构参照四份 PDF 解码后的字段风格）。
func sampleData(prodCode string) map[string]any {
	switch prodCode {
	case "P0130081":
		return map[string]any{"nsrfpxx": map[string]any{"khxsdqList": []map[string]string{
			{"ljse": "44.09", "kpqj": "2025-04-30", "nsrsbh": entInfoNormal, "jyje": "4452.61"},
		}}}
	case "P0130083":
		return map[string]any{"nsrfpxx": map[string]any{"syhzxxList": []map[string]string{
			{"ljkpcs": "1", "kpqj": "2025-05-31", "nsrsbh": entInfoNormal, "ljkpjebhs": "172.28"},
		}}}
	case "P0130082":
		return map[string]any{"nsrswxx": map[string]any{"sbsjList": []map[string]string{
			{"sssjq": "2026-01-01", "nsrsbh": entInfoNormal, "ynse": "1.71", "ybtse": "1.71"},
		}}}
	default: // P0130084
		return map[string]any{"nsrswxx": map[string]any{"jksjList": []map[string]string{
			{"sssjq": "2025-10-01", "nsrsbh": entInfoNormal, "bys": "19066.99"},
		}}}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// sign 复刻 SignedRequestsHelper.sign()：HMAC-SHA256(toSign, base64decode(sk))，
// 结果 base64 编码后做一次 URLEncode（服务端校验时对收到的 signature 做同样比较，
// 故这里对"已被表单解码一次"的 signature 值，需要先 QueryUnescape 抵消双重编码
// 中的第二层，再与自算的一次编码结果比较）。
func sign(endpoint, uri, version, msgID, org, ak, timestamp, args string) string {
	toSign := strings.Join([]string{
		http.MethodPost, endpoint, uri, version, msgID, org, ak, timestamp, args,
	}, "\n")
	keyBytes, _ := base64.StdEncoding.DecodeString(secretAccessKey)
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(toSign))
	b64 := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return url.QueryEscape(b64)
}

func main() {
	http.HandleFunc(requestURI, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeJSON(w, map[string]any{"resultCode": "E1000", "resultDesc": "查询参数校验不通过"})
			return
		}
		version := r.FormValue("version")
		msgID := r.FormValue("msgId")
		org := r.FormValue("orgCode")
		ak := r.FormValue("accessKeyId")
		timestamp := r.FormValue("timestamp")
		// args/signature 在真实协议里被双重 URLEncode：Go 的 r.ParseForm 已经解码了
		// "表单层"那一次，此处拿到的是仍带一层 URLEncode 的原始字符串（与客户端
		// callProduct 里 url.QueryEscape 后再交给 form.Encode 的结果对应）。
		argsEscaped := r.FormValue("args")
		sigEscaped := r.FormValue("signature")

		if version == "" {
			writeJSON(w, map[string]any{"resultCode": "E1005", "resultDesc": "版本号错误", "orderNo": msgID})
			return
		}
		if msgID == "" {
			writeJSON(w, map[string]any{"resultCode": "E1006", "resultDesc": "MSGID错误", "orderNo": msgID})
			return
		}

		args, err := url.QueryUnescape(argsEscaped)
		if err != nil {
			writeJSON(w, map[string]any{"resultCode": "E1000", "resultDesc": "查询参数校验不通过", "orderNo": msgID})
			return
		}
		expectSig := sign(endpoint, requestURI, version, msgID, org, ak, timestamp, args)
		if ak != accessKeyID {
			writeJSON(w, map[string]any{"resultCode": "E1009", "resultDesc": "accessKeyId错误", "orderNo": msgID})
			return
		}
		if org != orgCode {
			writeJSON(w, map[string]any{"resultCode": "E1012", "resultDesc": "机构代码错误", "orderNo": msgID})
			return
		}
		if sigEscaped != expectSig {
			log.Printf("sign mismatch: got=%s want=%s toSign-args=%s", sigEscaped, expectSig, args)
			writeJSON(w, map[string]any{"resultCode": "E1010", "resultDesc": "signature错误", "orderNo": msgID})
			return
		}

		var argsMap struct {
			ProdCode string `json:"prodCode"`
			EntInfo  string `json:"entInfo"`
		}
		if err := json.Unmarshal([]byte(args), &argsMap); err != nil {
			writeJSON(w, map[string]any{"resultCode": "E1000", "resultDesc": "查询参数校验不通过", "orderNo": msgID})
			return
		}
		if argsMap.EntInfo == "" {
			writeJSON(w, map[string]any{"resultCode": "E1003", "resultDesc": "请提供查询条件", "orderNo": msgID})
			return
		}

		switch {
		case argsMap.EntInfo == entInfoEmpty:
			writeJSON(w, map[string]any{
				"orderNo":    msgID,
				"resultCode": "00000",
				"resultDesc": "成功",
				"resultData": map[string]any{argsMap.ProdCode + "Status": "1"},
			})
		case argsMap.EntInfo == entInfoPartial && argsMap.ProdCode == "P0130083":
			writeJSON(w, map[string]any{"resultCode": "E0400", "resultDesc": "查询征信数据出错", "orderNo": msgID})
		default:
			plain, _ := json.Marshal(sampleData(argsMap.ProdCode))
			writeJSON(w, map[string]any{
				"orderNo":    msgID,
				"resultCode": "00000",
				"resultDesc": "成功",
				"packetCnt":  1,
				"resultData": map[string]any{
					argsMap.ProdCode + "Status": "4",
					argsMap.ProdCode + "Data": map[string]any{
						"result": []map[string]string{{"data": base64.StdEncoding.EncodeToString(plain)}},
					},
				},
			})
		}
	})

	fmt.Printf("mock entcredit (证通 entcreditapi 聚合, 四产品) listening on %s  orgCode=%s accessKeyId=%s\n", addr, orgCode, accessKeyID)
	log.Fatal(http.ListenAndServe(addr, nil))
}
