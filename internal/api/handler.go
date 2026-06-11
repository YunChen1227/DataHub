package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/datahub/relay/internal/application"
	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/common/ipfilter"
	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
)

// GlobalIPProvider exposes the global IP whitelist for edge enforcement (§16.4).
type GlobalIPProvider interface {
	GetGlobalIP(ctx context.Context) ([]string, error)
}

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	orch     *application.QueryOrchestrator
	admin    *admin.Service
	globalIP GlobalIPProvider
	spaDir   string // optional dir holding the built SPA (web/admin/dist)
}

// NewServer wires the business orchestrator plus the admin console (DESIGN §16).
func NewServer(orch *application.QueryOrchestrator, adminSvc *admin.Service, globalIP GlobalIPProvider, spaDir string) *Server {
	return &Server{orch: orch, admin: adminSvc, globalIP: globalIP, spaDir: spaDir}
}

// Routes wires the public endpoints with edge middleware (PDF §2.1 / DESIGN §5/§16).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /enol/api/v1/doCheck", s.handleDoCheck)
	mux.HandleFunc("GET /openapi/zlx/quota", s.handleQuota)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	s.registerAdminRoutes(mux)
	return RequestIDMiddleware(mux)
}

// globalIPAllowed enforces the global whitelist at the business edge (§16.4).
func (s *Server) globalIPAllowed(ctx context.Context) bool {
	if s.globalIP == nil {
		return true
	}
	cidrs, err := s.globalIP.GetGlobalIP(ctx)
	if err != nil {
		return true // fail-open on lookup error; logged elsewhere
	}
	return ipfilter.Allowed(appctx.ClientIP(ctx), cidrs)
}

// envelope is the PDF request信封 (§1.4): appId/sign/apiKey/encryptionType/body.
type envelope struct {
	AppID          string          `json:"appId"`
	Sign           string          `json:"sign"`
	APIKey         string          `json:"apiKey"`
	EncryptionType int             `json:"encryptionType"`
	Body           json.RawMessage `json:"body"`
}

// handleDoCheck serves POST /enol/api/v1/doCheck (PDF §2 / DESIGN §5.1).
func (s *Server) handleDoCheck(w http.ResponseWriter, r *http.Request) {
	seqNo := appctx.RequestID(r.Context())

	if !s.globalIPAllowed(r.Context()) {
		writeJSON(w, mapping.SystemError("IP 不在白名单", seqNo))
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// 请求体不可解析 → 全局 code=-1 响应异常 (PDF §1.6).
		writeJSON(w, mapping.SystemError("请求体解析失败", seqNo))
		return
	}

	var cmd model.QueryCommand
	if len(env.Body) > 0 {
		if err := json.Unmarshal(env.Body, &cmd); err != nil {
			writeJSON(w, mapping.SystemError("请求体解析失败", seqNo))
			return
		}
	}

	writeJSON(w, s.orch.Handle(r.Context(), signedFrom(&env), &cmd))
}

// quotaData mirrors the §5.2 success body shape, embedded in the PDF data object.
type quotaData struct {
	BusiCode         int    `json:"busiCode"`
	BusiMsg          string `json:"busiMsg"`
	Status           string `json:"status"`
	ServiceTotal     int64  `json:"serviceTotal"`
	ServiceUsed      int64  `json:"serviceUsed"`
	ServiceRemaining int64  `json:"serviceRemaining"`
}

type quotaResponse struct {
	Code  int        `json:"code"`
	Msg   string     `json:"msg"`
	SeqNo string     `json:"seqNo"`
	Data  *quotaData `json:"data,omitempty"`
}

// handleQuota serves GET /openapi/zlx/quota (DESIGN §5.2, 本服务扩展). 鉴权同主接口
// (appId + MD5 签名)，信封从请求体读取；维度② 不对客户暴露。
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	seqNo := appctx.RequestID(r.Context())

	if !s.globalIPAllowed(r.Context()) {
		writeJSON(w, mapping.SystemError("IP 不在白名单", seqNo))
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var env envelope
	_ = json.Unmarshal(raw, &env)

	view, _, err := s.orch.QuotaQuery(r.Context(), signedFrom(&env))
	if err != nil {
		ae := errs.AsAppError(err)
		writeJSON(w, mapping.Busi(ae.Busi, ae.Msg, seqNo))
		return
	}

	busiCode := int(errs.BusiSuccess)
	busiMsg := "success"
	if view.Remaining <= 0 {
		busiCode = int(errs.BusiNoBalance)
		busiMsg = errs.Msg(errs.BusiNoBalance)
	}
	writeJSON(w, quotaResponse{
		Code:  errs.CodeOK,
		Msg:   "请求成功",
		SeqNo: seqNo,
		Data: &quotaData{
			BusiCode:         busiCode,
			BusiMsg:          busiMsg,
			Status:           view.Status,
			ServiceTotal:     view.Total,
			ServiceUsed:      view.Used,
			ServiceRemaining: view.Remaining,
		},
	})
}

// signedFrom extracts the §8.1 signature material from the request envelope.
// BodyParams are the non-empty string business params used to recompute the MD5.
func signedFrom(env *envelope) *model.SignedRequest {
	return &model.SignedRequest{
		AppID:          env.AppID,
		Sign:           env.Sign,
		APIKey:         env.APIKey,
		EncryptionType: env.EncryptionType,
		BodyParams:     bodyParams(env.Body),
	}
}

// bodyParams decodes the body object into its non-empty string params (DESIGN
// §8.1: 剔除字节/文件类型与值为空的参数).
func bodyParams(rawBody json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(rawBody) == 0 {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		return out
	}
	for k, v := range m {
		if str, ok := v.(string); ok && str != "" {
			out[k] = str
		}
	}
	return out
}
