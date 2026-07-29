//go:build ignore

// Mock 投诉分析识别名单 (kfongtech) upstream implementing POST / (JSON) for tsfx
// full-link testing. Run: go run scripts/mock_complaint.go
//
// 镜像 upstream/complaint.go 的加密/签名算法（对齐上游 demo docs/投诉分析识别/demo/demo1）：
//   - AES key/iv 由 appSecret 派生：key=MD5(secret)大写[8,24)，iv=MD5(key)大写[8,24)
//   - param = 小写 hex(AES/CBC/PKCS7(sortParam(业务参数), key, iv))
//   - sign  = 小写 MD5(appSecret + sortParam(业务参数 + apiKey))
// 路由：
//   - unknown apiKey / bad sign   -> code 1002 签名验证失败 (上游侧错误 -> 网关 505062)
//   - mobile == notFoundMobile    -> code 0000 / data 里 forbid=0 未命中 (调用成功即计费 -> 001)
//   - otherwise                   -> code 0000 / data 里 forbid=1 命中 (001)
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

func md5hexUpper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// deriveKeyIV 由 appSecret 派生 AES key/iv（各 16 字节），与 client deriveComplaintKeyIV 对称。
func deriveKeyIV(secret string) (key, iv string) {
	key = md5hexUpper(secret)[8:24]
	iv = md5hexUpper(key)[8:24]
	return key, iv
}

// aesCBCDecryptHex 解密 hex(AES/CBC/PKCS7) 密文，返回明文（与 client encryptParam 对称）。
func aesCBCDecryptHex(param, key, iv string) ([]byte, error) {
	ct, err := hex.DecodeString(param)
	if err != nil {
		return nil, fmt.Errorf("hex decode: %w", err)
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	bs := block.BlockSize()
	if len(ct) == 0 || len(ct)%bs != 0 {
		return nil, fmt.Errorf("bad ciphertext length %d", len(ct))
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, []byte(iv)).CryptBlocks(out, ct)
	// 去 PKCS5/PKCS7 填充。
	pad := int(out[len(out)-1])
	if pad <= 0 || pad > bs || pad > len(out) {
		return nil, fmt.Errorf("bad padding %d", pad)
	}
	return out[:len(out)-pad], nil
}

// sortParam 按 key ASCII 升序拼成 k1=v1&k2=v2&...，剔除空值与 "sign"，与 client sortComplaintParams 对称。
func sortParam(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}
	return sb.String()
}

// parseKV 解析 k1=v1&k2=v2&... 明文为 map（sortParam 的逆操作）。
func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, pair := range strings.Split(s, "&") {
		if pair == "" {
			continue
		}
		if i := strings.IndexByte(pair, '='); i >= 0 {
			m[pair[:i]] = pair[i+1:]
		}
	}
	return m
}

// gzipBase64 gzip 压缩后 base64 编码（与 client decodeComplaintData 对称）。
func gzipBase64(plain []byte) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func main() {
	addr := env("MOCK_COMPLAINT_ADDR", ":9120")
	apiKey := env("COMPLAINT_APIKEY", "demo-tsfx-apikey")
	signSecret := env("COMPLAINT_SIGN_SECRET", "demo-tsfx-sign") // = 上游 appSecret，派生 AES key/iv 并加签
	notFoundMobile := env("COMPLAINT_NOTFOUND_MOBILE", "13800000000")
	aesKey, aesIV := deriveKeyIV(signSecret)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			APIKey string `json:"apiKey"`
			Param  string `json:"param"`
			Sign   string `json:"sign"`
		}
		_ = json.Unmarshal(raw, &req)

		// param 用派生 key/iv 解密（sign 校验依赖解密出的业务参数，故先解密）。
		plain, err := aesCBCDecryptHex(req.Param, aesKey, aesIV)
		if err != nil {
			log.Printf("complaint <- param decrypt fail: %v -> 1013", err)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "1013", "msg": "参数异常", "token": "tsfx-mock-1013",
			})
			return
		}
		biz := parseKV(string(plain))

		// sign = MD5(appSecret + sortParam(业务参数 + apiKey)) 小写 hex。
		signParams := make(map[string]string, len(biz)+1)
		for k, v := range biz {
			signParams[k] = v
		}
		signParams["apiKey"] = req.APIKey
		wantSign := md5hex(signSecret + sortParam(signParams))
		if req.APIKey != apiKey || req.Sign != wantSign {
			log.Printf("complaint <- bad apiKey/sign apiKey=%s -> 1002", req.APIKey)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "1002", "msg": "签名验证失败", "token": "tsfx-mock-1002",
			})
			return
		}

		mobile := biz["mobile"]
		forbid := 1 // 命中
		if mobile == notFoundMobile {
			forbid = 0 // 未命中
		}
		records := []map[string]any{{"callee": md5hex(mobile), "forbid": forbid}}
		arr, _ := json.Marshal(records)

		log.Printf("complaint <- poly=%s mobile=%s -> code=0000 forbid=%d", biz["poly"], mobile, forbid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "0000",
			"msg":   "查询成功",
			"token": "tsfx-mock-0000",
			"data":  gzipBase64(arr),
		})
	})

	fmt.Printf("mock 投诉分析识别名单 (kfongtech) upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
