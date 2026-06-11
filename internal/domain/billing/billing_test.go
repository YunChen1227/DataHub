package billing

import (
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// TestDecide_BillingScope verifies the v0.3 口径: 维度①（对用户计费）only counts
// 查得数据 (001); 999 查无结果 counts 维度②（上游扣费）but NOT 维度①.
func TestDecide_BillingScope(t *testing.T) {
	svc := New(DefaultTable())

	cases := []struct {
		name         string
		code         string
		wantCharged  bool // 维度② 上游扣费
		wantReturned bool // 维度① 对用户计费（查得数据）
	}{
		{"001 查得数据", "001", true, true},
		{"999 查无结果", "999", true, false},
		{"003 我方原因失败", "003", false, false},
		{"012 接口错误", "012", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := svc.Decide(&model.UpstreamResult{Code: c.code})
			if d.Charged != c.wantCharged {
				t.Errorf("code=%s Charged(维度②)=%v, want %v", c.code, d.Charged, c.wantCharged)
			}
			if d.Returned != c.wantReturned {
				t.Errorf("code=%s Returned(维度①对用户计费)=%v, want %v", c.code, d.Returned, c.wantReturned)
			}
		})
	}
}
