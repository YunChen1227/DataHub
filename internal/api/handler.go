package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/datahub/relay/internal/application"
	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
)

// VersionStack bundles the per-version dependencies (独立 orchestrator + 独立
// 后台服务，各自连独立数据库/上游)。三版本对外接口完全一致，仅靠路由名区分。
type VersionStack struct {
	Orch  *application.QueryOrchestrator
	Admin *admin.Service
}

// Server holds the HTTP handlers and their per-version dependencies.
type Server struct {
	stacks  map[string]*VersionStack // version -> stack (x1/v9/v8)
	control *admin.Service           // 后台登录 + JWT 校验的控制面 (= x1)
	spaDir  string                   // optional dir holding the built SPA (web/admin/dist)
}

// NewServer wires the per-version stacks plus the admin console (DESIGN §16).
// control 为后台统一登录/鉴权的控制面 (x1 版本的 admin.Service)。
func NewServer(stacks map[string]*VersionStack, control *admin.Service, spaDir string) *Server {
	return &Server{stacks: stacks, control: control, spaDir: spaDir}
}

// Routes wires the public endpoints with edge middleware (DESIGN §5/§16). 各版本
// 对外统一为 x1 信封格式，仅靠路由名区分：querySrmx{X1,V9,V8,ZLF,BLK} / quota{...}。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	for _, v := range model.Versions {
		st := s.stacks[v]
		if st == nil {
			continue
		}
		suffix := strings.ToUpper(v) // x1->X1, v9->V9, v8->V8
		mux.HandleFunc("POST /v1/openapi/zlx/querySrmx"+suffix, s.handleQuery(st))
		mux.HandleFunc("GET /v1/openapi/zlx/quota"+suffix, s.handleQuota(st))
	}
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

// handleQuery serves POST /v1/openapi/zlx/querySrmx{X1,V9,V8}: 统一 x1 信封格式,
// 由对应版本的 orchestrator 调用各自上游 (DESIGN §5.1)。
func (s *Server) handleQuery(st *VersionStack) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		writeJSON(w, st.Orch.Handle(r.Context(), signedFrom(&env), &cmd))
	}
}

// quotaResponse is本服务扩展的查询响应 (内部/admin 使用). 无额度限制，按路由独立统计：
// serviceUsed = 累计成功查得数据次数, totalCalls = 累计调用上游次数。
type quotaResponse struct {
	ErrorCode   string `json:"errorCode"`
	ErrorMsg    string `json:"errorMsg"`
	Status      string `json:"status,omitempty"`
	ServiceUsed int64  `json:"serviceUsed"` // 成功查得数据次数（累计）
	TotalCalls  int64  `json:"totalCalls"`  // 调用上游次数（累计）
}

// handleQuota serves GET /v1/openapi/zlx/quota{X1,V9,V8} (本服务扩展). 鉴权同主接口
// (appKey + MD5 签名)，信封从请求体读取；返回该版本下累计成功查得数。
func (s *Server) handleQuota(st *VersionStack) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env envelope
		_ = json.Unmarshal(raw, &env)

		view, _, err := st.Orch.QuotaQuery(r.Context(), signedFrom(&env))
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
			TotalCalls:  view.Calls,
		})
	}
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
