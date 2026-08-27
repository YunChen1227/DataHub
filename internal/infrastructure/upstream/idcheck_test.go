package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 上游文档的签名例子逐字验算：appid=xyzxyzxyz、timestamp=1555378976238、
// app_security=efcefcefcefcefc → sign=4e7e1974b79f3656aeaf03f1158f5d5d。
// 拼接分隔符或顺序写错，线上表现是「每个请求都 400 参数错误」。
func TestIDCheckSignMatchesDocExample(t *testing.T) {
	got := signFaceCompare("xyzxyzxyz", "1555378976238", "efcefcefcefcefc")
	const want = "4e7e1974b79f3656aeaf03f1158f5d5d"
	if got != want {
		t.Fatalf("sign = %s, 期望 %s", got, want)
	}
}

// 文档注「sign 不满 32 位需补 0」——这是 Java BigInteger 取十六进制会吃掉前导零的
// 修补说明。Go 的 hex.EncodeToString 恒定输出 32 位，本测试守住这个前提：若哪天
// 改成 big.Int 之类的实现，前导零场景会立刻失败。
func TestIDCheckSignAlwaysThirtyTwoHex(t *testing.T) {
	for i := 0; i < 2000; i++ {
		s := signFaceCompare("appid", strconv.Itoa(i), "secret")
		if len(s) != 32 {
			t.Fatalf("i=%d sign=%q 长度 %d, 期望 32", i, s, len(s))
		}
	}
}

// 请求侧契约：必须是 POST form（文档明确「不要 json 方式」）、带齐
// appid/timestamp/sign/name/idcard，且首版不发 secretMode（明文传参）。
func TestIDCheckRequestIsSignedPostForm(t *testing.T) {
	var (
		gotMethod, gotCT string
		gotForm          map[string]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotForm = map[string]string{}
		for k := range r.PostForm {
			gotForm[k] = r.PostForm.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"msg":"成功","success":true,"code":200,"data":{"result":0,"order_no":"O-1","desc":"一致"}}`))
	}))
	defer srv.Close()

	c := NewIDCheck(IDCheckConfig{BaseURL: srv.URL, AppID: "ap1", AppSecret: "sec1"}, srv.Client())
	if _, err := c.Query(context.Background(), &model.UpstreamRequest{
		Name: "张三", IDCard: "330129199109094312", Reqid: "r1",
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, 期望 POST", gotMethod)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, 期望 application/x-www-form-urlencoded", gotCT)
	}
	if gotForm["appid"] != "ap1" {
		t.Fatalf("appid = %q", gotForm["appid"])
	}
	if gotForm["name"] != "张三" || gotForm["idcard"] != "330129199109094312" {
		t.Fatalf("业务字段错: name=%q idcard=%q", gotForm["name"], gotForm["idcard"])
	}
	if _, ok := gotForm["secretMode"]; ok {
		t.Fatal("首版明文传参不应发 secretMode（发了上游会按 AES 解密业务字段）")
	}
	if want := md5Hex("ap1&" + gotForm["timestamp"] + "&sec1"); gotForm["sign"] != want {
		t.Fatalf("sign = %q, 期望 %q", gotForm["sign"], want)
	}
}

// 归一化口径逐条对齐文档「返回字段说明」：result 0/1 明标收费 → 001 计费；
// 2 无记录未标收费 → 999 查无；code≠200 与 result 缺失/越界 → 上游侧错误不计费。
// 这张表是计费正确性的唯一防线，改动前先回上游文档核对。
func TestIDCheckNormalization(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string // "" 表示期望 error
		wantUID  string
	}{
		{
			name:     "result=0 一致 → 001 计费",
			body:     `{"msg":"成功","success":true,"code":200,"data":{"result":0,"order_no":"O-0","desc":"一致","sex":"男","birthday":"199***20","address":"江西省**市**区"}}`,
			wantCode: "001", wantUID: "O-0",
		},
		{
			name:     "result=1 不一致 → 001 计费（上游给出确定结论，照样收费）",
			body:     `{"msg":"成功","success":true,"code":200,"data":{"result":1,"order_no":"O-1","desc":"不一致"}}`,
			wantCode: "001", wantUID: "O-1",
		},
		{
			name:     "result=2 无记录 → 999 查无不计费",
			body:     `{"msg":"成功","success":true,"code":200,"data":{"result":2,"order_no":"O-2"}}`,
			wantCode: "999", wantUID: "O-2",
		},
		{
			name: "code=400 参数错误 → 上游侧错误",
			body: `{"msg":"参数错误","success":false,"code":400,"data":{}}`,
		},
		{
			name: "code=603 余额不足 → 上游侧错误",
			body: `{"msg":"余额不足请充值","success":false,"code":603,"data":{}}`,
		},
		{
			name: "code=200 但 data.result 缺失 → 上游侧错误（不得退化成 result=0 误计费）",
			body: `{"msg":"成功","success":true,"code":200,"data":{"order_no":"O-X"}}`,
		},
		{
			name: "result 超出 0/1/2 枚举 → 上游侧错误",
			body: `{"msg":"成功","success":true,"code":200,"data":{"result":7,"order_no":"O-7"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewIDCheck(IDCheckConfig{BaseURL: srv.URL, AppID: "ap1", AppSecret: "sec1"}, srv.Client())
			res, err := c.Query(context.Background(), &model.UpstreamRequest{
				Name: "张三", IDCard: "330129199109094312", Reqid: "r1",
			})

			if tc.wantCode == "" {
				if err == nil {
					t.Fatalf("期望上游侧错误，实际成功: code=%s range=%s", res.Code, res.Range)
				}
				var be *model.UpstreamError
				if !errors.As(err, &be) {
					t.Fatalf("期望 *model.UpstreamError（带上游 code/uid 落审计），实际 %T: %v", err, err)
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

// 上游订单号 order_no 只进审计(UID/LogID)，绝不能出现在下游 result.range 里；
// 业务字段则要原样透出。
func TestIDCheckRangeStripsOrderNo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"msg":"成功","success":true,"code":200,"data":{"result":1,"order_no":"62607******58391552","desc":"不一致","sex":"男","birthday":"199***20","address":"江西省**市**区"}}`))
	}))
	defer srv.Close()

	c := NewIDCheck(IDCheckConfig{BaseURL: srv.URL, AppID: "ap1", AppSecret: "sec1"}, srv.Client())
	res, err := c.Query(context.Background(), &model.UpstreamRequest{
		Name: "张三", IDCard: "330129199109094312", Reqid: "r1",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.UID != "62607******58391552" {
		t.Fatalf("UID = %q, 期望上游 order_no 落审计", res.UID)
	}
	assertRangeOmits(t, res.Range, "order_no", "62607******58391552")
	assertRangeHas(t, res.Range, "result", "desc", "sex", "birthday", "address")
}

func assertRangeOmits(t *testing.T, rng string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(rng, n) {
			t.Fatalf("result.range 不应包含 %q: %s", n, rng)
		}
	}
}

func assertRangeHas(t *testing.T, rng string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !strings.Contains(rng, `"`+k+`"`) {
			t.Fatalf("result.range 缺少业务字段 %q: %s", k, rng)
		}
	}
}
