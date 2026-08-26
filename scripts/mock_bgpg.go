//go:build ignore

// Mock 背景评估 BJPG-01 (grsb / bgpg) upstream for full-link testing. Serves the
// JSON POST contract on /api/getData. Run: go run scripts/mock_bgpg.go
//
// 协议对齐 docs/BJPG-01背景评估 (2)(1).pdf：
//   - 请求头 accountId + prodId；请求体 {"data": Base64(AES/CBC/PKCS5(明文JSON))}。
//   - AES 密钥 = Hex.decodeHex(encryptKey)，IV = 16 个 ASCII 字符 "0000000000000000"。
//   - 明文业务 JSON 只有 {idCard,name} 两个字段（该上游不要手机号）。
//   - 响应 {"data","code","uuid","retMsg"}，data 为同一套 AES 密文。
// 场景（用解密出的 idCard 驱动；本上游无 mobile，故查无触发号改用身份证号）：
//   - 请求头缺 accountId        -> code 2-506
//   - 请求头 accountId 不匹配    -> code 2-507
//   - data 解密失败             -> code 2-501
//   - idCard/name 缺失          -> code 2-502
//   - idCard == 查无触发号       -> code 2-404 (没有查询到数据)
//   - idCard == 错误触发号       -> code 2-508 (请求ip不在白名单内)
//   - otherwise                -> code 200 + data{xm,sfz,jfdw,grsf,jfjs,cbjfzt,jfsj}
package main

import (
	"crypto/aes"
	"crypto/cipher"
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

// bgpgIV 与上游工具类一致：16 个 ASCII 字符 '0'，不是 16 个零字节。
const bgpgIV = "0000000000000000"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func pkcs5Pad(b []byte, blockSize int) []byte {
	pad := blockSize - len(b)%blockSize
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs5Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("空明文")
	}
	pad := int(b[len(b)-1])
	if pad <= 0 || pad > blockSize || pad > len(b) {
		return nil, fmt.Errorf("非法填充 %d", pad)
	}
	return b[:len(b)-pad], nil
}

func aesCBCEncryptBase64(plain, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs5Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(bgpgIV)).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

func aesCBCDecryptBase64(cipherB64 string, key []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("密文长度 %d 非法", len(raw))
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, []byte(bgpgIV)).CryptBlocks(out, raw)
	return pkcs5Unpad(out, bs)
}

func main() {
	addr := env("MOCK_BGPG_ADDR", ":9126")
	accountID := env("GRSB_ACCOUNT_ID", "demo-grsb-account")
	prodID := env("GRSB_PROD_ID", "BJPG-01")
	// encryptKey 是 hex 文本，解码后须为 16/24/32 字节（默认 32 个 hex 字符 = AES-128）。
	keyHex := env("GRSB_ENCRYPT_KEY", "0031cee6808eb6d5b0e07536218f1234")
	key, err := hex.DecodeString(keyHex)
	if err != nil || (len(key) != 16 && len(key) != 24 && len(key) != 32) {
		log.Fatalf("GRSB_ENCRYPT_KEY 非法：需 hex 文本且解码后 16/24/32 字节，得 %d 字节 (err=%v)", len(key), err)
	}
	// 本上游无 mobile，故查无/报错触发改用身份证号（均为合法 18 位格式）。
	notFoundIDCard := env("GRSB_NOTFOUND_IDCARD", "000000000000000404")
	errIDCard := env("GRSB_ERR_IDCARD", "000000000000000508")

	http.HandleFunc("/api/getData", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(raw, &req)

		gotAccount := r.Header.Get("accountId")
		resp := map[string]any{"uuid": "grsb-mock-" + fmt.Sprint(len(raw))}

		switch {
		case gotAccount == "":
			resp["code"], resp["retMsg"] = "2-506", "请求头中缺少accountId"
		case gotAccount != accountID:
			resp["code"], resp["retMsg"] = "2-507", "请求头中accountId不正确"
		case r.Header.Get("prodId") != prodID:
			resp["code"], resp["retMsg"] = "2-504", "您没有该接口的访问权限，请联系管理员处理"
		default:
			plain, derr := aesCBCDecryptBase64(req.Data, key)
			if derr != nil {
				resp["code"], resp["retMsg"] = "2-501", "解密失败，请检查data加密方式"
				break
			}
			var p struct {
				IDCard string `json:"idCard"`
				Name   string `json:"name"`
			}
			_ = json.Unmarshal(plain, &p)
			switch {
			case p.IDCard == "" || p.Name == "":
				resp["code"], resp["retMsg"] = "2-502", "参数不全或者参数不正确"
			case p.IDCard == notFoundIDCard:
				resp["code"], resp["retMsg"] = "2-404", "没有查询到数据"
			case p.IDCard == errIDCard:
				resp["code"], resp["retMsg"] = "2-508", "请求ip不在白名单内，请联系管理员"
			default:
				biz, _ := json.Marshal(map[string]string{
					"xm":     p.Name,
					"sfz":    p.IDCard,
					"jfdw":   "珠海科技有限公司",
					"grsf":   "24",     // 个人身份（文档标注可忽略）
					"jfjs":   "6800",   // 缴费基数
					"cbjfzt": "1",      // 参保状态：1 正常参保
					"jfsj":   "202603", // 缴费时间 yyyymm
				})
				enc, eerr := aesCBCEncryptBase64(biz, key)
				if eerr != nil {
					resp["code"], resp["retMsg"] = "2-500", "服务器内部异常"
				} else {
					resp["code"], resp["retMsg"] = "200", "成功"
					resp["data"] = enc
				}
			}
		}
		log.Printf("bgpg <- accountId=%s prodId=%s -> code=%v", gotAccount, r.Header.Get("prodId"), resp["code"])
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 背景评估 BJPG-01 upstream listening on %s (/api/getData)\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
