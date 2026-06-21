package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/datahub/relay/internal/application"
	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
)

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	orch   *application.QueryOrchestrator
	admin  *admin.Service
	spaDir string // optional dir holding the built SPA (web/admin/dist)
}

// NewServer wires the business orchestrator plus the admin console (DESIGN §16).
// IP 准入自 v0.7 起交由阿里云 ECS 安全组，网关不再做 IP 白名单。
func NewServer(orch *application.QueryOrchestrator, adminSvc *admin.Service, spaDir string) *Server {
	return &Server{orch: orch, admin: adminSvc, spaDir: spaDir}
}

// Routes wires the public endpoints with edge middleware
// (当前 x1 契约 + 旧版 v9 兼容 / DESIGN §5/§16). 网关前缀 /v1。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/openapi/zlx/querySrmxX1", s.handleQuery)
	mux.HandleFunc("GET /v1/openapi/zlx/quota", s.handleQuota)
	// 旧版 v9 下游契约 (income_cls.md): HTTP GET, account/key 验签。兼容老客户。
	mux.HandleFunc("GET /yrzx/finan/net/10w/v9", s.handleQueryV9)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	s.registerAdminRoutes(mux)
	return RequestIDMiddleware(mux)
}

// envelope is the请求信封 (网关 appKey/appSecret): appKey/sign/encryptionType/body.
type envelope struct {
	AppKey         string          `json:"appKey"`
	Sign           string          `json:"sign"`
	EncryptionType int             `json:"encryptionType"`
	Body           json.RawMessage `json:"body"`
}

// handleQuery serves POST /v1/openapi/zlx/querySrmxX1 (本服务 x1 契约, DESIGN §5.1).
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	seqNo := appctx.RequestID(r.Context())

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

// quotaResponse is本服务扩展的查询响应 (.doc 未定义, 内部/admin 使用). 无额度限制，
// serviceUsed = 累计成功查得数据的次数。
type quotaResponse struct {
	ErrorCode   string `json:"errorCode"`
	ErrorMsg    string `json:"errorMsg"`
	Status      string `json:"status,omitempty"`
	ServiceUsed int64  `json:"serviceUsed"` // 成功查得数据次数（累计）
}

// handleQuota serves GET /v1/openapi/zlx/quota (本服务扩展). 鉴权同主接口
// (appKey + MD5 签名)，信封从请求体读取；返回累计成功查得数。
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
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
		ErrorCode:   errs.ErrorCodeOK,
		ErrorMsg:    "success",
		Status:      view.Status,
		ServiceUsed: view.Used,
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
