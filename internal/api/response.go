package api

import (
	"encoding/json"
	"net/http"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

// writeJSON serializes v with the appropriate HTTP status for the head code.
func writeJSON(w http.ResponseWriter, errorCode string, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatusFor(errorCode))
	_ = json.NewEncoder(w).Encode(v)
}

// writeResult emits a QueryResult envelope.
func writeResult(w http.ResponseWriter, res *model.QueryResult) {
	writeJSON(w, res.Head.ErrorCode, res)
}

// httpStatusFor maps a gateway head.errorCode to an HTTP status (DESIGN §5.3).
func httpStatusFor(code string) int {
	switch errs.Code(code) {
	case errs.OK:
		return http.StatusOK
	case errs.MissingAppKey, errs.SignatureInvalid, errs.LicenseInactive:
		return http.StatusUnauthorized
	case errs.ServiceQuotaEmpty, errs.UpstreamQuotaEmpty:
		return http.StatusTooManyRequests
	case errs.ParamInvalid:
		return http.StatusBadRequest
	case errs.UpstreamNotExecuted, errs.UpstreamBusiness:
		return http.StatusBadGateway
	default:
		return http.StatusOK
	}
}
