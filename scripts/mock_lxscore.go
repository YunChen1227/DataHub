//go:build ignore

// Mock 灵犀分 score_195_v1 (fullink) upstream implementing POST /report/encode
// for lxf full-link testing. Run: go run scripts/mock_lxscore.go
//
// 镜像 upstream/lxscore.go + descbc.go 的签名/加密算法（对齐
// docs/灵犀分-score_195_v1-接口文档.pdf）：
//   - sign = 大写 hex(DES/CBC/PKCS5(按参数名 ASCII 升序拼的 k=v&… 串, encryptKey))
//   - data = 同一套 DES 加密的 {"score_195_v1":"<分数>"}
//   - IV 取密钥本身
// 路由：
//   - unknown customerId/customerProdId  -> status 500 / 2031204 该商户信息不存在
//   - bad sign                           -> status 500 / 2031208 认证失败
//   - mobile MD5 == errorMobile 的 MD5    -> status 500 / 2031225 重复请求拒绝
//                                           (上游业务失败 -> 网关 505062，审计须带上游标识)
//   - mobile MD5 == notFoundMobile 的 MD5 -> status 200 / 分数 "-1" (查无 -> 999)
//   - otherwise                          -> status 200 / 分数 "600" (查得 -> 001)
package main

import (
	"bytes"
	"crypto/cipher"
	"crypto/des" //nolint:gosec // 上游契约指定 DES
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// desEncryptHex/desDecryptHex 与 upstream/descbc.go 对称：DES/CBC/PKCS5，大写 hex，IV=key。
func desEncryptHex(plain, key []byte) (string, error) {
	block, err := des.NewCipher(key) //nolint:gosec
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	pad := bs - len(plain)%bs
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key).CryptBlocks(out, padded)
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

// signStr 按文档 §2.2 参数表的固定字段顺序拼 k=v&k=v…（sign 自身不参与）。
// 与 upstream/lxscore.go 的 lxScoreSignStr 保持一致：真实上游按文档字段顺序验签，
// 用字母序会被判 2031208。
func signStr(params map[string]string) string {
	order := []string{
		"customerId", "customerProdId", "customerRequestId",
		"name", "mobile", "idCardNo", "timestamp",
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, "&")
}

func writeErr(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": 500, "internalErrorCode": code, "msg": msg, "data": nil,
	})
}

func main() {
	addr := env("MOCK_LXSCORE_ADDR", ":9122")
	customerID := env("LXSCORE_CUSTOMER_ID", "demo-lxf-customer")
	customerProdID := env("LXSCORE_CUSTOMER_PROD_ID", "demo-lxf-prod")
	encryptKey := env("LXSCORE_ENCRYPT_KEY", "lxfdemo1") // 8 字符 DES 密钥
	notFoundMobile := env("LXSCORE_NOTFOUND_MOBILE", "13800000000")
	errorMobile := env("LXSCORE_ERROR_MOBILE", "13700000000")
	score := env("LXSCORE_SCORE", "600")

	key := []byte(encryptKey)
	if len(key) != des.BlockSize {
		log.Fatalf("LXSCORE_ENCRYPT_KEY 必须是 8 个字符，当前 %d", len(key))
	}

	http.HandleFunc("/report/encode", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			CustomerID        string `json:"customerId"`
			CustomerProdID    string `json:"customerProdId"`
			CustomerRequestID string `json:"customerRequestId"`
			Name              string `json:"name"`
			Mobile            string `json:"mobile"`
			IDCardNo          string `json:"idCardNo"`
			Timestamp         int64  `json:"timestamp"`
			Sign              string `json:"sign"`
		}
		_ = json.Unmarshal(raw, &req)

		if req.CustomerID != customerID || req.CustomerProdID != customerProdID {
			log.Printf("lxscore <- unknown customerId=%s prodId=%s -> 2031204",
				req.CustomerID, req.CustomerProdID)
			writeErr(w, "2031204", "该商户信息不存在")
			return
		}

		want, err := desEncryptHex([]byte(signStr(map[string]string{
			"customerId":        req.CustomerID,
			"customerProdId":    req.CustomerProdID,
			"customerRequestId": req.CustomerRequestID,
			"name":              req.Name,
			"mobile":            req.Mobile,
			"idCardNo":          req.IDCardNo,
			"timestamp":         strconv.FormatInt(req.Timestamp, 10),
		})), key)
		if err != nil || !strings.EqualFold(req.Sign, want) {
			log.Printf("lxscore <- bad sign reqId=%s -> 2031208", req.CustomerRequestID)
			writeErr(w, "2031208", "认证失败, 请检查参数是否正确")
			return
		}

		// 上游收到的是 MD5 摘要，故用约定手机号的 MD5 比对。
		if req.Mobile == md5hex(errorMobile) {
			log.Printf("lxscore <- reqId=%s -> 2031225", req.CustomerRequestID)
			writeErr(w, "2031225", "重复请求 : "+req.CustomerRequestID)
			return
		}
		out := score
		if req.Mobile == md5hex(notFoundMobile) {
			out = "-1"
		}
		plain, _ := json.Marshal(map[string]string{"score_195_v1": out})
		data, err := desEncryptHex(plain, key)
		if err != nil {
			writeErr(w, "9031001", "系统异常")
			return
		}

		log.Printf("lxscore <- reqId=%s -> status=200 score=%s", req.CustomerRequestID, out)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 200, "internalErrorCode": "0", "msg": "success", "data": data,
		})
	})

	fmt.Printf("mock 灵犀分 score_195_v1 (fullink) upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
