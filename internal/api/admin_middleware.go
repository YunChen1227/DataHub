package api

import (
	"net/http"
	"strings"
)

// requireAdmin wraps an admin handler with JWT bearer authentication (DESIGN §16.1).
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
		if token == "" || authz == token { // missing or no "Bearer " prefix
			writeAdminError(w, http.StatusUnauthorized, "缺少或非法的令牌")
			return
		}
		if _, err := s.control.VerifyToken(token); err != nil {
			writeAdminError(w, http.StatusUnauthorized, "令牌无效或已过期")
			return
		}
		next(w, r)
	}
}
