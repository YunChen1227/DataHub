package api

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/persistence/memory"
)

// TestReportEndpointsSmoke 构造内存后端的 Server，验证出账/对账路由注册无冲突、
// 鉴权通过、并返回合法 xlsx (zip)。同时覆盖「每条路由单独计费 + 查询计费口径」。
func TestReportEndpointsSmoke(t *testing.T) {
	store := memory.New()
	ctx := context.Background()

	// 两条路由各一客户 (同 appKey)，blk 为查询计费 (查无也计费)。
	store.SeedLicense(&model.LicenseView{LicenseID: "LIC1", AppKey: "ak1", ClientUUID: "u", Status: "ACTIVE"}, "sec", "客户甲", "13800000000")
	now := time.Now()
	mustAudit(t, store, "x1", "ak1", "a1", true, true, true, false, now)
	mustAudit(t, store, "x1", "ak1", "a2", true, false, false, false, now)
	mustAudit(t, store, "blk", "ak1", "b1", true, false, true, false, now) // 查询计费
	mustAudit(t, store, "blk", "ak1", "b2", false, true, true, true, now)  // 走缓存 (上游对账应排除)

	cfg := admin.Config{JWTSecret: "test-secret"}
	control := admin.New("x1", store, store, store, cfg)
	if err := control.BootstrapAdmin(ctx, "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	token, _, err := control.Login(ctx, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}

	stacks := map[string]*VersionStack{
		"x1":  {Admin: control},
		"blk": {Admin: admin.New("blk", store, store, store, cfg)},
	}
	srv := NewServer(stacks, control, "")
	h := srv.Routes()

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{
		"/admin/api/reports/downstream?from=2020-01-01&to=2020-01-01&customer=客户甲",
		"/admin/api/blk/reports/upstream?from=2020-01-01&to=2020-01-01",
	} {
		rec := do(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s -> %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	// 用真实区间导出下游账单并校验是合法 xlsx。
	from := now.Add(-time.Hour).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	to := now.Add(time.Hour).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	rec := do("/admin/api/reports/downstream?from=" + from + "&to=" + to)
	if rec.Code != http.StatusOK {
		t.Fatalf("downstream 导出失败 %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len())); err != nil {
		t.Fatalf("下游账单不是合法 xlsx: %v", err)
	}

	// 未带令牌应 401。
	req := httptest.NewRequest(http.MethodGet, "/admin/api/reports/downstream?from="+from+"&to="+to, nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, got %d", rec2.Code)
	}
}

func mustAudit(t *testing.T, s *memory.Store, ver, appKey, reqid string, called, found, billed, cache bool, when time.Time) {
	t.Helper()
	rec := &model.AuditRecord{
		RequestID: "req-" + reqid, Version: ver, AppKey: appKey, Reqid: reqid,
		CalledUpstream: called, FoundData: found, Billed: billed, FromCache: cache,
		UpstreamUID: "uid-" + reqid, UpstreamLogID: "log-" + reqid, UpstreamCode: "001",
		CreatedAt: when,
	}
	if err := s.AppendAudit(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}
