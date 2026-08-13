//go:build ignore

// Mock 收入A_g版 (grgjj / incomeag) upstream for full-link testing. Serves the
// JSON POST contract on /yrzx/common/v2/credit/v2. Run: go run scripts/mock_incomeag.go
//
// 协议对齐 docs/收入A_g版--ShowDoc.html + 官方 demo (ThreeDesUtil.java)：
//   - 请求体 {account,type,data,reqid,verify}；data=Base64(3DES/ECB/PKCS5(明文JSON))。
//   - verify=MD5(account + 加密前JSON串 + reqid + type + key).toUpperCase()。
//   - 响应 result=Base64(3DES(业务结果JSON))，用同一 3DES 密钥解密。
// 场景（用解密出的 mobile 驱动，与其它 mock 的 13800000000 查无惯例一致）：
//   - account 不匹配            -> code 002 (账号不存在)
//   - verify 不匹配             -> code 013 (校验签名错误)
//   - mobile == 13800000000     -> code 999 (无结果返回)
//   - otherwise                 -> code 001 + result{cbjfzt,jfjs,jfsj}
package main

import (
	"crypto/des" //nolint:gosec
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func md5upper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func pkcs5Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("空明文")
	}
	pad := int(b[len(b)-1])
	if pad <= 0 || pad > 8 || pad > len(b) {
		return nil, fmt.Errorf("非法填充 %d", pad)
	}
	return b[:len(b)-pad], nil
}

func pkcs5Pad(b []byte) []byte {
	pad := 8 - len(b)%8
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func des3DecryptBase64(cipherB64 string, key []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return nil, err
	}
	block, err := des.NewTripleDESCipher(key) //nolint:gosec
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw)%8 != 0 {
		return nil, fmt.Errorf("密文长度 %d 非法", len(raw))
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 8 {
		block.Decrypt(out[i:i+8], raw[i:i+8])
	}
	return pkcs5Unpad(out)
}

func des3EncryptBase64(plain []byte, key []byte) (string, error) {
	block, err := des.NewTripleDESCipher(key) //nolint:gosec
	if err != nil {
		return "", err
	}
	padded := pkcs5Pad(plain)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += 8 {
		block.Encrypt(out[i:i+8], padded[i:i+8])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func main() {
	addr := env("MOCK_INCOMEAG_ADDR", ":9123")
	account := env("GRGJJ_ACCOUNT", "demo-grgjj-account")
	signKey := env("GRGJJ_KEY", "demo-grgjj-key")
	// 3DES 密钥：Base64 编码，解码后须为 24 字节 DESede 密钥（默认取 24 个 ASCII 字符）。
	keyB64 := env("GRGJJ_3DES_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdefghijklmn")))
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 24 {
		log.Fatalf("GRGJJ_3DES_KEY 非法：需 Base64(24 字节)，得 %d 字节 (err=%v)", len(key), err)
	}

	http.HandleFunc("/yrzx/common/v2/credit/v2", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Account string `json:"account"`
			Type    string `json:"type"`
			Data    string `json:"data"`
			Reqid   string `json:"reqid"`
			Verify  string `json:"verify"`
		}
		_ = json.Unmarshal(raw, &req)

		resp := map[string]any{"uid": "grgjj-mock-" + req.Reqid, "reqid": req.Reqid}

		if req.Account != account {
			resp["code"], resp["msg"] = "002", "账号不存在"
			writeJSON(w, resp)
			return
		}
		plain, derr := des3DecryptBase64(req.Data, key)
		if derr != nil {
			resp["code"], resp["msg"] = "020", "参数为空或格式错误"
			writeJSON(w, resp)
			return
		}
		// verify = MD5(account + 加密前JSON串 + reqid + type + key).toUpperCase()
		want := md5upper(req.Account + string(plain) + req.Reqid + req.Type + signKey)
		if !strings.EqualFold(req.Verify, want) {
			resp["code"], resp["msg"] = "013", "校验签名错误"
			writeJSON(w, resp)
			return
		}
		var d struct {
			Name   string `json:"name"`
			Cid    string `json:"cid"`
			Mobile string `json:"mobile"`
		}
		_ = json.Unmarshal(plain, &d)

		switch {
		case d.Mobile == "13800000000":
			resp["code"], resp["msg"] = "999", "无结果返回"
		default:
			resp["code"], resp["msg"] = "001", "成功"
			result, _ := json.Marshal(map[string]string{
				"cbjfzt": "1",      // 缴费状态：正常
				"jfjs":   "7",      // 缴费基数（对应收入A_g版字典评分）
				"jfsj":   "202601", // 缴费时间
			})
			enc, eerr := des3EncryptBase64(result, key)
			if eerr != nil {
				resp["code"], resp["msg"] = "012", "接口错误，请联系提供商"
			} else {
				resp["result"] = enc
			}
		}
		log.Printf("incomeag <- reqid=%s mobile=%s -> code=%v", req.Reqid, d.Mobile, resp["code"])
		writeJSON(w, resp)
	})

	fmt.Printf("mock 收入A_g版 upstream listening on %s (/yrzx/common/v2/credit/v2)\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
