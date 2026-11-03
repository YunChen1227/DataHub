// Package report 生成出账/对账 Excel（仅计数，不涉金额，只读 audit_log）：
//
//   - 下游账单：按客户 × 路由统计区间内的 调用/查得/查询/计费 次数。DataHub 每条
//     路由单独计费，有的「查得计费」(只对 001 计费)、有的「查询计费」(001+999 都
//     计费)，口径由 billing.BillsNotFound 决定并在报表中标注。
//   - 上游对账：每条路由就是一个上游，统计该路由区间内真正调上游的调用 (已排除
//     「走缓存」回放行)，给出 调用/查得/计费/查无 汇总与逐条明细。
//
// 计费口径结论：audit_log.billed 在所有路径上都等于台账 counted_service，故计费
// 次数可直接由 billed 聚合，无需关联 billing_ledger，也无需落任何上游原始返回。
package report

import (
	"errors"
	"fmt"
	"time"

	"github.com/datahub/relay/internal/common/xlsx"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/model"
)

// MaxRangeDays 是单次导出的最长区间（天）。
const MaxRangeDays = 366

// MaxRows 是单次导出的明细最大行数，超过请缩小区间。
const MaxRows = 300000

// ErrTooLarge：明细行数超过 MaxRows。
var ErrTooLarge = errors.New("导出数据量过大")

var beijing = time.FixedZone("CST", 8*3600)

// ChargeBasis 返回某路由的计费口径标签：查询计费（001+999 都计）或查得计费（只计 001）。
func ChargeBasis(route string) string {
	if billing.BillsNotFound(route) {
		return "查询计费"
	}
	return "查得计费"
}

// ParseRange 把北京时间日期 [from, to]（YYYY-MM-DD，含 to 当天）转成 [From, To)。
func ParseRange(from, to string) (time.Time, time.Time, error) {
	f, err := time.ParseInLocation("2006-01-02", trimSpace(from), beijing)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("起始日期格式应为 YYYY-MM-DD")
	}
	t, err := time.ParseInLocation("2006-01-02", trimSpace(to), beijing)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("截止日期格式应为 YYYY-MM-DD")
	}
	if t.Before(f) {
		return time.Time{}, time.Time{}, fmt.Errorf("截止日期不能早于起始日期")
	}
	t = t.AddDate(0, 0, 1)
	if t.Sub(f) > MaxRangeDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("单次导出区间不能超过 %d 天", MaxRangeDays)
	}
	return f, t, nil
}

// ---------------- 下游账单 ----------------

// RouteData 是单条路由在区间内的聚合与明细，由 handler 逐路由组装后传入。
type RouteData struct {
	Route   string               // 路由名 (x1/v9/...)
	Label   string               // 展示名
	Aggs    []*model.BillingAgg  // 按客户聚合 (Name 已回填)
	Details []*model.AuditRecord // 逐条审计明细 (已按客户/区间过滤)
}

var downstreamSummaryHeaders = []string{"客户", "appKey", "路由", "计费口径", "调用次数", "查得次数", "查询计费次数", "计费次数"}
var downstreamSummaryWidths = []float64{24, 24, 10, 12, 12, 12, 14, 12}

var downstreamDetailHeaders = []string{"时间", "requestId", "客户", "appKey", "路由", "计费口径", "busiCode", "查得", "计费"}
var downstreamDetailWidths = []float64{20, 34, 24, 24, 10, 12, 10, 8, 8}

// BuildDownstream 把各路由的聚合/明细拼成下游账单工作簿。names 为 appKey→客户名
// 的全局映射（用于明细行回填客户名）。customer 为展示用客户名（空=全部客户）。
func BuildDownstream(routes []RouteData, names map[string]string, customer string, from, to, now time.Time) *xlsx.Workbook {
	if customer == "" {
		customer = "全部客户"
	}
	wb := &xlsx.Workbook{}

	meta := [][]any{
		{"客户", customer},
		{"区间（北京时间）", rangeLabel(from, to)},
		{"导出时间", fmtTime(now)},
		{"口径说明", "每条路由单独计费；查得计费=只对查得(001)计费，查询计费=查得(001)与查无(999)都计费"},
		{"计费次数口径", "等同台账 counted_service；查询计费次数=计费次数−查得次数(查得计费路由恒为0)"},
		{},
	}
	hdr := map[int]bool{len(meta): true}
	meta = append(meta, toAny(downstreamSummaryHeaders))

	var totCalls, totFound, totQuery, totBilled int64
	for _, rd := range routes {
		basis := ChargeBasis(rd.Route)
		for _, a := range rd.Aggs {
			query := a.Billed - a.Found
			if query < 0 {
				query = 0
			}
			name := a.Name
			if name == "" {
				name = names[a.AppKey]
			}
			meta = append(meta, []any{name, a.AppKey, rd.Label, basis, a.Calls, a.Found, query, a.Billed})
			totCalls += a.Calls
			totFound += a.Found
			totQuery += query
			totBilled += a.Billed
		}
	}
	meta = append(meta, []any{"合计", "", "", "", totCalls, totFound, totQuery, totBilled})
	sh := wb.Add("汇总", nil, meta, downstreamSummaryWidths)
	sh.HeaderRows = hdr

	var details [][]any
	for _, rd := range routes {
		basis := ChargeBasis(rd.Route)
		for _, a := range rd.Details {
			if len(details) >= MaxRows {
				break
			}
			name := names[a.AppKey]
			details = append(details, []any{
				fmtTime(a.CreatedAt), a.RequestID, name, a.AppKey, rd.Label, basis,
				a.BusiCode, yesNo(a.FoundData), yesNo(a.Billed),
			})
		}
	}
	wb.Add("明细", downstreamDetailHeaders, details, downstreamDetailWidths)
	return wb
}

// ---------------- 上游对账 ----------------

var upstreamDetailHeaders = []string{"时间", "requestId", "客户", "appKey", "上游订单号", "上游请求号", "上游code", "busiCode", "查得", "计费"}
var upstreamDetailWidths = []float64{20, 34, 24, 24, 28, 28, 12, 10, 8, 8}

// BuildUpstream 把某路由(=一个上游)的调用明细拼成对账工作簿。audits 应已限定为
// called_upstream=true 且非 from_cache（真正调了上游的行）。names 为 appKey→客户名。
func BuildUpstream(route, label string, audits []*model.AuditRecord, names map[string]string, from, to, now time.Time) *xlsx.Workbook {
	wb := &xlsx.Workbook{}

	var calls, found, billed int64
	for _, a := range audits {
		calls++
		if a.FoundData {
			found++
		}
		if a.Billed {
			billed++
		}
	}
	empty := calls - found

	meta := [][]any{
		{"上游（路由）", label + "（" + route + "）"},
		{"计费口径", ChargeBasis(route)},
		{"区间（北京时间）", rangeLabel(from, to)},
		{"导出时间", fmtTime(now)},
		{"说明", "仅统计真正调用上游的请求，已排除「走缓存」回放行；查无数=调用总数−查得数"},
		{},
	}
	hdr := map[int]bool{len(meta): true}
	meta = append(meta, []any{"指标", "次数"})
	meta = append(meta,
		[]any{"调用总数", calls},
		[]any{"查得数", found},
		[]any{"查无数", empty},
		[]any{"计费数", billed},
	)
	sh := wb.Add("汇总", nil, meta, []float64{22, 14})
	sh.HeaderRows = hdr

	var details [][]any
	for _, a := range audits {
		if len(details) >= MaxRows {
			break
		}
		name := names[a.AppKey]
		details = append(details, []any{
			fmtTime(a.CreatedAt), a.RequestID, name, a.AppKey,
			a.UpstreamUID, a.UpstreamLogID, a.UpstreamCode, a.BusiCode,
			yesNo(a.FoundData), yesNo(a.Billed),
		})
	}
	wb.Add("明细", upstreamDetailHeaders, details, upstreamDetailWidths)
	return wb
}

// ---------------- 格式化 ----------------

func fmtTime(t time.Time) string { return t.In(beijing).Format("2006-01-02 15:04:05") }

func rangeLabel(from, to time.Time) string {
	return from.In(beijing).Format("2006-01-02") + " 至 " + to.Add(-time.Second).In(beijing).Format("2006-01-02")
}

func yesNo(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			s = s[:len(s)-1]
		} else {
			break
		}
	}
	return s
}
