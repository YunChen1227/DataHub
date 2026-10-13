package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

var riskPersonReq = &model.UpstreamRequest{
	Name: "张三", IDCard: "310000199001010010", Mobile: "17325421010", Reqid: "r1",
}

// 风险人员V108 §2.2：body 只有 name+idCard（tradeNo 选填不注入，**无 mobile**）；
// §1.4 encryptionType 缺省 1 明文；sign 覆盖实发 body。
func TestRiskPersonRequestIsPlaintextNameIDCardOnly(t *testing.T) {
	var got enolCapturedRequest
	srv := enolCaptureServer(t, &got, `{"code":0,"msg":"请求成功","seqNo":"48u8tgsvwd60","data":{"busiCode":10,"busiMsg":"success","result":{"resultCode":"0000000000000000000000001"}}}`)
	defer srv.Close()

	c := NewRiskPerson(RiskPersonConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
	if _, err := c.Query(context.Background(), riskPersonReq); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got.method != http.MethodPost || got.contentType != "application/json; charset=utf-8" {
		t.Fatalf("method/content-type = %s / %q", got.method, got.contentType)
	}
	if got.env.APIKey != RiskPersonAPIKey {
		t.Fatalf("apiKey = %q, 期望 %q", got.env.APIKey, RiskPersonAPIKey)
	}
	if got.env.EncryptionType != 1 {
		t.Fatalf("encryptionType = %d, 期望缺省 1 (明文)", got.env.EncryptionType)
	}
	want := map[string]string{"name": "张三", "idCard": "310000199001010010"}
	if len(got.env.Body) != len(want) {
		t.Fatalf("body 应只有 name/idCard 两项（本产品参数表无 mobile）, 实际 %v", got.env.Body)
	}
	for k, v := range want {
		if got.env.Body[k] != v {
			t.Fatalf("body.%s = %q, 期望明文 %q", k, got.env.Body[k], v)
		}
	}
	if s := signGama(got.env.Body, "sec1"); got.env.Sign != s {
		t.Fatalf("sign = %q, 期望 %q", got.env.Sign, s)
	}
}

func TestRiskPersonMD5ModeHashesPII(t *testing.T) {
	var got enolCapturedRequest
	srv := enolCaptureServer(t, &got, `{"code":0,"seqNo":"S","data":{"busiCode":1000,"busiMsg":"数据未查得"}}`)
	defer srv.Close()

	c := NewRiskPerson(RiskPersonConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1", EncryptionType: 2}, srv.Client())
	if _, err := c.Query(context.Background(), riskPersonReq); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got.env.EncryptionType != 2 {
		t.Fatalf("encryptionType = %d, 期望 2", got.env.EncryptionType)
	}
	if got.env.Body["name"] != encodePII("张三", 2) || got.env.Body["idCard"] != encodePII("310000199001010010", 2) {
		t.Fatalf("MD5 模式 body 应为摘要值: %v", got.env.Body)
	}
}

// 归一化口径逐条对齐 风险人员V108.pdf §1.6：10 查询成功【计费】→001；
// 1000 数据未查得【计费】→999（**本路由查无也计费**，由 billing.TableFor("fxry") 决定）；
// 1001~1009 / code=-1 / 未知码 → 上游侧错误；10 但 resultCode 缺失/非法 → 上游侧错误。
func TestRiskPersonNormalization(t *testing.T) {
	cases := append([]enolNormCase{
		{
			name:     "busiCode 10 查询成功 → 001 计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"48u8tgsvwd60","data":{"busiCode":10,"busiMsg":"success","result":{"resultCode":"0000000000000000000000001"}}}`,
			wantCode: "001", wantUID: "48u8tgsvwd60",
		},
		{
			name:     "busiCode 1000 数据未查得 → 999（本路由仍计费）",
			body:     `{"code":0,"msg":"请求成功","seqNo":"S-1000","data":{"busiCode":1000,"busiMsg":"数据未查得"}}`,
			wantCode: "999", wantUID: "S-1000",
		},
		{
			name:    "busiCode 10 但 result 缺失 → 上游侧错误（拿不到结论不计费）",
			body:    `{"code":0,"seqNo":"S-EMPTY","data":{"busiCode":10,"busiMsg":"success"}}`,
			wantUID: "S-EMPTY",
		},
		{
			name:    "busiCode 10 但 resultCode 含非 0/1 字符 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-BAD","data":{"busiCode":10,"busiMsg":"success","result":{"resultCode":"00000000000000000000000x1"}}}`,
			wantUID: "S-BAD",
		},
	}, enolErrorRows()...)
	runEnolNormalization(t, cases, func(baseURL string, hc *http.Client) (*model.UpstreamResult, error) {
		return NewRiskPerson(RiskPersonConfig{BaseURL: baseURL, AppID: "ap1", Secret: "sec1"}, hc).
			Query(context.Background(), riskPersonReq)
	})
}

func TestRiskPersonRangePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"请求成功","seqNo":"48u8tgsvwd60","data":{"busiCode":10,"busiMsg":"success","result":{"resultCode":"1000000000001000000000000"}}}`))
	}))
	defer srv.Close()

	res, err := NewRiskPerson(RiskPersonConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client()).
		Query(context.Background(), riskPersonReq)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if want := `{"resultCode":"1000000000001000000000000"}`; res.Range != want {
		t.Fatalf("range = %s, 期望 %s", res.Range, want)
	}
	assertRangeOmits(t, res.Range, "seqNo", "48u8tgsvwd60")
}

func TestRiskPersonRequeryNotReachable(t *testing.T) {
	rr, err := NewRiskPerson(RiskPersonConfig{BaseURL: "http://127.0.0.1:1"}, nil).Requery(context.Background(), "r1")
	if err != nil || rr.Reachable {
		t.Fatalf("对账接口未联调前 Reachable 必须为 false: rr=%+v err=%v", rr, err)
	}
}
