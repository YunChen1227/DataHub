package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/model"
)

// registerAdminRoutes mounts the admin console API + the SPA static files (§16).
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	if s.admin == nil {
		return
	}
	mux.HandleFunc("POST /admin/api/login", s.adminLogin)

	mux.HandleFunc("GET /admin/api/users", s.requireAdmin(s.adminListUsers))
	mux.HandleFunc("POST /admin/api/users", s.requireAdmin(s.adminCreateUser))
	mux.HandleFunc("GET /admin/api/users/{id}", s.requireAdmin(s.adminGetUser))
	mux.HandleFunc("PATCH /admin/api/users/{id}", s.requireAdmin(s.adminUpdateUser))
	mux.HandleFunc("DELETE /admin/api/users/{id}", s.requireAdmin(s.adminDeleteUser))
	mux.HandleFunc("POST /admin/api/users/{id}/rotate-secret", s.requireAdmin(s.adminRotateSecret))

	mux.HandleFunc("GET /admin/api/audits", s.requireAdmin(s.adminListAudits))

	if s.spaDir != "" {
		mux.HandleFunc("GET /admin/", s.serveSPA)
		mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
		})
	}
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
	token, exp, err := s.admin.Login(r.Context(), in.Username, in.Password)
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"token": token, "expireAt": exp})
}

// --- §16.2 users ---

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	// ?q= 支持按 uuid(appKey)/名称/手机号检索 (DESIGN §16.2)。
	users, err := s.admin.SearchUsers(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.admin.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminError(w, http.StatusNotFound, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, u)
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string `json:"name"`
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	res, err := s.admin.CreateUser(r.Context(), admin.CreateUserInput{
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
	var in struct {
		Status string `json:"status"`
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	u, err := s.admin.UpdateUser(r.Context(), r.PathValue("id"), admin.UpdateUserInput{
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
	if err := s.admin.DeleteUser(r.Context(), r.PathValue("id")); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminRotateSecret(w http.ResponseWriter, r *http.Request) {
	secret, err := s.admin.RotateSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"secret": secret})
}

// --- §16.3 audits ---

func (s *Server) adminListAudits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.AuditFilter{
		AppKey: q.Get("appKey"),
		Limit:  atoiDefault(q.Get("limit"), 100),
		Offset: atoiDefault(q.Get("offset"), 0),
	}
	if bc := q.Get("busiCode"); bc != "" {
		if n, err := strconv.Atoi(bc); err == nil {
			f.BusiCode = &n
		}
	}
	// ?q= 按 uuid(appKey)/名称/手机号过滤：先解析为匹配的 appKey 集合 (DESIGN §16.3)。
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		matched, err := s.admin.SearchUsers(r.Context(), kw)
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
			writeAdminJSON(w, http.StatusOK, map[string]any{"audits": []*model.AuditRecord{}})
			return
		}
		f.AppKeys = appKeys
	}
	audits, err := s.admin.ListAudits(r.Context(), f)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"audits": audits})
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
