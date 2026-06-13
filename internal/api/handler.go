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

// Routes wires the public endpoints with edge middleware
// (接口文档-经济能力.doc §3 / DESIGN §5/§16). 网关前缀 /v1。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/openapi/zlx/querySrmxV9", s.handleQuery)
	mux.HandleFunc("GET /v1/openapi/zlx/quota", s.handleQuota)
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

// envelope is the请求信封 (网关 appKey/appSecret): appKey/sign/encryptionType/body.
type envelope struct {
	AppKey         string          `json:"appKey"`
	Sign           string          `json:"sign"`
	EncryptionType int             `json:"encryptionType"`
	Body           json.RawMessage `json:"body"`
}

// handleQuery serves POST /v1/openapi/zlx/querySrmxV9 (接口文档-经济能力.doc §3.1).
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	seqNo := appctx.RequestID(r.Context())

	if !s.globalIPAllowed(r.Context()) {
		writeJSON(w, mapping.Error(errs.BusiAccountAbnormal, "IP 不在白名单", seqNo, 0))
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		writeJSON(w, mapping.Error(errs.BusiDataRequestErr, "请求体解析失败", seqNo, 0))
		return
	}

	var cmd model.QueryCommand
	if len(env.Body) > 0 {
		if err := json.Unmarshal(env.Body, &cmd); err != nil {
			writeJSON(w, mapping.Error(errs.BusiDataRequestErr, "请求体解析失败", seqNo, 0))
			return
		}
	}

	writeJSON(w, s.orch.Handle(r.Context(), signedFrom(&env), &cmd))
}

// quotaResponse is本服务扩展的配额查询响应 (.doc 未定义, 内部/admin 使用).
type quotaResponse struct {
	ErrorCode        string `json:"errorCode"`
	ErrorMsg         string `json:"errorMsg"`
	Status           string `json:"status,omitempty"`
	ServiceTotal     int64  `json:"serviceTotal"`
	ServiceUsed      int64  `json:"serviceUsed"`
	ServiceRemaining int64  `json:"serviceRemaining"`
}

// handleQuota serves GET /v1/openapi/zlx/quota (本服务扩展). 鉴权同主接口
// (appKey + MD5 签名)，信封从请求体读取；维度② 不对客户暴露。
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	if !s.globalIPAllowed(r.Context()) {
		writeJSON(w, quotaResponse{ErrorCode: errs.ErrorCode(errs.BusiAccountAbnormal), ErrorMsg: "IP 不在白名单"})
		return
	}

	raw, _ := io.ReadAll(r.Body)
	var env envelope
	_ = json.Unmarshal(raw, &env)

	view, _, err := s.orch.QuotaQuery(r.Context(), signedFrom(&env))
	if err != nil {
		ae := errs.AsAppError(err)
		writeJSON(w, quotaResponse{ErrorCode: errs.ErrorCode(ae.Busi), ErrorMsg: ae.Msg})
		return
	}
	writeJSON(w, quotaResponse{
		ErrorCode:        errs.ErrorCodeOK,
		ErrorMsg:         "success",
		Status:           view.Status,
		ServiceTotal:     view.Total,
		ServiceUsed:      view.Used,
		ServiceRemaining: view.Remaining,
	})
}

// signedFrom extracts the signature material from the request envelope.
// BodyParams are the non-empty string business params used to recompute the MD5.
func signedFrom(env *envelope) *model.SignedRequest {
	return &model.SignedRequest{
		AppKey:         env.AppKey,
		Sign:           env.Sign,
		EncryptionType: env.EncryptionType,
		BodyParams:     bodyParams(env.Body),
	}
}

// bodyParams decodes the body object into its non-empty string params
// (DESIGN §8.1: 剔除字节/文件类型与值为空的参数).
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
