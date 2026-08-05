//go:build ignore

// probe_salesdata.go —— swfp 源5（销项数据 / 凯盈云 crestv）真实上游联调探针。
//
// 文档 docs/销项数据接口文档V1.0.docx §3.1 只写「使用 AppKey 进行 AES 加密并转 Base64」，
// 未给出分组模式/填充/密钥口径。本探针把候选口径逐个打到真实上游，用应答区分谁对：
// 上游能解密 → 返回业务码（0000/0001/...）；解不开 → 报错/500/乱码。
//
// 用法：
//
//	go run ./scripts/probe_salesdata.go
//	SALESDATA_APP_ID=xxx SALESDATA_APP_KEY=yyy SALESDATA_TAXID=zzz go run ./scripts/probe_salesdata.go
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	appID   = env("SALESDATA_APP_ID", "KQXQtVQJ")
	appKey  = env("SALESDATA_APP_KEY", "ECAA45DBB169138F395A1FDDB146F8934C9E8DD5BEB06AFBE9731B16B177EBFC")
	taxID   = env("SALESDATA_TAXID", "92500233MA60R5KW8M")
	baseURL = env("SALESDATA_BASE_URL", "http://api2.crestv.com:32313/api/ws")
	biz     = env("SALESDATA_BIZ", "monthlyInvoiceSummryInfo")
)

type keyVariant struct {
	name string
	key  []byte
	mode string // "ecb" | "cbc-zero" | "cbc-key16"
}

func pkcs5Pad(b []byte, bs int) []byte {
	pad := bs - len(b)%bs
	return append(b, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func encrypt(plain []byte, v keyVariant) (string, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	padded := pkcs5Pad(plain, bs)
	out := make([]byte, len(padded))
	switch v.mode {
	case "ecb":
		for i := 0; i < len(padded); i += bs {
			block.Encrypt(out[i:i+bs], padded[i:i+bs])
		}
	case "cbc-zero":
		cipher.NewCBCEncrypter(block, make([]byte, bs)).CryptBlocks(out, padded)
	case "cbc-key16":
		cipher.NewCBCEncrypter(block, v.key[:bs]).CryptBlocks(out, padded)
	default:
		return "", fmt.Errorf("unknown mode %s", v.mode)
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func variants() []keyVariant {
	hexKey, hexErr := hex.DecodeString(appKey)
	md5Key := md5.Sum([]byte(appKey))
	shaKey := sha256.Sum256([]byte(appKey))

	vs := []keyVariant{
		{"raw-appkey-bytes(现有实现)", []byte(appKey), "ecb"},
	}
	if hexErr == nil {
		vs = append(vs,
			keyVariant{"hex-decode(32B, AES-256)", hexKey, "ecb"},
			keyVariant{"hex-decode(32B) CBC/zero-IV", hexKey, "cbc-zero"},
			keyVariant{"hex-decode(32B) CBC/key[:16]-IV", hexKey, "cbc-key16"},
			keyVariant{"hex-decode 前16B(AES-128)", hexKey[:16], "ecb"},
		)
	}
	vs = append(vs,
		keyVariant{"appkey前32字符(AES-256)", []byte(appKey)[:32], "ecb"},
		keyVariant{"appkey前16字符(AES-128)", []byte(appKey)[:16], "ecb"},
		keyVariant{"appkey后16字符(AES-128)", []byte(appKey)[len(appKey)-16:], "ecb"},
		keyVariant{"md5(appkey)(16B)", md5Key[:], "ecb"},
		keyVariant{"sha256(appkey)(32B)", shaKey[:], "ecb"},
	)
	return vs
}

func main() {
	fmt.Printf("上游: %s/%s\n", strings.TrimRight(baseURL, "/"), biz)
	fmt.Printf("AppID=%s  AppKey长度=%d  taxpayerIdNum=%s\n\n", appID, len(appKey), taxID)

	inner, _ := json.Marshal(map[string]string{"taxpayerIdNum": taxID})
	client := &http.Client{Timeout: 30 * time.Second}
	fullURL := strings.TrimRight(baseURL, "/") + "/" + biz

	// 对照组：不加密，直接把内层 JSON 的 Base64 当 ReqData（看上游报什么错，最能暴露真实要求）。
	post(client, fullURL, "对照组: ReqData=Base64(明文JSON)", base64.StdEncoding.EncodeToString(inner))

	for _, v := range variants() {
		ct, err := encrypt(inner, v)
		if err != nil {
			fmt.Printf("── %-34s 本地加密失败: %v\n\n", v.name, err)
			continue
		}
		post(client, fullURL, v.name, ct)
	}
}

func post(client *http.Client, url, label, reqData string) {
	payload, _ := json.Marshal(map[string]string{"AppID": appID, "ReqData": reqData})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Printf("── %-34s 构造请求失败: %v\n\n", label, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("── %-34s 请求失败: %v\n\n", label, err)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(raw))
	if len(body) > 600 {
		body = body[:600] + fmt.Sprintf("…(共%d字节)", len(raw))
	}
	fmt.Printf("── %-34s HTTP %d\n   %s\n\n", label, resp.StatusCode, body)
}
