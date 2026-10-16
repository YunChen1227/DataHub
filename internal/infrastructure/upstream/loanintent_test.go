package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// enolCapturedRequest 记录 enol doCheck 请求的原始报文，供三条 enol 新路由的请求契约测试复用。
type enolCapturedRequest struct {
	method, contentType string
	raw                 map[string]json.RawMessage
	env                 struct {
		EncryptionType int               `json:"encryptionType"`
		AppID          string            `json:"appId"`
		Sign           string            `json:"sign"`
		APIKey         string            `json:"apiKey"`
		Body           map[string]string `json:"body"`
	}
}

func enolCaptureServer(t *testing.T, got *enolCapturedRequest, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.contentType = r.Header.Get("Content-Type")
		var buf json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&buf)
		_ = json.Unmarshal(buf, &got.raw)
		_ = json.Unmarshal(buf, &got.env)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
}

// enolAssertThreeFactorRequest 校验三份 enol 文档 §1.4/§2.2 的共同请求契约：JSON POST、
// apiKey 固定值、body 只有 name/idCard/mobile 三项明文 (不注入 tradeNo)、sign 覆盖实发 body。
func enolAssertThreeFactorRequest(t *testing.T, got *enolCapturedRequest, wantAPIKey string) {
	t.Helper()
	if got.method != http.MethodPost {
		t.Fatalf("method = %s, 期望 POST", got.method)
	}
	if got.contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	if got.env.APIKey != wantAPIKey {
		t.Fatalf("apiKey = %q, 期望固定值 %q", got.env.APIKey, wantAPIKey)
	}
	want := map[string]string{"name": "张三", "idCard": "360429199806073112", "mobile": "17325421010"}
	if len(got.env.Body) != len(want) {
		t.Fatalf("body 应只有 name/idCard/mobile 三项, 实际 %v", got.env.Body)
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

var enolThreeFactorReq = &model.UpstreamRequest{
	Name: "张三", IDCard: "360429199806073112", Mobile: "17325421010", Reqid: "r1",
}

// enolNormCase 是 busiCode 归一表的一行；wantCode 为空表示期望上游侧错误。
type enolNormCase struct {
	name     string
	body     string
	wantCode string
	wantUID  string
}

// enolErrorRows 是三份文档 §1.6 共有的上游侧错误码行 (均不计费)。
func enolErrorRows() []enolNormCase {
	rows := []enolNormCase{}
	for _, c := range []struct {
		code int
		msg  string
	}{
		{1001, "账户余额不足"}, {1002, "账户信息不存在"}, {1003, "appId异常"}, {1004, "产品编号异常"},
		{1005, "账号信息异常"}, {1006, "透支余额已达上限"}, {1007, "数据请求异常"}, {1009, "服务尚未开通"},
	} {
		code := strconv.Itoa(c.code)
		uid := "S-" + code
		rows = append(rows, enolNormCase{
			name:    "busiCode " + code + " " + c.msg + " → 上游侧错误",
			body:    `{"code":0,"msg":"请求成功","seqNo":"` + uid + `","data":{"busiCode":` + code + `,"busiMsg":"` + c.msg + `"}}`,
			wantUID: uid,
		})
	}
	return append(rows,
		enolNormCase{name: "全局 code=-1 响应异常 → 上游侧错误", body: `{"code":-1,"msg":"响应异常","seqNo":"S-ERR"}`, wantUID: "S-ERR"},
		enolNormCase{name: "未知 busiCode → 上游侧错误（不得退化成查得误计费）", body: `{"code":0,"seqNo":"S-X","data":{"busiCode":9999,"busiMsg":"未知"}}`, wantUID: "S-X"},
	)
}

func runEnolNormalization(t *testing.T, cases []enolNormCase, query func(baseURL string, hc *http.Client) (*model.UpstreamResult, error)) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			res, err := query(srv.URL, srv.Client())
			if tc.wantCode == "" {
				if err == nil {
					t.Fatalf("期望上游侧错误，实际成功: code=%s range=%s", res.Code, res.Range)
				}
				var be *model.UpstreamError
				if !errors.As(err, &be) {
					t.Fatalf("期望 *model.UpstreamError（带上游 code/uid 落审计），实际 %T: %v", err, err)
				}
				if be.UID != tc.wantUID || be.LogID != tc.wantUID {
					t.Fatalf("失败路径 UID/LogID = %q/%q, 期望均为 %q", be.UID, be.LogID, tc.wantUID)
				}
				return
			}
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if res.Code != tc.wantCode {
				t.Fatalf("code = %s, 期望 %s", res.Code, tc.wantCode)
			}
			if res.UID != tc.wantUID || res.LogID != tc.wantUID {
				t.Fatalf("UID/LogID = %q/%q, 期望均为 %q", res.UID, res.LogID, tc.wantUID)
			}
			if res.Code == "999" && res.Range != "" {
				t.Fatalf("999 不应透出 range, 实际 %s", res.Range)
			}
		})
	}
}

// 借贷申请意向V22【TD】§1.4 请求结构**没有 encryptionType**：报文里不得出现该键。
func TestLoanIntentRequestOmitsEncryptionType(t *testing.T) {
	var got enolCapturedRequest
	srv := enolCaptureServer(t, &got, `{"code":0,"msg":"请求成功","seqNo":"S-1","data":{"busiCode":10,"busiMsg":"success","result":{"TC_LOAN_CNT_3M":9}}}`)
	defer srv.Close()

	c := NewLoanIntent(LoanIntentConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
	if _, err := c.Query(context.Background(), enolThreeFactorReq); err != nil {
		t.Fatalf("Query: %v", err)
	}
	enolAssertThreeFactorRequest(t, &got, LoanIntentAPIKey)
	if _, present := got.raw["encryptionType"]; present {
		t.Fatalf("TD 文档请求结构无 encryptionType，报文不应带该键: %v", got.raw)
	}
}

// 归一化口径逐条对齐 docs/借贷申请意向V22【TD】.pdf §1.6：10 查得【计费】→001；1000 未查得
// (无计费标注)→999；1001~1009 / code=-1 / 未知码 → 上游侧错误。
func TestLoanIntentNormalization(t *testing.T) {
	cases := append([]enolNormCase{
		{
			name:     "busiCode 10 查得结果 → 001 计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"26ahy0m5dhag","data":{"busiCode":10,"busiMsg":"success","result":{"TC_LOAN_CNT_3M":9}}}`,
			wantCode: "001", wantUID: "26ahy0m5dhag",
		},
		{
			name:     "busiCode 1000 未查得 → 999 不计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"S-1000","data":{"busiCode":1000,"busiMsg":"未查得"}}`,
			wantCode: "999", wantUID: "S-1000",
		},
	}, enolErrorRows()...)
	runEnolNormalization(t, cases, func(baseURL string, hc *http.Client) (*model.UpstreamResult, error) {
		return NewLoanIntent(LoanIntentConfig{BaseURL: baseURL, AppID: "ap1", Secret: "sec1"}, hc).
			Query(context.Background(), enolThreeFactorReq)
	})
}

// result 富对象整体透出：数字字面量原样保留，上游 seqNo 不出现在 range 里。
func TestLoanIntentRangePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"请求成功","seqNo":"26ahy0m5dhag","data":{"busiCode":10,"busiMsg":"success","result":{"TC_LOAN_CNT_3M":9,"TC_BANK_ORG_3M":2,"seqNo":"26ahy0m5dhag"}}}`))
	}))
	defer srv.Close()

	res, err := NewLoanIntent(LoanIntentConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client()).
		Query(context.Background(), enolThreeFactorReq)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Range != `{"TC_LOAN_CNT_3M":9,"TC_BANK_ORG_3M":2}` {
		t.Fatalf("range = %s", res.Range)
	}
	assertRangeOmits(t, res.Range, "seqNo", "26ahy0m5dhag")
}

func TestLoanIntentRequeryNotReachable(t *testing.T) {
	rr, err := NewLoanIntent(LoanIntentConfig{BaseURL: "http://127.0.0.1:1"}, nil).Requery(context.Background(), "r1")
	if err != nil || rr.Reachable {
		t.Fatalf("对账接口未联调前 Reachable 必须为 false: rr=%+v err=%v", rr, err)
	}
}
