//go:build ignore

// Mock 销项数据上游 (凯盈云 crestv, docs/销项数据接口文档V1.0.docx)，swfp 源5 的
// 全链路测试挡板。Run: go run scripts/mock_salesdata.go
//
// 严格复刻协议假设 (internal/infrastructure/upstream/salesdata.go)：外层
// {AppID, ReqData}，ReqData = Base64(AES/ECB/PKCS5(内层 JSON))，应答同构。
// 按 taxpayerIdNum（= 下游 creditCode）驱动场景（与 mock_entcredit 同一惯例）：
//   - 92500233MA60R5KW8M → 两接口均 0000 查得（源5 ok）
//   - 91110000EMPTYEMPT0 → 两接口均 0001 查无
//   - 91110000BADFA00001 → 两接口均 0002 请求超时（源5 error → 下游 002）
//   - 其余合法税号     → 0000 查得
//   - AppID 不符 / 解密失败 → 0002
package main

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

var (
	appID  = env("SALESDATA_APP_ID", "demo-sales-appid")
	appKey = env("SALESDATA_APP_KEY", "demosalesaeskey0") // 16 字节 AES 密钥
	addr   = env("SALESDATA_ADDR", ":9121")
)

const (
	creditNormal = "92500233MA60R5KW8M"
	creditEmpty  = "91110000EMPTYEMPT0"
	creditFail   = "91110000BADFA00001"
)

// --- AES/ECB/PKCS5 (与 relay 端 aesecb.go 镜像) ---

func pkcs5Pad(data []byte, bs int) []byte {
	pad := bs - len(data)%bs
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func encrypt(plain []byte) string {
	block, _ := aes.NewCipher([]byte(appKey))
	bs := block.BlockSize()
	padded := pkcs5Pad(plain, bs)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return base64.StdEncoding.EncodeToString(out)
}

func decrypt(cipherB64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher([]byte(appKey))
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("bad cipher len %d", len(raw))
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += bs {
		block.Decrypt(out[i:i+bs], raw[i:i+bs])
	}
	pad := int(out[len(out)-1])
	if pad <= 0 || pad > bs || pad > len(out) {
		return nil, fmt.Errorf("bad padding")
	}
	return out[:len(out)-pad], nil
}

// respond 加密内层应答并按外层信封写回。
func respond(w http.ResponseWriter, inner map[string]any) {
	plain, _ := json.Marshal(inner)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"ReqData": encrypt(plain)})
}

func fail(w http.ResponseWriter, code, msg string) {
	respond(w, map[string]any{"code": code, "msg": msg, "taxAmountTotal": "", "firstTime": "", "charge": "0"})
}

// bizData 按业务接口返回样例数据（结构对齐 docs/销项数据接口文档V1.0.docx §4.1/§4.2）。
func bizData(biz string) map[string]any {
	if biz == "monthlyInvoiceSummryInfo" {
		return map[string]any{
			"salesInvoice": []map[string]any{{
				"belongMonth": 202403, "invoiceAmtMonth": 1234.56, "taxAmtMonth": 160.49,
				"invoiceCntMonth": 8, "invoiceHighAmtMonth": 500.00, "allInvoiceHighAmtMonth": 600.00,
				"redInvoiceAmtMonth": -10.00, "redTaxAmtMonth": -1.30, "redInvoiceCntMonth": 1,
				"nullifiedInvoiceAmtMonth": 0, "nullifiedInvoiceCntMonth": 0, "nullTaxAmtMonth": 0,
				"invoiceDayMonth": 5, "blueInvoiceDayMonth": 4,
				"latestInvoiceDate": "20240328", "noTradeRcordDay": 3,
			}},
			"summaryIndicators": map[string]any{
				"inputL1ySaleActualAmt": 99999.99, "inputSaleTop1": 0.4, "inputSaleCorps": 12,
				"lastMonth": "20240301", "firstMonth": "20220401", "inputL1yMths": 11,
			},
		}
	}
	return map[string]any{
		"monthlyDownstreamInfo": []map[string]any{{
			"belongMonth": 202403, "buyerName": "重庆众合共赢科技有限公司", "buyerTaxpayerIdNum": "91500233MA5YQWQ44M",
			"tradeAmtRankMonth": 1, "tradeAmtMonth": 800.00, "taxAmtMonth": 104.00,
			"invoiceCntMonth": 2, "invoiceCntPctMonth": 0.250, "tradeAmtPctMonth": 0.648,
			"redInvoiceAmtMonth": 0, "redInvoiceCntMonth": 0, "redTaxAmtMonth": 0,
			"nullifiedInvoiceAmtMonth": 0, "nullifiedInvoiceCntMonth": 0, "nullTaxAmtMonth": 0,
		}},
	}
}

func handle(biz string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			AppID   string `json:"AppID"`
			ReqData string `json:"ReqData"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || env.ReqData == "" {
			fail(w, "0002", "报文解析失败")
			return
		}
		if env.AppID != appID {
			fail(w, "0002", "AppID 无效")
			return
		}
		plain, err := decrypt(env.ReqData)
		if err != nil {
			fail(w, "0002", "ReqData 解密失败")
			return
		}
		var inner struct {
			TaxpayerIdNum string `json:"taxpayerIdNum"`
		}
		if err := json.Unmarshal(plain, &inner); err != nil || inner.TaxpayerIdNum == "" {
			fail(w, "0002", "taxpayerIdNum 缺失")
			return
		}

		switch inner.TaxpayerIdNum {
		case creditEmpty:
			respond(w, map[string]any{"code": "0001", "msg": "当前税号无关联数据",
				"taxAmountTotal": "0", "firstTime": "2026-08-01 10:00:00", "charge": "0"})
		case creditFail:
			fail(w, "0002", "请求超时")
		default:
			respond(w, map[string]any{"code": "0000", "msg": "查询成功",
				"taxAmountTotal": "1234567.89", "firstTime": "2026-08-01 10:00:00", "charge": "1",
				"data": bizData(biz)})
		}
	}
}

func main() {
	http.HandleFunc("/api/ws/monthlyInvoiceSummryInfo", handle("monthlyInvoiceSummryInfo"))
	http.HandleFunc("/api/ws/monthlyDownstreamInfo", handle("monthlyDownstreamInfo"))
	fmt.Printf("mock salesdata (销项数据, swfp 源5) listening on %s  AppID=%s\n", addr, appID)
	log.Fatal(http.ListenAndServe(addr, nil))
}
