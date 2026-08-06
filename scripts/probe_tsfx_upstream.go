//go:build ignore

// probe_tsfx_upstream: 直连 kfongtech 上游，验证 api_key/api_secret 与加密加签。
// 用法: go run ./scripts/probe_tsfx_upstream.go
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	upstreamURL = "https://api.kfongtech.com/inlet/api"
	method      = "api.complaint.query"
	version     = "1.0.0"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func md5hex(s string, upper bool) string {
	sum := md5.Sum([]byte(s))
	h := hex.EncodeToString(sum[:])
	if upper {
		return strings.ToUpper(h)
	}
	return h
}

func deriveKeyIV(secret string) (key, iv string) {
	key = md5hex(secret, true)[8:24]
	iv = md5hex(key, true)[8:24]
	return key, iv
}

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

func pkcs7Pad(data []byte, bs int) []byte {
	pad := bs - len(data)%bs
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func encryptParam(plain, key, iv string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad([]byte(plain), block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(out, padded)
	return hex.EncodeToString(out), nil
}

func sign(params map[string]string, secret string) string {
	return md5hex(secret+sortParam(params), false)
}

func call(apiKey, apiSecret, mobile, label string) {
	biz := map[string]string{
		"method":  method,
		"version": version,
		"poly":    "C1",
		"mobile":  mobile,
	}
	plain := sortParam(biz)
	key, iv := deriveKeyIV(apiSecret)
	param, err := encryptParam(plain, key, iv)
	if err != nil {
		fmt.Printf("[%s] encrypt error: %v\n", label, err)
		return
	}
	signParams := map[string]string{
		"method":  biz["method"],
		"version": biz["version"],
		"poly":    biz["poly"],
		"mobile":  biz["mobile"],
		"apiKey":  apiKey,
	}
	body := map[string]string{
		"apiKey": apiKey,
		"param":  param,
		"sign":   sign(signParams, apiSecret),
	}
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	t0 := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(t0)
	if err != nil {
		fmt.Printf("[%s] HTTP error (%v): %v\n", label, elapsed, err)
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("[%s] HTTP %d (%v) mobile=%s\n  => %s\n", label, resp.StatusCode, elapsed, mobile, trunc(string(raw), 400))
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func main() {
	apiKey := env("TSFX_UPSTREAM_API_KEY", "3338412123068672")
	apiSecret := env("TSFX_UPSTREAM_API_SECRET", "DPL9SST7dqkjFs8vlG519O15La6wOOmE")
	plainMobile := env("TSFX_MOBILE", "13809091009")

	fmt.Println("== tsfx 直连 kfongtech 上游探测 ==")
	fmt.Printf("  url=%s\n", upstreamURL)
	fmt.Printf("  apiKey=%s\n", apiKey)

	call(apiKey, apiSecret, plainMobile, "plain-mobile")
	call(apiKey, apiSecret, md5hex(plainMobile, false), "md5-mobile")
}
