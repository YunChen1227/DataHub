package api

import (
	"encoding/json"
	"net/http"
)

// writeJSON serializes v as the PDF envelope. Business state is carried in the
// body (code / busiCode), so the HTTP status is always 200 (PDF §1.3/§5.3).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
