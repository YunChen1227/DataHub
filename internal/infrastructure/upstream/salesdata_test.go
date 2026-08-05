package upstream

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 凯盈云实际下发的 AppKey 形态：64 个十六进制字符。直接当 ASCII 字节用是 64 字节，
// crypto/aes 会报 invalid key size 64——源5 在联网之前就已经失败。
const prodShapedAppKey = "ECAA45DBB169138F395A1FDDB146F8934C9E8DD5BEB06AFBE9731B16B177EBFC"

func TestSalesAESKeyAcceptsDocumentedForms(t *testing.T) {
	cases := []struct {
		name    string
		appKey  string
		wantLen int
	}{
		{"64 hex 字符 → AES-256", prodShapedAppKey, 32},
		{"48 hex 字符 → AES-192", strings.Repeat("ab", 24), 24},
		{"16 字节原文", "demosalesaeskey0", 16},
		{"32 字节原文(非 hex)", "demosaleskey-32-bytes-long-!!!!!", 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := salesAESKey(tc.appKey)
			if err != nil {
				t.Fatalf("salesAESKey(%q) 报错: %v", tc.appKey, err)
			}
			if len(key) != tc.wantLen {
				t.Fatalf("密钥长度 = %d, 期望 %d", len(key), tc.wantLen)
			}
		})
	}
}

func TestSalesAESKeyRejectsInvalidLength(t *testing.T) {
	for _, appKey := range []string{"", "short", "REPLACE_WITH_SALESDATA_APP_KEY"} {
		if _, err := salesAESKey(appKey); err == nil {
			t.Fatalf("salesAESKey(%q) 应报错，实际通过", appKey)
		}
	}
}

// 原文长度已合法时一律按原文使用，不再尝试 hex 解码。副作用：32 个 hex 字符
// 同时满足「32 字节原文」与「16 字节 hex」，按此优先级判为 AES-256 原文密钥
// （凯盈云下发的是 64 字符，不落在这个歧义区；真遇到 AES-128 hex 需显式约定）。
func TestSalesAESKeyPrefersRawWhenAlreadyValidLength(t *testing.T) {
	raw := strings.Repeat("ab", 16) // 32 字符，也是合法 hex
	key, err := salesAESKey(raw)
	if err != nil {
		t.Fatalf("salesAESKey 报错: %v", err)
	}
	if string(key) != raw {
		t.Fatalf("密钥 = %q, 期望原文 %q", key, raw)
	}
}

// 端到端：客户端用生产形态的 AppKey 请求，mock 上游按同样口径解密后应能读出
// taxpayerIdNum，并按文档 §3.1 的同构信封回加密应答。
func TestSalesDataQueryWithProdShapedAppKey(t *testing.T) {
	key, err := hex.DecodeString(prodShapedAppKey)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}

	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, 期望 application/json", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var env salesEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("解析外层信封: %v", err)
		}
		if env.AppID != "KQXQtVQJ" {
			t.Errorf("AppID = %q", env.AppID)
		}
		plain, err := aesECBDecryptBase64(env.ReqData, key)
		if err != nil {
			t.Fatalf("上游解密 ReqData 失败: %v", err)
		}
		var inner map[string]string
		if err := json.Unmarshal(plain, &inner); err != nil {
			t.Fatalf("解析内层请求: %v", err)
		}
		if inner["taxpayerIdNum"] != "92500233MA60R5KW8M" {
			t.Errorf("taxpayerIdNum = %q", inner["taxpayerIdNum"])
		}

		var data string
		if strings.HasSuffix(r.URL.Path, salesBizInvoiceSummary) {
			data = `{"salesInvoice":[{"belongMonth":"202601","invoiceAmtMonth":100.5}]}`
		} else {
			data = `{"monthlyDownstreamInfo":[{"belongMonth":"202601","buyerName":"某某公司"}]}`
		}
		respInner := `{"code":"0000","msg":"查询成功","taxAmountTotal":"123","firstTime":"2026-01-01 00:00:00","charge":"1","data":` + data + `}`
		ct, err := aesECBEncryptBase64([]byte(respInner), key)
		if err != nil {
			t.Fatalf("上游加密应答失败: %v", err)
		}
		out, _ := json.Marshal(salesEnvelope{ReqData: ct})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	client := NewSalesData(SalesDataConfig{
		BaseURL: srv.URL + "/api/ws",
		AppID:   "KQXQtVQJ",
		AppKey:  prodShapedAppKey,
	}, srv.Client())

	res, err := client.Query(context.Background(), &model.UpstreamRequest{
		CreditCode: "92500233MA60R5KW8M",
		Reqid:      "test-reqid",
	})
	if err != nil {
		t.Fatalf("Query 失败: %v", err)
	}
	if res.Code != "001" {
		t.Fatalf("Code = %q, 期望 001", res.Code)
	}
	for _, want := range []string{"salesInvoice", "monthlyDownstreamInfo"} {
		if !strings.Contains(res.Range, want) {
			t.Errorf("range 缺少 %s: %s", want, res.Range)
		}
	}
	// 文档 §2.1：业务接口作为路径追加在 …/api/ws 之后。
	wantPaths := []string{"/api/ws/" + salesBizInvoiceSummary, "/api/ws/" + salesBizDownstream}
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("请求路径 = %v, 期望 %v", gotPaths, wantPaths)
	}
}

// AppKey 解不出密钥时不 panic，查询返回带原因的错误。
func TestSalesDataQueryFailsClearlyOnBadAppKey(t *testing.T) {
	client := NewSalesData(SalesDataConfig{
		BaseURL: "http://127.0.0.1:1",
		AppID:   "x",
		AppKey:  "REPLACE_WITH_SALESDATA_APP_KEY",
	}, nil)
	_, err := client.Query(context.Background(), &model.UpstreamRequest{CreditCode: "92500233MA60R5KW8M"})
	if err == nil || !strings.Contains(err.Error(), "AppKey") {
		t.Fatalf("期望密钥错误，实际: %v", err)
	}
}

// 文档 §4.3 发票明细：字段名/大小写与分页默认值必须与文档一致。
func TestSalesDataQueryInvoiceDetail(t *testing.T) {
	key, _ := hex.DecodeString(prodShapedAppKey)

	var got salesDetailRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, salesBizInvoiceDetail) {
			t.Errorf("路径 = %q, 期望以 %s 结尾", r.URL.Path, salesBizInvoiceDetail)
		}
		body, _ := io.ReadAll(r.Body)
		var env salesEnvelope
		_ = json.Unmarshal(body, &env)
		plain, err := aesECBDecryptBase64(env.ReqData, key)
		if err != nil {
			t.Fatalf("解密失败: %v", err)
		}
		// 逐字校验文档 §4.3 的字段名与大小写。
		var rawFields map[string]json.RawMessage
		if err := json.Unmarshal(plain, &rawFields); err != nil {
			t.Fatalf("解析内层: %v", err)
		}
		for _, f := range []string{"taxpayerIdNum", "StartIndex", "CountLimit"} {
			if _, ok := rawFields[f]; !ok {
				t.Errorf("内层请求缺字段 %s: %s", f, plain)
			}
		}
		if err := json.Unmarshal(plain, &got); err != nil {
			t.Fatalf("解析内层: %v", err)
		}

		respInner := `{"code":"0000","msg":"查询成功","data":{"total":"2","invoiceInfos":[{"invoiceIdNum":"1"},{"invoiceIdNum":"2"}]}}`
		ct, _ := aesECBEncryptBase64([]byte(respInner), key)
		out, _ := json.Marshal(salesEnvelope{ReqData: ct})
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	client := NewSalesData(SalesDataConfig{
		BaseURL: srv.URL, AppID: "KQXQtVQJ", AppKey: prodShapedAppKey,
	}, srv.Client())

	// countLimit=0 → 文档默认 1000；超过 1000 会被截断。
	page, err := client.QueryInvoiceDetail(context.Background(), "92500233MA60R5KW8M", 0, 0, "rid")
	if err != nil {
		t.Fatalf("QueryInvoiceDetail 失败: %v", err)
	}
	if got.CountLimit != salesDetailDefaultCount {
		t.Errorf("CountLimit = %d, 期望默认 %d", got.CountLimit, salesDetailDefaultCount)
	}
	if got.TaxpayerIDNum != "92500233MA60R5KW8M" {
		t.Errorf("taxpayerIdNum = %q", got.TaxpayerIDNum)
	}
	if page.Total.String() != "2" || len(page.InvoiceInfos) != 2 {
		t.Errorf("total=%s infos=%d", page.Total, len(page.InvoiceInfos))
	}

	if _, err := client.QueryInvoiceDetail(context.Background(), "92500233MA60R5KW8M", -5, 5000, "rid"); err != nil {
		t.Fatalf("QueryInvoiceDetail 失败: %v", err)
	}
	if got.CountLimit != salesDetailMaxCount {
		t.Errorf("CountLimit = %d, 期望截断为 %d", got.CountLimit, salesDetailMaxCount)
	}
	if got.StartIndex != 0 {
		t.Errorf("StartIndex = %d, 期望负值归 0", got.StartIndex)
	}
}

// 无加密联调环境可能直接回 Base64 的明文 JSON，或裸明文；两者都要能解。
func TestDecodeSalesBodyFallbacks(t *testing.T) {
	key, _ := hex.DecodeString(prodShapedAppKey)
	inner := `{"code":"0000"}`

	ct, _ := aesECBEncryptBase64([]byte(inner), key)
	enc, _ := json.Marshal(salesEnvelope{ReqData: ct})
	b64, _ := json.Marshal(salesEnvelope{ReqData: base64.StdEncoding.EncodeToString([]byte(inner))})

	for name, raw := range map[string][]byte{
		"AES 密文": enc,
		"Base64 明文": b64,
		"裸明文":    []byte(inner),
	} {
		if got := string(decodeSalesBody(raw, key)); got != inner {
			t.Errorf("%s: decodeSalesBody = %q, 期望 %q", name, got, inner)
		}
	}
}
