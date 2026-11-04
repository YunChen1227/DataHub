//go:build ignore

// Mock 申请反欺诈-定制模型 upstream (阿里云 SAF ExecuteRequest + Service=
// anti_fraud_customed) for fqz full-link testing. Run: go run scripts/mock_antifraud.go
//
// Verifies the POP RPC HMAC-SHA1 signature (secret+"&") over the form params, then routes:
//   - bad signature                     -> HTTP 400 + Code "SignatureDoesNotMatch" (POP 网关错误)
//   - Action/Service mismatch           -> Code 404 Service 不合法
//   - ServiceParameters 缺 mobile/scene -> Code 400 ServiceParameters 不合法 (兜底暴露漏拦)
//   - mobile == forbiddenMobile         -> Code 403 权限不足 (上游侧错误, 不计费)
//   - mobile == notFoundMobile          -> Code 200 + score "-1" 未查得 (**查无也计费**)
//   - otherwise                         -> Code 200 + score "258"
//
// §3.2 手机号三选一 (mobile / mobileMd5 / mobileSha256) 全部接受：取到哪个用哪个，特殊号
// 的匹配按同样的摘要口径比对，故 fqz 配 encryptionType 1/2/3 任一都能跑通全链路。
package main

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func popEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

// encode 把明文手机号按上游入参形态 (plain/md5/sha256) 归一，供特殊号比对。
func encode(mobile, enc string) string {
	switch enc {
	case "md5":
		sum := md5.Sum([]byte(mobile))
		return hex.EncodeToString(sum[:])
	case "sha256":
		sum := sha256.Sum256([]byte(mobile))
		return hex.EncodeToString(sum[:])
	default:
		return mobile
	}
}

func sign(method string, form url.Values, secret string) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		if k != "Signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, popEncode(k)+"="+popEncode(form.Get(k)))
	}
	toSign := method + "&" + popEncode("/") + "&" + popEncode(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func main() {
	addr := env("MOCK_ANTIFRAUD_ADDR", ":9133")
	secret := env("FQZ_ACCESS_KEY_SECRET", "demo-fqz-secret")
	notFound := env("FQZ_NOTFOUND_MOBILE", "13800000000")
	forbidden := env("FQZ_FORBIDDEN_MOBILE", "13800000403")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form := r.PostForm
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		reply := func(status int, resp map[string]any) {
			resp["RequestId"] = "fqz-mock-001"
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		}

		var sp map[string]string
		_ = json.Unmarshal([]byte(form.Get("ServiceParameters")), &sp)
		// 三选一：取到哪个键用哪个，并记下形态以便按同口径比对特殊号。
		mobile, enc := sp["mobile"], "plain"
		if mobile == "" {
			if v := sp["mobileMd5"]; v != "" {
				mobile, enc = v, "md5"
			} else if v := sp["mobileSha256"]; v != "" {
				mobile, enc = v, "sha256"
			}
		}
		log.Printf("antifraud <- service=%s mobileField=%s mobile=%s scene=%s", form.Get("Service"), enc, mobile, sp["scene"])

		switch {
		case r.Method != http.MethodPost || sign(r.Method, form, secret) != form.Get("Signature"):
			reply(http.StatusBadRequest, map[string]any{"Code": "SignatureDoesNotMatch", "Message": "Specified signature is not matched with our calculation."})
		case form.Get("Action") != "ExecuteRequest" || form.Get("Service") != "anti_fraud_customed":
			reply(http.StatusOK, map[string]any{"Code": 404, "Message": "Service 不合法"})
		case mobile == "" || sp["scene"] == "":
			reply(http.StatusOK, map[string]any{"Code": 400, "Message": "ServiceParameters 不合法"})
		case mobile == encode(forbidden, enc):
			reply(http.StatusOK, map[string]any{"Code": 403, "Message": "权限不足"})
		case mobile == encode(notFound, enc):
			reply(http.StatusOK, map[string]any{"Code": 200, "Message": "OK", "Data": map[string]any{"score": "-1"}})
		default:
			reply(http.StatusOK, map[string]any{"Code": 200, "Message": "OK", "Data": map[string]any{"score": "258"}})
		}
	})

	fmt.Printf("mock 申请反欺诈-定制模型 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
