package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/datahub/relay/internal/application"
	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
)

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	orch *application.QueryOrchestrator
}

func NewServer(orch *application.QueryOrchestrator) *Server {
	return &Server{orch: orch}
}

// Routes wires the public endpoints with edge middleware (DESIGN §5).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /openapi/zlx/querySrmxV9", s.handleQuery)
	mux.HandleFunc("GET /openapi/zlx/quota", s.handleQuota)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	return RequestIDMiddleware(mux)
}

// handleQuery serves POST /openapi/zlx/querySrmxV9 (DESIGN §5.1).
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	requestID := appctx.RequestID(r.Context())

	body, _ := io.ReadAll(r.Body)
	var cmd model.QueryCommand
	if err := json.Unmarshal(body, &cmd); err != nil {
		writeResult(w, mapping.ErrorResponse(errs.ParamInvalid, "请求体解析失败", requestID))
		return
	}

	signed := signedFrom(r, body)
	res, err := s.orch.Handle(r.Context(), signed, &cmd)
	if err != nil {
		ae := errs.AsAppError(err)
		writeResult(w, mapping.ErrorResponse(ae.Code, ae.Msg, requestID))
		return
	}
	writeResult(w, res)
}

// quotaBody mirrors the §5.2 success body shape.
type quotaBody struct {
	Status           string `json:"status"`
	ServiceTotal     int64  `json:"serviceTotal"`
	ServiceUsed      int64  `json:"serviceUsed"`
	ServiceRemaining int64  `json:"serviceRemaining"`
}

type quotaResponse struct {
	Head model.Head `json:"head"`
	Body *quotaBody  `json:"body,omitempty"`
}

// handleQuota serves GET /openapi/zlx/quota (DESIGN §5.2).
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	requestID := appctx.RequestID(r.Context())

	signed := signedFrom(r, nil)
	view, lic, err := s.orch.QuotaQuery(r.Context(), signed)
	if err != nil {
		ae := errs.AsAppError(err)
		writeJSON(w, string(ae.Code), quotaResponse{Head: head(string(ae.Code), ae.Msg, requestID)})
		return
	}

	code := string(errs.OK)
	msg := "success"
	if view.Remaining <= 0 {
		code = string(errs.ServiceQuotaEmpty)
		msg = errs.New(errs.ServiceQuotaEmpty, "").Msg
	}
	_ = lic
	writeJSON(w, code, quotaResponse{
		Head: head(code, msg, requestID),
		Body: &quotaBody{
			Status:           view.Status,
			ServiceTotal:     view.Total,
			ServiceUsed:      view.Used,
			ServiceRemaining: view.Remaining,
		},
	})
}

func head(code, msg, requestID string) model.Head {
	return model.Head{
		ErrorCode: code,
		RequestID: requestID,
		LogID:     requestID,
		ErrorMsg:  msg,
		Timestamp: time.Now().UnixMilli(),
	}
}

// signedFrom extracts the §8.1 signature material from headers.
func signedFrom(r *http.Request, body []byte) *model.SignedRequest {
	return &model.SignedRequest{
		AppKey:    r.Header.Get("X-App-Key"),
		Timestamp: r.Header.Get("X-Timestamp"),
		Nonce:     r.Header.Get("X-Nonce"),
		Sign:      r.Header.Get("X-Sign"),
		Body:      body,
	}
}
