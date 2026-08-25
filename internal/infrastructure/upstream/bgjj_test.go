package upstream

import (
	"context"
	"crypto/md5" //nolint:gosec
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 加签必须与官方 demo SignUtil.getSign 逐字一致：顶层键 ASCII 升序、剔空值、
// value=Java map toString 形态，末尾 &key=merchantKey，MD5 小写 hex。
func TestSignBgJJMatchesDemoAlgorithm(t *testing.T) {
	top := []kv{
		{"dsorderid", "1787125569662b68c96913e5"},
		{"merchant_id", "0000000000005077"},
		{"params", javaMapString([]kv{{"name", "张三"}, {"idcard", "abc"}, {"mobile", "138"}})},
		{"timestamp", "1787125569662"},
	}
	got := signBgJJ(top, "P8rT2wXyZ9aBcDeFgHiJkLmNoPqRsTuV")

	// 独立复算：手工拼出 demo 的待签名串再 MD5，交叉验证 signBgJJ 的实现。
	want := md5hex("dsorderid=1787125569662b68c96913e5&merchant_id=0000000000005077&" +
		"params={name=张三, idcard=abc, mobile=138}&timestamp=1787125569662&" +
		"key=P8rT2wXyZ9aBcDeFgHiJkLmNoPqRsTuV")
	if got != want {
		t.Fatalf("sign mismatch:\n got=%s\nwant=%s", got, want)
	}
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// javaMapString 必须产出 "{k1=v1, k2=v2}"（逗号+空格，无引号）。
func TestJavaMapStringFormat(t *testing.T) {
	got := javaMapString([]kv{{"name", "张三"}, {"idcard", "x"}, {"mobile", "y"}})
	want := "{name=张三, idcard=x, mobile=y}"
	if got != want {
		t.Fatalf("javaMapString=%q want %q", got, want)
	}
}

// orderedParamsJSON 必须保持 name→idcard→mobile 键序（上游解析后 toString 才能重算 sign）。
func TestOrderedParamsJSONKeepsOrder(t *testing.T) {
	got := string(orderedParamsJSON([]kv{{"name", "张三"}, {"idcard", "x"}, {"mobile", "y"}}))
	want := `{"name":"张三","idcard":"x","mobile":"y"}`
	if got != want {
		t.Fatalf("orderedParamsJSON=%s want %s", got, want)
	}
}

// normalizeRange：备用源 {date,score,jfzt} → 下游契约 {cbjfzt,jfjs,jfsj}（严格白名单）。
func TestBgJJNormalizeRangeMapping(t *testing.T) {
	c := &BgJJClient{}
	rng, err := c.normalizeRange(json.RawMessage(`{"date":"202606","score":"17","jfzt":"1","extra":"ignored"}`))
	if err != nil {
		t.Fatalf("normalizeRange: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(rng), &m); err != nil {
		t.Fatalf("range not json: %v", err)
	}
	if m["cbjfzt"] != "1" || m["jfjs"] != "17" || m["jfsj"] != "202606" {
		t.Fatalf("mapping wrong: %v", m)
	}
	if _, ok := m["extra"]; ok {
		t.Fatalf("白名单被破坏：上游多余字段 extra 泄漏进 range")
	}
	if len(m) != 3 {
		t.Fatalf("range 应只含 3 个契约字段，got %v", m)
	}
}

// 查得 (100)：Query 归一为 001，range 为映射后业务对象，UID/LogID 都带 orderid。
func TestBgJJQueryFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		// 校验请求体：params 键序、sign 存在。
		if !strings.Contains(string(raw), `"params":{"name":"张三","idcard":"330129199109094312","mobile":"13809091009"}`) {
			t.Errorf("params 键序/内容不符: %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":"100","message":"查询成功","orderid":"ORD-1","dsorderid":"ds1","data":{"date":"202606","score":"17","jfzt":"1"}}`)
	}))
	defer srv.Close()

	c, err := NewBgJJ(BgJJConfig{BaseURL: srv.URL, MerchantID: "M1", MerchantKey: "K1"}, srv.Client())
	if err != nil {
		t.Fatalf("NewBgJJ: %v", err)
	}
	res, err := c.Query(context.Background(), &model.UpstreamRequest{
		Name: "张三", IDCard: "330129199109094312", Mobile: "13809091009", Reqid: "rq1",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Code != "001" || res.UID != "ORD-1" || res.LogID != "ORD-1" {
		t.Fatalf("want 001/ORD-1/ORD-1, got %s/%s/%s", res.Code, res.UID, res.LogID)
	}
	if !strings.Contains(res.Range, `"cbjfzt":"1"`) || !strings.Contains(res.Range, `"jfjs":"17"`) || !strings.Contains(res.Range, `"jfsj":"202606"`) {
		t.Fatalf("range 映射不对: %s", res.Range)
	}
}

// 查无 (201)：Query 归一为 999，带 orderid。
func TestBgJJQueryNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"201","message":"查无记录","orderid":"ORD-2","dsorderid":"ds2","data":{}}`)
	}))
	defer srv.Close()
	c, _ := NewBgJJ(BgJJConfig{BaseURL: srv.URL, MerchantID: "M1", MerchantKey: "K1"}, srv.Client())
	res, err := c.Query(context.Background(), &model.UpstreamRequest{Name: "李四", IDCard: "330129199109094312", Mobile: "13800000000", Reqid: "rq2"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Code != "999" || res.UID != "ORD-2" {
		t.Fatalf("want 999/ORD-2, got %s/%s", res.Code, res.UID)
	}
}

// 上游侧错误 (301 非白名单IP)：返回 *model.UpstreamError，带上 code/orderid 供对账。
func TestBgJJQueryUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"301","message":"非白名单IP","orderid":"ORD-3","dsorderid":"ds3","data":null}`)
	}))
	defer srv.Close()
	c, _ := NewBgJJ(BgJJConfig{BaseURL: srv.URL, MerchantID: "M1", MerchantKey: "K1"}, srv.Client())
	_, err := c.Query(context.Background(), &model.UpstreamRequest{Name: "王五", IDCard: "330129199109094312", Mobile: "13809091009", Reqid: "rq3"})
	if err == nil {
		t.Fatal("want error")
	}
	var ue *model.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("want *model.UpstreamError, got %T", err)
	}
	if ue.Code != "301" || ue.UID != "ORD-3" || ue.LogID != "ORD-3" {
		t.Fatalf("失败也要可追查：code=%s uid=%s logid=%s", ue.Code, ue.UID, ue.LogID)
	}
}
