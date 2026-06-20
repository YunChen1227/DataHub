package billing

import (
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// TestDecide_BillingScope verifies the口径: 成功查得数 only counts 查得数据 (001);
// 999 查无结果 is Resolved (确定结论 → BILLED) but NOT Returned (不累计查得数).
func TestDecide_BillingScope(t *testing.T) {
	svc := New(DefaultTable())

	cases := []struct {
		name         string
		code         string
		wantResolved bool // 上游确定结论 → 台账 BILLED
		wantReturned bool // 查得数据 → 累计成功查得数
	}{
		{"001 查得数据", "001", true, true},
		{"999 查无结果", "999", true, false},
		{"003 我方原因失败", "003", false, false},
		{"012 接口错误", "012", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := svc.Decide(&model.UpstreamResult{Code: c.code})
			if d.Resolved != c.wantResolved {
				t.Errorf("code=%s Resolved(确定结论)=%v, want %v", c.code, d.Resolved, c.wantResolved)
			}
			if d.Returned != c.wantReturned {
				t.Errorf("code=%s Returned(成功查得数)=%v, want %v", c.code, d.Returned, c.wantReturned)
			}
		})
	}
}
