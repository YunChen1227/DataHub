package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// range 铁律回归：下游 body.result.range 不得出现任何上游标识类字段。
// 这里覆盖各上游真实响应形态里出现过的标识字段，改 sanitize.go 后必须保持通过。
func TestSanitizeRangeStripsUpstreamIdentifiers(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string // 必须被剥掉的上游标识
		present []string // 必须保留的业务字段
	}{
		{
			// facecompare (rlbd1/rlbd2)：order_no 是上游订单号，只进审计不进 range。
			name:    "facecompare 剥 order_no 保留业务字段",
			in:      `{"order_no":"797385997015384952","score":932.26,"msg":"系统判断为同一人","incorrect":100,"sex":"男","birthday":"19930123","address":"江西省吉安地区遂川县"}`,
			absent:  []string{"order_no", "797385997015384952"},
			present: []string{`"score":932.26`, `"incorrect":100`, `"sex":"男"`, `"birthday":"19930123"`, `"address":"江西省吉安地区遂川县"`},
		},
		{
			name:    "驼峰/短横线/大小写变体一并命中",
			in:      `{"orderNo":"A","Trade-No":"B","RequestId":"C","seqNo":"D","reqno":"E","logId":"F","uuid":"G","token":"H","cbjfzt":"1"}`,
			absent:  []string{"orderNo", "Trade-No", "RequestId", "seqNo", "reqno", "logId", "uuid", "token"},
			present: []string{`"cbjfzt":"1"`},
		},
		{
			name:    "嵌套对象与数组任意层级都剥",
			in:      `{"records":[{"callee":"abc","forbid":1,"reqNo":"R1"},{"callee":"def","forbid":0,"detail":{"hitType":"X","serialNo":"S1"}}]}`,
			absent:  []string{"reqNo", "R1", "serialNo", "S1"},
			present: []string{`"callee":"abc"`, `"forbid":1`, `"hitType":"X"`},
		},
		{
			name:    "顶层数组",
			in:      `[{"forbid":1,"orderId":"O1"}]`,
			absent:  []string{"orderId", "O1"},
			present: []string{`"forbid":1`},
		},
		{
			// 上游凭证/签名类字段正常不会出现在结果体，防御性剥离。
			name:    "凭证与签名类字段也剥",
			in:      `{"appId":"x","sign":"y","accountId":"z","prodId":"BJPG-01","jfjs":"17"}`,
			absent:  []string{"appId", "sign", "accountId", "prodId", "BJPG-01"},
			present: []string{`"jfjs":"17"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeRange(json.RawMessage(tc.in))
			if !json.Valid([]byte(got)) {
				t.Fatalf("输出不是合法 JSON: %s", got)
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("上游标识 %q 泄漏进 range: %s", s, got)
				}
			}
			for _, s := range tc.present {
				if !strings.Contains(got, s) {
					t.Errorf("业务字段 %q 被误剥: %s", s, got)
				}
			}
		})
	}
}

// 字段顺序与数字字面量必须原样保留：下游按顺序/精度解析时不能因脱敏而变形。
func TestSanitizeRangePreservesOrderAndNumbers(t *testing.T) {
	got := sanitizeRange(json.RawMessage(`{"b":1,"orderNo":"X","a":932.26,"c":null,"d":true,"e":1e3}`))
	want := `{"b":1,"a":932.26,"c":null,"d":true,"e":1e3}`
	if got != want {
		t.Fatalf("sanitizeRange 改变了顺序或字面量\n got=%s\nwant=%s", got, want)
	}
}

// fail closed：非法 JSON 无法确认里面没有上游标识，宁可置空也不能把原文倒给下游。
func TestSanitizeRangeFailsClosedOnInvalidJSON(t *testing.T) {
	for _, in := range []string{`{"orderNo":`, `not json at all`, `{"a":1}{"b":2}`} {
		if got := sanitizeRange(json.RawMessage(in)); got != "" {
			t.Errorf("非法 JSON %q 应返回空串，got %q", in, got)
		}
	}
	if got := sanitizeRange(nil); got != "" {
		t.Errorf("空输入应返回空串，got %q", got)
	}
}

// 聚合段禁止携带上游信息：失败段只有中性 status，不带上游 code/msg/uid/logId。
func TestAggSectionCarriesNoUpstreamInfo(t *testing.T) {
	sec := classify(nil, busiErr("E1010", "余额不足", "ORD-A", "LOG-A"))
	raw, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("marshal section: %v", err)
	}
	got := string(raw)
	for _, leak := range []string{"E1010", "余额不足", "ORD-A", "LOG-A"} {
		if strings.Contains(got, leak) {
			t.Errorf("上游信息 %q 泄漏进聚合段: %s", leak, got)
		}
	}
	if got != `{"status":"error"}` {
		t.Fatalf("失败段应只有中性 status，got %s", got)
	}
}
