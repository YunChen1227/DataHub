//go:build ignore

// 还原 grgjj 测试脚本触发后，relay 发给 yrzx 上游的两步请求（含中间变量）。
// go run ./scripts/trace_grgjj_upstream_request.go
package main

import (
	"context"
	"crypto/des" //nolint:gosec
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func main() {
	account := "QZ1H144FEu94GS35"
	signKey := "NO43H7l6R58c918B"
	baseURL := "https://api.yinrongzhixin.com:14443/yrzx/common/v2/credit/v2"

	name := "张三"
	cid := "330129199109094312"
	mobile := "13809091009"
	reqid := "dkntjfkhryjx1" // 最近一次阿里云审计 id=10 的 reqid

	fmt.Println("=== 说明 ===")
	fmt.Println("probe_grgjj_aliyun.go 只打到 DataHub relay (http://aiszcloud.cn:8080)。")
	fmt.Println("relay 收到后会自己调 yrzx 上游，完整流程如下：")
	fmt.Println()

	plain, _ := json.Marshal(map[string]string{
		"cid":    cid,
		"mobile": mobile,
		"name":   name,
	})
	plainStr := string(plain)

	// ── 第 1 步：获取密钥 ──
	skReqid := "sk" + reqid
	if len(skReqid) > 20 {
		skReqid = skReqid[len(skReqid)-20:]
	}
	secVerify := md5Upper(account + skReqid + signKey)
	secURL := fmt.Sprintf("https://api.yinrongzhixin.com:14443/yrzx/secKey/info?account=%s&reqid=%s&verify=%s",
		account, skReqid, secVerify)

	fmt.Println("【第 1 步】GET 获取密钥")
	fmt.Println("URL:", secURL)
	fmt.Println("签名: verify = MD5(account + reqid + key).toUpperCase()")
	fmt.Printf("  拼接串 = %q + %q + %q\n", account, skReqid, signKey)
	fmt.Printf("  verify = %s\n", secVerify)
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	key, secRaw, secHTTP, err := fetchSecKey(ctx, secURL)
	fmt.Printf("【第1步实际响应】HTTP %d body=%s\n\n", secHTTP, secRaw)

	queryVerify := md5Upper(account + plainStr + reqid + "1106" + signKey)

	fmt.Println("【第 2 步】POST 主查询")
	fmt.Println("URL:", baseURL)
	fmt.Println("Header: Content-Type: application/json;charset=utf-8")
	fmt.Println()
	fmt.Println("加密前业务 JSON（Go json.Marshal 按字段名排序）:")
	fmt.Println(" ", plainStr)
	fmt.Println()
	fmt.Println("verify = MD5(account + 加密前JSON + reqid + type + key).toUpperCase()")
	fmt.Printf("  拼接串 = %q + %q + %q + %q + %q\n", account, plainStr, reqid, "1106", signKey)
	fmt.Printf("  verify = %s\n", queryVerify)
	fmt.Println()
	fmt.Println("data = Base64( 3DES/ECB/PKCS5( 加密前JSON ) )")
	fmt.Println("  3DES 密钥 = 第1步 result.key 经 deriveSessionKey 解密/归一后的 24 字节")
	fmt.Println()

	if err != nil {
		fmt.Println("【取钥失败】", err)
		fmt.Println()
		fmt.Println("=== 最终 POST body 结构（data 无法填真实值）===")
		printBody(account, "1106", "<Base64(3DES(plain))>", reqid, queryVerify)
		return
	}

	fmt.Printf("【会话密钥】24字节 hex = %s\n\n", hex.EncodeToString(key))

	data, err := des3EncryptB64(plain, key)
	if err != nil {
		fmt.Println("【加密 data 失败】", err)
		return
	}

	fmt.Println("=== 最终发给上游的 POST body（完整 JSON）===")
	printBody(account, "1106", data, reqid, queryVerify)
}

func md5Upper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func printBody(account, typ, data, reqid, verify string) {
	body := map[string]string{
		"account": account,
		"type":    typ,
		"data":    data,
		"reqid":   reqid,
		"verify":  verify,
	}
	b, _ := json.MarshalIndent(body, "", "  ")
	fmt.Println(string(b))
}

func fetchSecKey(ctx context.Context, url string) ([]byte, string, int, error) {
	signKey := "NO43H7l6R58c918B"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", 0, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	var sr struct {
		Code   string `json:"code"`
		Msg    string `json:"msg"`
		Result struct {
			Key string `json:"key"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, body, resp.StatusCode, fmt.Errorf("decode: %w", err)
	}
	if sr.Code != "001" {
		return nil, body, resp.StatusCode, fmt.Errorf("code=%s msg=%s", sr.Code, sr.Msg)
	}
	key, err := deriveSessionKey(sr.Result.Key, signKey)
	return key, body, resp.StatusCode, err
}

func deriveSessionKey(resultKey, signKey string) ([]byte, error) {
	resultKey = strings.TrimSpace(resultKey)
	if mk, err := desedeExpand([]byte(signKey)); err == nil {
		if plain, derr := des3DecryptB64(resultKey, mk); derr == nil {
			if k, kerr := desedeExpand(plain); kerr == nil {
				return k, nil
			}
			if k, kerr := flexibleKey(strings.TrimSpace(string(plain))); kerr == nil {
				return k, nil
			}
		}
	}
	return flexibleKey(resultKey)
}

func desedeExpand(b []byte) ([]byte, error) {
	switch len(b) {
	case 24:
		return b, nil
	case 16:
		out := make([]byte, 24)
		copy(out, b)
		copy(out[16:], b[:8])
		return out, nil
	case 8:
		out := make([]byte, 24)
		copy(out, b)
		copy(out[8:], b)
		copy(out[16:], b)
		return out, nil
	default:
		return nil, fmt.Errorf("bad len %d", len(b))
	}
}

func flexibleKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == 24 {
		return []byte(s), nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 24 {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == 24 {
		return b, nil
	}
	return nil, fmt.Errorf("cannot normalize key len=%d", len(s))
}

func des3EncryptB64(plain, key []byte) (string, error) {
	block, err := des.NewTripleDESCipher(key) //nolint:gosec
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	padded := pkcs5Pad(plain, bs)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func des3DecryptB64(cipherB64 string, key []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return nil, err
	}
	block, err := des.NewTripleDESCipher(key) //nolint:gosec
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("bad cipher len")
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += bs {
		block.Decrypt(out[i:i+bs], raw[i:i+bs])
	}
	return pkcs5Unpad(out, bs)
}

func pkcs5Pad(b []byte, bs int) []byte {
	pad := bs - len(b)%bs
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs5Unpad(b []byte, bs int) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty")
	}
	pad := int(b[len(b)-1])
	if pad <= 0 || pad > bs || pad > len(b) {
		return nil, fmt.Errorf("bad pad")
	}
	return b[:len(b)-pad], nil
}
