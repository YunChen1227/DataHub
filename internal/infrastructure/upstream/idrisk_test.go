package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 应诺尔 PDF §3.2 把待签名字符串逐字写了出来：业务参数 idCard/mobile/tradeNo
// （值均为 Demo 里的字面量）+ 末尾 secret 拼成
//
//	idCardce62d2fdd5564508386384fda0cee40fmobile2ea01442a7eebf816aef379c74c0fa53tradeNo457fa900b705291ada6secret
//
// 本测试用该字符串反向验算 signGama 的拼接顺序与 secret 位置。拼错的线上表现是
// 「每个请求都被上游判为签名错」——故用文档原文钉死，不许凭直觉改。
func TestIDRiskSignMatchesDocExample(t *testing.T) {
	body := map[string]string{
		"idCard":  "ce62d2fdd5564508386384fda0cee40f",
		"mobile":  "2ea01442a7eebf816aef379c74c0fa53",
		"tradeNo": "457fa900b705291ada6",
	}
	want := md5Hex("idCardce62d2fdd5564508386384fda0cee40f" +
		"mobile2ea01442a7eebf816aef379c74c0fa53" +
		"tradeNo457fa900b705291ada6" + "secret")
	if got := signGama(body, "secret"); got != want {
		t.Fatalf("sign = %s, 期望 %s", got, want)
	}
}

// 请求侧契约 (应诺尔 PDF §1.4 请求结构 + §2.2 body 参数表)：JSON POST、
// apiKey 固定 idRiskTagV107、encryptionType=2 时 name/idCard 传 MD5 摘要、
// body 里**只有** name+idCard 两项（无 mobile、无 tradeNo），sign 覆盖实发 body。
func TestIDRiskRequestIsSignedJSONPost(t *testing.T) {
	var (
		gotMethod, gotCT string
		gotEnv           struct {
			EncryptionType int               `json:"encryptionType"`
			AppID          string            `json:"appId"`
			Sign           string            `json:"sign"`
			APIKey         string            `json:"apiKey"`
			Body           map[string]string `json:"body"`
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotEnv)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"请求成功","seqNo":"S-1","data":{"busiCode":10,"busiMsg":"success","result":{"detail":["A0"]}}}`))
	}))
	defer srv.Close()

	c := NewIDRisk(IDRiskConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
	if _, err := c.Query(context.Background(), &model.UpstreamRequest{
		Name: "张三", IDCard: "310000199001010010", Mobile: "13900000000", Reqid: "r1",
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, 期望 POST", gotMethod)
	}
	if gotCT != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if gotEnv.APIKey != IDRiskAPIKey {
		t.Fatalf("apiKey = %q, 期望固定值 %q", gotEnv.APIKey, IDRiskAPIKey)
	}
	if gotEnv.EncryptionType != 2 {
		t.Fatalf("encryptionType = %d, 期望缺省 2 (MD5)", gotEnv.EncryptionType)
	}
	// encryptionType=2：PII 必须是 MD5 摘要而非明文。发明文等于把身份信息裸奔外发。
	if gotEnv.Body["name"] != md5Hex("张三") {
		t.Fatalf("body.name = %q, 期望 MD5 摘要 %q", gotEnv.Body["name"], md5Hex("张三"))
	}
	if gotEnv.Body["idCard"] != md5Hex("310000199001010010") {
		t.Fatalf("body.idCard = %q, 期望 MD5 摘要", gotEnv.Body["idCard"])
	}
	// 本产品参数表没有 mobile：即便 UpstreamRequest 里带了手机号也不许发出去
	// （多发一个参数会连带把 sign 算错，上游直接判签名失败）。
	if _, leaked := gotEnv.Body["mobile"]; leaked {
		t.Fatalf("body 不应含 mobile（本产品参数表只有 name/idCard/tradeNo）: %v", gotEnv.Body)
	}
	if len(gotEnv.Body) != 2 {
		t.Fatalf("body 应只有 name+idCard 两项, 实际 %v", gotEnv.Body)
	}
	if want := signGama(gotEnv.Body, "sec1"); gotEnv.Sign != want {
		t.Fatalf("sign = %q, 期望 %q", gotEnv.Sign, want)
	}
}

// encryptionType=1 时按文档走明文，不得仍旧发摘要。
func TestIDRiskPlaintextModeSendsRawPII(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Body map[string]string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		body = env.Body
		_, _ = w.Write([]byte(`{"code":0,"seqNo":"S-2","data":{"busiCode":10,"result":{"detail":["A0"]}}}`))
	}))
	defer srv.Close()

	c := NewIDRisk(IDRiskConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1", EncryptionType: 1}, srv.Client())
	if _, err := c.Query(context.Background(), &model.UpstreamRequest{
		Name: "张三", IDCard: "310000199001010010", Reqid: "r1",
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if body["name"] != "张三" || body["idCard"] != "310000199001010010" {
		t.Fatalf("明文模式应原样发送, 实际 %v", body)
	}
}

// 归一化口径逐条对齐 docs/身份风险V107(1).pdf §1.6 busiCode 表：
//
//	10   查询成功【计费】     → 001 查得
//	1000 数据未查得【计费】   → 999 查无（**该路由查无也收费**，由 billing.TableFor
//	                            ("sffx") 决定；归一码本身仍如实为 999）
//	1001 账户余额不足 / 1002 账户信息不存在 / 1003 appId 异常 / 1004 产品编号异常
//	1005 账号信息异常 / 1006 透支余额已达上限 / 1007 数据请求异常 / 1009 服务尚未开通
//	                          → 上游侧错误，不计费
//	全局 code=-1 响应异常      → 上游侧错误，不计费
//
// 这张表是计费正确性的唯一防线，改动前先回上游文档核对。每条都必须带上游 seqNo
// （成功/查无/失败三条路径都要，供后台向上游对账）。
func TestIDRiskNormalization(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string // "" 表示期望 error
		wantUID  string // 期望 UID 与 LogID 均为该值
	}{
		{
			name:     "busiCode 10 查询成功 → 001 计费",
			body:     `{"code":0,"msg":"请求成功","seqNo":"48u8tgsvwd60","data":{"busiCode":10,"busiMsg":"success","result":{"detail":["A0"]}}}`,
			wantCode: "001", wantUID: "48u8tgsvwd60",
		},
		{
			name:     "busiCode 1000 数据未查得 → 999（查无，本路由仍计费）",
			body:     `{"code":0,"msg":"请求成功","seqNo":"S-1000","data":{"busiCode":1000,"busiMsg":"数据未查得"}}`,
			wantCode: "999", wantUID: "S-1000",
		},
		{
			name:    "busiCode 1001 账户余额不足 → 上游侧错误",
			body:    `{"code":0,"msg":"请求成功","seqNo":"S-1001","data":{"busiCode":1001,"busiMsg":"账户余额不足"}}`,
			wantUID: "S-1001",
		},
		{
			name:    "busiCode 1002 账户信息不存在 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1002","data":{"busiCode":1002,"busiMsg":"账户信息不存在"}}`,
			wantUID: "S-1002",
		},
		{
			name:    "busiCode 1003 appId 异常 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1003","data":{"busiCode":1003,"busiMsg":"appId异常"}}`,
			wantUID: "S-1003",
		},
		{
			name:    "busiCode 1004 产品编号异常 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1004","data":{"busiCode":1004,"busiMsg":"产品编号异常"}}`,
			wantUID: "S-1004",
		},
		{
			name:    "busiCode 1005 账号信息异常 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1005","data":{"busiCode":1005,"busiMsg":"账号信息异常"}}`,
			wantUID: "S-1005",
		},
		{
			name:    "busiCode 1006 透支余额已达上限 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1006","data":{"busiCode":1006,"busiMsg":"透支余额已达上限"}}`,
			wantUID: "S-1006",
		},
		{
			name:    "busiCode 1007 数据请求异常 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1007","data":{"busiCode":1007,"busiMsg":"数据请求异常"}}`,
			wantUID: "S-1007",
		},
		{
			name:    "busiCode 1009 服务尚未开通 → 上游侧错误",
			body:    `{"code":0,"seqNo":"S-1009","data":{"busiCode":1009,"busiMsg":"服务尚未开通"}}`,
			wantUID: "S-1009",
		},
		{
			name:    "全局 code=-1 响应异常 → 上游侧错误",
			body:    `{"code":-1,"msg":"响应异常","seqNo":"S-ERR"}`,
			wantUID: "S-ERR",
		},
		{
			name: "未知 busiCode → 上游侧错误（不得退化成查得误计费）",
			body: `{"code":0,"seqNo":"S-X","data":{"busiCode":9999,"busiMsg":"未知"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewIDRisk(IDRiskConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
			res, err := c.Query(context.Background(), &model.UpstreamRequest{
				Name: "张三", IDCard: "310000199001010010", Reqid: "r1",
			})

			if tc.wantCode == "" {
				if err == nil {
					t.Fatalf("期望上游侧错误，实际成功: code=%s range=%s", res.Code, res.Range)
				}
				var be *model.UpstreamError
				if !errors.As(err, &be) {
					t.Fatalf("期望 *model.UpstreamError（带上游 code/uid 落审计），实际 %T: %v", err, err)
				}
				// 失败路径也必须带上游 seqNo，否则运营无法向上游对账追查。
				if tc.wantUID != "" && (be.UID != tc.wantUID || be.LogID != tc.wantUID) {
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
		})
	}
}

// result 富对象整体经 result.range 透出（blk 模式）：detail 数组原样保留，
// 上游标识 seqNo 只落审计、绝不出现在 range 里。
func TestIDRiskRangeCarriesDetailWithoutUpstreamIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"请求成功","seqNo":"vu6pjf4g0400","data":{"busiCode":10,"busiMsg":"success","result":{"detail":["C2","J3"],"seqNo":"vu6pjf4g0400"}}}`))
	}))
	defer srv.Close()

	c := NewIDRisk(IDRiskConfig{BaseURL: srv.URL, AppID: "ap1", Secret: "sec1"}, srv.Client())
	res, err := c.Query(context.Background(), &model.UpstreamRequest{
		Name: "张三", IDCard: "310000199001010010", Reqid: "r1",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.UID != "vu6pjf4g0400" {
		t.Fatalf("UID = %q, 期望上游 seqNo 落审计", res.UID)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Range), &m); err != nil {
		t.Fatalf("range 不是合法 JSON: %s", res.Range)
	}
	detail, ok := m["detail"].([]any)
	if !ok || len(detail) != 2 || detail[0] != "C2" || detail[1] != "J3" {
		t.Fatalf("range.detail 应原样透出 [C2 J3], 实际 %v", m["detail"])
	}
	assertRangeOmits(t, res.Range, "seqNo", "vu6pjf4g0400")
}

// 未联调对账接口前 Requery 必须返回 Reachable=false，让台账保持 PENDING 交由对账
// 兜底，而不是谎报「上游确认未执行」把该收的钱判成不收。
func TestIDRiskRequeryNotReachable(t *testing.T) {
	c := NewIDRisk(IDRiskConfig{BaseURL: "http://127.0.0.1:1", AppID: "ap1", Secret: "sec1"}, nil)
	rr, err := c.Requery(context.Background(), "r1")
	if err != nil {
		t.Fatalf("Requery: %v", err)
	}
	if rr.Reachable {
		t.Fatal("对账接口未联调前 Reachable 必须为 false")
	}
}
