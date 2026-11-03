package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/common/xlsx"
	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/report"
)

// registerAdminRoutes mounts the admin console API + the SPA static files (§16).
// 登录与 JWT 校验走统一控制面 (s.control = x1)；用户/审计数据按版本作用域
// /admin/api/{ver}/... 路由到对应版本独立的后台服务 (各连独立数据库)。
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	if s.control == nil {
		return
	}
	mux.HandleFunc("POST /admin/api/login", s.adminLogin)

	mux.HandleFunc("GET /admin/api/{ver}/users", s.requireAdmin(s.adminListUsers))
	mux.HandleFunc("POST /admin/api/{ver}/users", s.requireAdmin(s.adminCreateUser))
	mux.HandleFunc("GET /admin/api/{ver}/users/{id}", s.requireAdmin(s.adminGetUser))
	mux.HandleFunc("PATCH /admin/api/{ver}/users/{id}", s.requireAdmin(s.adminUpdateUser))
	mux.HandleFunc("DELETE /admin/api/{ver}/users/{id}", s.requireAdmin(s.adminDeleteUser))
	mux.HandleFunc("POST /admin/api/{ver}/users/{id}/rotate-secret", s.requireAdmin(s.adminRotateSecret))

	mux.HandleFunc("GET /admin/api/{ver}/audits", s.requireAdmin(s.adminListAudits))
	mux.HandleFunc("GET /admin/api/{ver}/stats", s.requireAdmin(s.adminUsageStats))

	// 出账/对账导出 (§16.6，仅计数)：下游账单跨所有路由 (Server 级)，上游对账按
	// 单条路由 (=一个上游)。两者都只读 audit_log，不落任何原始返回。
	mux.HandleFunc("GET /admin/api/reports/downstream", s.requireAdmin(s.adminReportDownstream))
	mux.HandleFunc("GET /admin/api/{ver}/reports/upstream", s.requireAdmin(s.adminReportUpstream))

	if s.spaDir != "" {
		mux.HandleFunc("GET /admin/", s.serveSPA)
		mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
		})
	}
}

// adminFor resolves the version-scoped admin service from the {ver} path value.
// Returns (nil,false) for an unknown version, in which case it has already
// written a 404 response.
func (s *Server) adminFor(w http.ResponseWriter, r *http.Request) (*admin.Service, bool) {
	ver := r.PathValue("ver")
	if !model.ValidVersion(ver) {
		writeAdminError(w, http.StatusNotFound, "未知版本: "+ver)
		return nil, false
	}
	st := s.stacks[ver]
	if st == nil || st.Admin == nil {
		writeAdminError(w, http.StatusNotFound, "版本未启用: "+ver)
		return nil, false
	}
	return st.Admin, true
}

// --- §16.1 login ---

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	token, exp, err := s.control.Login(r.Context(), in.Username, in.Password)
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"token": token, "expireAt": exp})
}

// --- §16.2 users ---

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	// ?q= 支持按 uuid(appKey)/名称/手机号检索 (DESIGN §16.2)。
	users, err := svc.SearchUsers(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	u, err := svc.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminError(w, http.StatusNotFound, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, u)
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	res, err := svc.CreateUser(r.Context(), admin.CreateUserInput{
		Name:   in.Name,
		Mobile: in.Mobile,
	})
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, res)
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	u, err := svc.UpdateUser(r.Context(), r.PathValue("id"), admin.UpdateUserInput{
		Status: in.Status,
		Mobile: in.Mobile,
	})
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, u)
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteUser(r.Context(), r.PathValue("id")); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminRotateSecret(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	secret, err := svc.RotateSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"secret": secret})
}

// --- §16.3 audits ---

func (s *Server) adminListAudits(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := model.AuditFilter{
		AppKey: q.Get("appKey"),
		Reqid:  strings.TrimSpace(q.Get("reqid")),
		Limit:  atoiDefault(q.Get("limit"), 100),
		Offset: atoiDefault(q.Get("offset"), 0),
	}
	if bc := q.Get("busiCode"); bc != "" {
		if n, err := strconv.Atoi(bc); err == nil {
			f.BusiCode = &n
		}
	}
	// 按年/月/日查看：前端把选中周期换算成 [from, to) 的 RFC3339 边界传入。
	f.From = parseTimeParam(q.Get("from"))
	f.To = parseTimeParam(q.Get("to"))
	// 成败过滤：success=仅查得数据，fail=仅未查得(查无/错误)，空=全部。
	switch q.Get("status") {
	case "success":
		t := true
		f.Success = &t
	case "fail", "failure":
		fl := false
		f.Success = &fl
	}
	// ?q= 按 uuid(appKey)/名称/手机号过滤：先解析为匹配的 appKey 集合 (DESIGN §16.3)。
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		matched, err := svc.SearchUsers(r.Context(), kw)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		appKeys := make([]string, 0, len(matched))
		for _, u := range matched {
			appKeys = append(appKeys, u.AppKey)
		}
		if len(appKeys) == 0 {
			// 无匹配用户：直接返回空结果，避免退化为全量查询。
			writeAdminJSON(w, http.StatusOK, map[string]any{"audits": []*model.AuditRecord{}, "total": 0})
			return
		}
		f.AppKeys = appKeys
	}
	// total 用于前端分页 (CountAudits 忽略 Limit/Offset，与 ListAudits 口径一致)。
	total, err := svc.CountAudits(r.Context(), f)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	audits, err := svc.ListAudits(r.Context(), f)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"audits": audits, "total": total})
}

// --- §16.4 usage stats (按年/月/日 请求次数与成功次数) ---

func (s *Server) adminUsageStats(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := model.StatsFilter{
		Granularity: model.StatsGranularity(q.Get("granularity")),
		From:        parseTimeParam(q.Get("from")),
		To:          parseTimeParam(q.Get("to")),
		Limit:       atoiDefault(q.Get("limit"), 0),
	}
	// ?q= 按 uuid(appKey)/名称/手机号过滤：先解析为匹配的 appKey 集合，再限定统计范围。
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		matched, err := svc.SearchUsers(r.Context(), kw)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		appKeys := make([]string, 0, len(matched))
		for _, u := range matched {
			appKeys = append(appKeys, u.AppKey)
		}
		if len(appKeys) == 0 {
			writeAdminJSON(w, http.StatusOK, map[string]any{"stats": []*model.UsageStat{}})
			return
		}
		f.AppKeys = appKeys
	}
	stats, err := svc.UsageStats(r.Context(), f)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

// --- §16.6 出账/对账导出 (仅计数，Excel) ---

// reportBeijing 是导出文件名时间戳用的北京时区。
var reportBeijing = time.FixedZone("CST", 8*3600)

// adminReportDownstream 跨所有路由出下游账单：按 客户 × 路由 统计调用/查得/查询/
// 计费次数。customer 空=全部客户。遍历每个版本独立的 admin 服务 (各连独立库)。
func (s *Server) adminReportDownstream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, err := report.ParseRange(q.Get("from"), q.Get("to"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	customer := strings.TrimSpace(q.Get("customer"))
	names := map[string]string{}
	var routes []report.RouteData
	detailBudget := report.MaxRows + 1
	for _, ver := range model.Versions {
		st := s.stacks[ver]
		if st == nil || st.Admin == nil {
			continue
		}
		var appKeys []string
		if customer != "" {
			matched, err := st.Admin.SearchUsers(r.Context(), customer)
			if err != nil {
				writeAdminError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, u := range matched {
				if u.Name != customer { // SearchUsers 为子串匹配；下游账单按精确客户名
					continue
				}
				appKeys = append(appKeys, u.AppKey)
				names[u.AppKey] = u.Name
			}
			if len(appKeys) == 0 {
				continue // 该路由下没有此客户
			}
		} else {
			users, err := st.Admin.ListUsers(r.Context())
			if err != nil {
				writeAdminError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, u := range users {
				names[u.AppKey] = u.Name
			}
		}
		aggs, err := st.Admin.BillingSummary(r.Context(), appKeys, from, to)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(aggs) == 0 {
			continue
		}
		for _, a := range aggs {
			if a.Name == "" {
				a.Name = names[a.AppKey]
			}
		}
		var details []*model.AuditRecord
		if detailBudget > 0 {
			details, err = st.Admin.ReportAudits(r.Context(), model.AuditFilter{AppKeys: appKeys, From: from, To: to, Limit: detailBudget})
			if err != nil {
				writeAdminError(w, http.StatusInternalServerError, err.Error())
				return
			}
			detailBudget -= len(details)
		}
		routes = append(routes, report.RouteData{Route: ver, Label: strings.ToUpper(ver), Aggs: aggs, Details: details})
	}
	wb := report.BuildDownstream(routes, names, customer, from, to, time.Now())
	writeWorkbook(w, "下游账单", wb)
}

// adminReportUpstream 按单条路由 (=一个上游) 出对账单：只统计真正调上游且非走缓存
// 的行，给出调用/查得/计费/查无汇总与逐条明细。
func (s *Server) adminReportUpstream(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.adminFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, to, err := report.ParseRange(q.Get("from"), q.Get("to"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	called := true
	audits, err := svc.ReportAudits(r.Context(), model.AuditFilter{
		CalledUpstream: &called,
		ExcludeCache:   true,
		From:           from,
		To:             to,
		Limit:          report.MaxRows + 1,
	})
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := map[string]string{}
	if users, err := svc.ListUsers(r.Context()); err == nil {
		for _, u := range users {
			names[u.AppKey] = u.Name
		}
	}
	route := svc.Route()
	wb := report.BuildUpstream(route, strings.ToUpper(route), audits, names, from, to, time.Now())
	writeWorkbook(w, "上游对账-"+route, wb)
}

// writeWorkbook 以 attachment 形式回写 xlsx (文件名带北京时间戳)。
func writeWorkbook(w http.ResponseWriter, base string, wb *xlsx.Workbook) {
	fname := base + "-" + time.Now().In(reportBeijing).Format("20060102-150405") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fname))
	_ = wb.Write(w)
}

// parseTimeParam 解析 RFC3339 时间参数，非法/空返回零值 (表示不限)。
func parseTimeParam(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

// --- SPA static serving (§16.0) ---

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/admin/")
	clean := filepath.Clean(rel)
	if clean == "." || clean == "/" || strings.HasPrefix(clean, "..") {
		clean = "index.html"
	}
	full := filepath.Join(s.spaDir, clean)
	if fi, err := os.Stat(full); err != nil || fi.IsDir() {
		// SPA fallback: serve index.html for client-side routes.
		full = filepath.Join(s.spaDir, "index.html")
	}
	http.ServeFile(w, r, full)
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAdminError(w http.ResponseWriter, status int, msg string) {
	writeAdminJSON(w, status, map[string]any{"error": msg})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
