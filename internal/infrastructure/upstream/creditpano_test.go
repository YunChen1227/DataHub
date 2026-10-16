package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 信用全景V21 §1.4：apiKey 用参数表的无空格值 (示例里的 " loanRiskTagV21" 是笔误)，
// encryptionType=1 明文。
func TestCreditPanoRequestIsPlaintextSignedJSONPost(t *testing.T) {
	var got enolCapturedRequest
	srv := enolCaptureServer(t, &got, `{"code":0,"msg":"请求成功","seqNo":"S-1","data":{"busiCode":10,"busiMsg":"success","result":{"fee_status":"1"}}}`)
	defer srv.Close()

	c := NewCreditPano(CreditPanoConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
	if _, err := c.Query(context.Background(), enolThreeFactorReq); err != nil {
		t.Fatalf("Query: %v", err)
	}
	enolAssertThreeFactorRequest(t, &got, "loanRiskTagV21")
	if got.env.EncryptionType != 1 {
		t.Fatalf("encryptionType = %d, 期望 1 (明文)", got.env.EncryptionType)
	}
}

// 归一化口径对齐 docs/信用全景V21.pdf §1.6 busiCode 表 + result.fee_status「1-收费；0-不收费」。
// fee_status 是上游逐笔下发的计费标志，为计费唯一判据（billing-scope 铁律 3）。
func TestCreditPanoNormalization(t *testing.T) {
	cases := append([]enolNormCase{
		{
			name:     "busiCode 10 + fee_status=1 → 001 计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"1653qan7kg800","data":{"busiCode":10,"busiMsg":"success","result":{"xyp_model_score_high":"668","fee_status":"1"}}}`,
			wantCode: "001", wantUID: "1653qan7kg800",
		},
		{
			name:     "busiCode 10 + fee_status=0 → 999 不计费（上游明说不收费）",
			body:     `{"code":0,"msg":"请求成功","seqNo":"S-F0","data":{"busiCode":10,"busiMsg":"success","result":{"xyp_model_score_high":"668","fee_status":"0"}}}`,
			wantCode: "999", wantUID: "S-F0",
		},
		{
			name:     "busiCode 10 + fee_status 数字 0 → 999 不计费",
			body:     `{"code":0,"seqNo":"S-F0N","data":{"busiCode":10,"result":{"fee_status":0}}}`,
			wantCode: "999", wantUID: "S-F0N",
		},
		{
			name:     "busiCode 10 + fee_status 缺失 → 001（按 10 查询成功【计费】）",
			body:     `{"code":0,"seqNo":"S-FM","data":{"busiCode":10,"result":{"xyp_model_score_high":"668"}}}`,
			wantCode: "001", wantUID: "S-FM",
		},
		{
			name:     "busiCode 1000 数据未查得 → 999 不计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"S-1000","data":{"busiCode":1000,"busiMsg":"数据未查得"}}`,
			wantCode: "999", wantUID: "S-1000",
		},
	}, enolErrorRows()...)
	runEnolNormalization(t, cases, func(baseURL string, hc *http.Client) (*model.UpstreamResult, error) {
		return NewCreditPano(CreditPanoConfig{BaseURL: baseURL, AppID: "ap1", Secret: "sec1"}, hc).
			Query(context.Background(), enolThreeFactorReq)
	})
}

// fee_status 作为 result 的业务字段照样透出；上游 seqNo 不出现在 range 里。
func TestCreditPanoRangePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"请求成功","seqNo":"1653qan7kg800","data":{"busiCode":10,"busiMsg":"s uccess","result":{"xyp_t01aazhzz":"","xyp_cpl0073":"0.58","fee_status":"1"}}}`))
	}))
	defer srv.Close()

	res, err := NewCreditPano(CreditPanoConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client()).
		Query(context.Background(), enolThreeFactorReq)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := `{"xyp_t01aazhzz":"","xyp_cpl0073":"0.58","fee_status":"1"}`
	if res.Range != want {
		t.Fatalf("range = %s, 期望 %s", res.Range, want)
	}
	assertRangeOmits(t, res.Range, "seqNo", "1653qan7kg800")
}

func TestCreditPanoRequeryNotReachable(t *testing.T) {
	rr, err := NewCreditPano(CreditPanoConfig{BaseURL: "http://127.0.0.1:1"}, nil).Requery(context.Background(), "r1")
	if err != nil || rr.Reachable {
		t.Fatalf("对账接口未联调前 Reachable 必须为 false: rr=%+v err=%v", rr, err)
	}
}
