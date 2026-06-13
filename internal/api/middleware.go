// Package api is the HTTP接入层: requestId middleware, signature extraction,
// handlers and unified response envelopes (DESIGN §3.1 / §9).
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/ipfilter"
	"github.com/datahub/relay/internal/common/reqid"
)

// RequestIDMiddleware generates the全链路追踪 requestId at the edge (before auth,
// so auth failures are also traceable — DESIGN §9.2) and echoes it via the
// X-Request-Id header. It buffers and restores the body so handlers can re-read it.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		id := r.Header.Get("X-Request-Id") // optional inbound passthrough
		if id == "" {
			// client portion of the requestId comes from the envelope appKey.
			var env struct {
				AppKey string `json:"appKey"`
			}
			_ = json.Unmarshal(body, &env)
			id = reqid.Generate(time.Now().UnixMilli(), env.AppKey, body)
		}

		ctx := appctx.WithRequestID(r.Context(), id)
		ctx = appctx.WithClientIP(ctx, ipfilter.ClientIP(r.Header.Get("X-Forwarded-For"), r.RemoteAddr))
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
