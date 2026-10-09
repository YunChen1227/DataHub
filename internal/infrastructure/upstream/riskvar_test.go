package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 风险变量V11 §1.4：encryptionType 只定义了 1=明文，报文带 encryptionType=1、PII 明文。
func TestRiskVarRequestIsPlaintextSignedJSONPost(t *testing.T) {
	var got enolCapturedRequest
	srv := enolCaptureServer(t, &got, `{"code":0,"msg":"请求成功","seqNo":"S-1","data":{"busiCode":10,"busiMsg":"success","result":{"Rule_final_decision":"Accept"}}}`)
	defer srv.Close()

	c := NewRiskVar(RiskVarConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
	if _, err := c.Query(context.Background(), enolThreeFactorReq); err != nil {
		t.Fatalf("Query: %v", err)
	}
	enolAssertThreeFactorRequest(t, &got, RiskVarAPIKey)
	if got.env.EncryptionType != 1 {
		t.Fatalf("encryptionType = %d, 期望 1 (明文)", got.env.EncryptionType)
	}
}

// 归一化口径逐条对齐 风险变量V11.pdf §1.6：10 查询成功【计费】→001；
// 1000 数据未查得【计费】→999（**本路由查无也计费**，由 billing.TableFor("fxbl") 决定）；
// 1001~1009 / code=-1 / 未知码 → 上游侧错误。
func TestRiskVarNormalization(t *testing.T) {
	cases := append([]enolNormCase{
		{
			name:     "busiCode 10 查询成功 → 001 计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"136e056c0g800","data":{"busiCode":10,"busiMsg":"success","result":{"als_m12_id_max_monnum":"6"}}}`,
			wantCode: "001", wantUID: "136e056c0g800",
		},
		{
			name:     "busiCode 1000 数据未查得 → 999（本路由仍计费）",
			body:     `{"code":0,"msg":"请求成功","seqNo":"S-1000","data":{"busiCode":1000,"busiMsg":"数据未查得"}}`,
			wantCode: "999", wantUID: "S-1000",
		},
	}, enolErrorRows()...)
	runEnolNormalization(t, cases, func(baseURL string, hc *http.Client) (*model.UpstreamResult, error) {
		return NewRiskVar(RiskVarConfig{BaseURL: baseURL, AppID: "ap1", Secret: "sec1"}, hc).
			Query(context.Background(), enolThreeFactorReq)
	})
}

func TestRiskVarRangePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"请求成功","seqNo":"136e056c0g800","data":{"busiCode":10,"busiMsg":"success","result":{"als_m6_id_avg_monnum":"2.75","Rule_final_decision":"Accept","Rule_name_QJF077":"在非银机构申请次数较多"}}}`))
	}))
	defer srv.Close()

	res, err := NewRiskVar(RiskVarConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client()).
		Query(context.Background(), enolThreeFactorReq)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := `{"als_m6_id_avg_monnum":"2.75","Rule_final_decision":"Accept","Rule_name_QJF077":"在非银机构申请次数较多"}`
	if res.Range != want {
		t.Fatalf("range = %s, 期望 %s", res.Range, want)
	}
	assertRangeOmits(t, res.Range, "seqNo", "136e056c0g800")
}

func TestRiskVarRequeryNotReachable(t *testing.T) {
	rr, err := NewRiskVar(RiskVarConfig{BaseURL: "http://127.0.0.1:1"}, nil).Requery(context.Background(), "r1")
	if err != nil || rr.Reachable {
		t.Fatalf("对账接口未联调前 Reachable 必须为 false: rr=%+v err=%v", rr, err)
	}
}
