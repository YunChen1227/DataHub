//go:build ignore

// Mock 租赁分V2-D (守信) upstream for zlf full-link testing. Run:
// go run scripts/mock_rental.go
//
// Accepts form POST institution_id + AES/ECB/PKCS5 biz_data, decrypts with
// RENTAL_AES_KEY, then routes:
//   - bad institution_id     -> SW0001
//   - phone == 13800000000   -> SW0002 (查无)
//   - otherwise              -> SW0000 + score1 546.6
package main

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type rentalBiz struct {
	Name        string `json:"name"`
	Phone       string `json:"phone"`
	IdentNumber string `json:"ident_number"`
}

func pkcs5Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty ciphertext")
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > len(data) {
		return nil, fmt.Errorf("invalid padding")
	}
	return data[:len(data)-pad], nil
}

func aesECBDecryptBase64(cipherB64 string, key []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(raw)%bs != 0 {
		return nil, fmt.Errorf("ciphertext length %d not block-aligned", len(raw))
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += bs {
		block.Decrypt(out[i:i+bs], raw[i:i+bs])
	}
	return pkcs5Unpad(out)
}

func main() {
	addr := env("MOCK_RENTAL_ADDR", ":9114")
	institution := env("RENTAL_INSTITUTION_ID", "demo-rental-institution")
	aesKey := []byte(env("RENTAL_AES_KEY", "demo-rental-aes1"))

	http.HandleFunc("/api/lightning/product/query", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		instID := r.FormValue("institution_id")
		bizCipher := r.FormValue("biz_data")

		resp := map[string]any{"resp_order": "rental-mock-001", "timestamp": 1718456789012}
		switch {
		case instID != institution:
			resp["resp_code"], resp["resp_msg"] = "SW0001", "认证失败"
		default:
			plain, err := aesECBDecryptBase64(bizCipher, aesKey)
			if err != nil {
				resp["resp_code"], resp["resp_msg"] = "SW0033", "解密失败"
				break
			}
			var biz rentalBiz
			if err := json.Unmarshal(plain, &biz); err != nil {
				resp["resp_code"], resp["resp_msg"] = "SW0017", "参数错误"
				break
			}
			switch {
			case biz.Phone == "13800000000":
				resp["resp_code"], resp["resp_msg"] = "SW0002", "查无记录"
			default:
				resp["resp_code"], resp["resp_msg"] = "SW0000", "查询成功"
				resp["resp_data"] = map[string]float64{"score1": 546.6}
			}
			log.Printf("rental <- phone=%s -> resp_code=%v", biz.Phone, resp["resp_code"])
		}

		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 租赁分V2-D upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}