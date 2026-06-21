package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
)

// 旧版 v9 入参格式校验 (income_cls.md: idCard 末位 X 大写; mobile 11 位)。
var (
	v9MobileRe = regexp.MustCompile(`^1\d{10}$`)
	v9IDCardRe = regexp.MustCompile(`^\d{17}[\dX]$`)
)

// handleQueryV9 serves GET /yrzx/finan/net/10w/v9 —— 本服务旧版 v9 下游契约
// (income_cls.md): HTTP GET, account/key 验签, 响应 code/msg/uid/result.range/verify。
// 入参存在性/格式校验在此完成 (返回码 005/008/009/011/020); 鉴权与业务由 orchestrator.HandleV9。
func (s *Server) handleQueryV9(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := &model.V9Request{
		Account: strings.TrimSpace(q.Get("account")),
		IDCard:  strings.ToUpper(strings.TrimSpace(q.Get("idCard"))),
		Name:    strings.TrimSpace(q.Get("name")),
		Mobile:  strings.TrimSpace(q.Get("mobile")),
		Reqid:   strings.TrimSpace(q.Get("reqid")),
		Verify:  strings.TrimSpace(q.Get("verify")),
	}

	// 入参校验 (income_cls.md §返回码字典)。
	switch {
	case req.Account == "":
		writeJSON(w, mapping.V9Error("009", "", req.Reqid))
		return
	case req.Reqid == "" || len(req.Reqid) > 20:
		writeJSON(w, mapping.V9Error("008", "", req.Reqid))
		return
	case req.IDCard == "" || !v9IDCardRe.MatchString(req.IDCard):
		writeJSON(w, mapping.V9Error("005", "", req.Reqid))
		return
	case req.Mobile == "" || !v9MobileRe.MatchString(req.Mobile):
		writeJSON(w, mapping.V9Error("020", "", req.Reqid))
		return
	case req.Verify == "":
		writeJSON(w, mapping.V9Error("011", "", req.Reqid))
		return
	}

	writeJSON(w, s.orch.HandleV9(r.Context(), req))
}
