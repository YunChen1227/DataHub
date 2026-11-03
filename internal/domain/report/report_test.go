package report

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/datahub/relay/internal/common/xlsx"
	"github.com/datahub/relay/internal/domain/model"
)

func readSheets(t *testing.T, wb *xlsx.Workbook) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	if err := wb.Write(&buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestChargeBasis(t *testing.T) {
	if ChargeBasis("blk") != "查询计费" { // blk 在 billNotFoundRoutes 中
		t.Error("blk 应为查询计费")
	}
	if ChargeBasis("x1") != "查得计费" {
		t.Error("x1 应为查得计费")
	}
}

func TestParseRange(t *testing.T) {
	from, to, err := ParseRange("2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatal(err)
	}
	// to 为含当天的开区间右界 = 2026-02-01 00:00 +08:00。
	if to.Sub(from) != 31*24*time.Hour {
		t.Fatalf("区间天数不对: %v", to.Sub(from))
	}
	if _, _, err := ParseRange("2026-02-01", "2026-01-01"); err == nil {
		t.Error("截止早于起始应报错")
	}
	if _, _, err := ParseRange("2020-01-01", "2026-01-01"); err == nil {
		t.Error("超过 366 天应报错")
	}
}

func TestBuildDownstream(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, beijing)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, beijing)
	to := time.Date(2026, 11, 2, 0, 0, 0, 0, beijing)
	routes := []RouteData{
		{
			Route: "x1", Label: "X1",
			Aggs: []*model.BillingAgg{{AppKey: "ak1", Name: "客户甲", Calls: 5, Found: 3, Billed: 3}},
			Details: []*model.AuditRecord{
				{RequestID: "r1", AppKey: "ak1", BusiCode: 10, FoundData: true, Billed: true, CreatedAt: now},
			},
		},
		{
			Route: "blk", Label: "BLK", // 查询计费：billed 可大于 found
			Aggs: []*model.BillingAgg{{AppKey: "ak1", Name: "客户甲", Calls: 4, Found: 1, Billed: 4}},
		},
	}
	names := map[string]string{"ak1": "客户甲"}
	wb := BuildDownstream(routes, names, "客户甲", from, to, now)
	files := readSheets(t, wb)
	sum := files["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sum, "查得计费") || !strings.Contains(sum, "查询计费") {
		t.Error("汇总应标注两种计费口径")
	}
	// blk 查询计费次数 = billed(4) - found(1) = 3。
	if !strings.Contains(sum, "<v>3</v>") {
		t.Error("查询计费次数应为 3")
	}
	if !strings.Contains(sum, "客户甲") {
		t.Error("应含客户名")
	}
	// 明细 sheet 存在且含 requestId。
	if d := files["xl/worksheets/sheet2.xml"]; !strings.Contains(d, "r1") {
		t.Error("明细缺少 requestId")
	}
}

func TestBuildUpstream(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, beijing)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, beijing)
	to := time.Date(2026, 11, 2, 0, 0, 0, 0, beijing)
	audits := []*model.AuditRecord{
		{RequestID: "u1", AppKey: "ak1", FoundData: true, Billed: true, UpstreamUID: "UID1", UpstreamLogID: "LOG1", UpstreamCode: "001", BusiCode: 10, CreatedAt: now},
		{RequestID: "u2", AppKey: "ak1", FoundData: false, Billed: true, UpstreamUID: "UID2", UpstreamLogID: "LOG2", UpstreamCode: "999", BusiCode: 1000, CreatedAt: now},
	}
	wb := BuildUpstream("blk", "BLK", audits, map[string]string{"ak1": "客户甲"}, from, to, now)
	files := readSheets(t, wb)
	sum := files["xl/worksheets/sheet1.xml"]
	// 调用总数 2 / 查得 1 / 查无 1 / 计费 2。
	if !strings.Contains(sum, "调用总数") {
		t.Error("汇总缺少指标")
	}
	if !strings.Contains(sum, "<v>2</v>") || !strings.Contains(sum, "<v>1</v>") {
		t.Error("汇总计数不对")
	}
	detail := files["xl/worksheets/sheet2.xml"]
	if !strings.Contains(detail, "UID1") || !strings.Contains(detail, "LOG2") {
		t.Error("明细应含上游订单号/请求号")
	}
}
