package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

var antiFraudReq = &model.UpstreamRequest{Mobile: "17325421010", Reqid: "r1"}

// 阿里云官方签名机制文档的示例向量 (ECS DescribeRegions, secret=testsecret)。
func TestSignAliyunPOPKnownVector(t *testing.T) {
	params := map[string]string{
		"Timestamp":        "2016-02-23T12:46:24Z",
		"Format":           "XML",
		"AccessKeyId":      "testid",
		"Action":           "DescribeRegions",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   "3ee8c1b8-83d3-44af-a94f-4e0ad82fd6cf",
		"Version":          "2014-05-26",
		"SignatureVersion": "1.0",
	}
	if got, want := signAliyunPOP(http.MethodGet, params, "testsecret"), "OLeaidS1JvxuMvnyHOwuJ+uX5qY="; got != want {
		t.Fatalf("signature = %q, 期望 %q", got, want)
	}
}

func antiFraudServer(t *testing.T, got *url.Values, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, 期望 POST", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		v, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("form 解析失败: %v", err)
		}
		if got != nil {
			*got = v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
}

// §2.3 公共参数 + §3.1 Service + §3.2 ServiceParameters (mobile + scene)，签名可复算。
func TestAntiFraudRequestShape(t *testing.T) {
	var got url.Values
	srv := antiFraudServer(t, &got, `{"Message":"OK","RequestId":"RID","Data":{"score":"258"},"Code":200}`)
	defer srv.Close()

	c := NewAntiFraud(AntiFraudConfig{BaseURL: srv.URL, AccessKeyID: "ak", AccessKeySecret: "sk"}, srv.Client())
	c.now = func() time.Time { return time.Date(2026, 10, 28, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }
	if _, err := c.Query(context.Background(), antiFraudReq); err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := map[string]string{
		"AccessKeyId":      "ak",
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"Timestamp":        "2026-10-28T02:00:00Z",
		"Version":          AntiFraudAPIVersion,
		"Action":           "ExecuteRequest",
		"Service":          "anti_fraud_customed",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Fatalf("%s = %q, 期望 %q", k, got.Get(k), v)
		}
	}
	if got.Get("SignatureNonce") == "" {
		t.Fatal("SignatureNonce 为空")
	}
	var sp map[string]string
	if err := json.Unmarshal([]byte(got.Get("ServiceParameters")), &sp); err != nil {
		t.Fatalf("ServiceParameters 非 JSON: %v", err)
	}
	if len(sp) != 2 || sp["mobile"] != "17325421010" || sp["scene"] != "gc_cust_202610" {
		t.Fatalf("ServiceParameters = %v, 期望仅 mobile + scene", sp)
	}
	params := map[string]string{}
	for k := range got {
		params[k] = got.Get(k)
	}
	if s := signAliyunPOP(http.MethodPost, params, "sk"); got.Get("Signature") != s {
		t.Fatalf("Signature = %q, 期望 %q", got.Get("Signature"), s)
	}
}

// 归一化口径逐条对齐 docs/申请反欺诈定制模型_冠辰科技_202610.pdf §3.3/§3.4：
// 200 请求正常【计费】：score≠-1 → 001；score=-1 → 999（**查无也计费**，由
// billing.TableFor("fqz") 决定）；400/402/403/404/500 与 POP 网关错误 → 上游侧错误。
func TestAntiFraudNormalization(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCode  string // 001 / 999；空表示期望 error
		wantRange string
		wantErr   string // 期望 UpstreamError.Code
	}{
		{name: "200 score=258 → 001", body: `{"Message":"OK","RequestId":"RID","Data":{"score":"258"},"Code":200}`, wantCode: "001", wantRange: `{"score":"258"}`},
		{name: "200 score 数字形态 → 001", body: `{"Message":"OK","RequestId":"RID","Data":{"score":800},"Code":200}`, wantCode: "001", wantRange: `{"score":800}`},
		{name: "200 Data 为 JSON 字符串 → 001", body: `{"Message":"OK","RequestId":"RID","Data":"{\"score\":\"300\"}","Code":200}`, wantCode: "001", wantRange: `{"score":"300"}`},
		{name: "200 score=-1 → 999 计费", body: `{"Message":"OK","RequestId":"RID","Data":{"score":"-1"},"Code":200}`, wantCode: "999"},
		{name: "200 Data 缺失 → error", body: `{"Message":"OK","RequestId":"RID","Code":200}`, wantErr: "200"},
		{name: "200 score 非数值 → error", body: `{"Message":"OK","RequestId":"RID","Data":{"score":"abc"},"Code":200}`, wantErr: "200"},
		{name: "400 参数不合法", body: `{"Message":"bad","RequestId":"RID","Code":400}`, wantErr: "400"},
		{name: "402 限流", body: `{"Message":"qps","RequestId":"RID","Code":402}`, wantErr: "402"},
		{name: "403 权限不足", body: `{"Message":"forbidden","RequestId":"RID","Code":403}`, wantErr: "403"},
		{name: "404 Service 不合法", body: `{"Message":"nf","RequestId":"RID","Code":404}`, wantErr: "404"},
		{name: "500 系统错误", body: `{"Message":"err","RequestId":"RID","Code":500}`, wantErr: "500"},
		{name: "POP 网关签名错误", body: `{"RequestId":"RID","HostId":"saf.cn-shanghai.aliyuncs.com","Code":"SignatureDoesNotMatch","Message":"sig"}`, wantErr: "SignatureDoesNotMatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := antiFraudServer(t, nil, tc.body)
			defer srv.Close()
			c := NewAntiFraud(AntiFraudConfig{BaseURL: srv.URL, AccessKeyID: "ak", AccessKeySecret: "sk"}, srv.Client())
			res, err := c.Query(context.Background(), antiFraudReq)
			if tc.wantErr != "" {
				var ue *model.UpstreamError
				if !errors.As(err, &ue) {
					t.Fatalf("期望 *model.UpstreamError, 实际 res=%+v err=%v", res, err)
				}
				if ue.Code != tc.wantErr || ue.UID != "RID" || ue.LogID != "RID" {
					t.Fatalf("UpstreamError = %+v, 期望 code=%s uid=logId=RID", ue, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if res.Code != tc.wantCode || res.UID != "RID" || res.LogID != "RID" || res.Reqid != "r1" {
				t.Fatalf("result = %+v", res)
			}
			if res.Range != tc.wantRange {
				t.Fatalf("range = %q, 期望 %q", res.Range, tc.wantRange)
			}
		})
	}
}
