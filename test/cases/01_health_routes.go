//go:build ignore

// 01_health_routes: /healthz 与四条业务路由（x1 / quota / v9）的可达性。仅验证
// 路由已注册（非 404）且 relay 在线，不校验业务结果。
//
// Run: go run test/cases/01_health_routes.go
package main

import (
	"net/http"

	"github.com/datahub/relay/test/harness"
)

func main() {
	rec := harness.NewRecorder("01_health_routes", "健康检查与路由可达性")
	defer rec.Finish()

	st, _, raw := harness.Call(http.MethodGet, "/healthz", nil, nil)
	rec.Check("GET /healthz == 200 ok", "HTTP 200 + body 含 ok",
		st == 200 && contains(raw, "ok"), itoa(st)+" "+raw)

	// x1: POST，带最小信封；只要不是 404 即视为已注册（业务结果在其他用例覆盖）。
	x := harness.QueryX1(harness.AppKey, harness.Secret,
		map[string]string{"mobile": "13809091009", "idCard": "330129199109094312"}, nil)
	rec.Check("POST querySrmxX1 已注册", "返回 head 信封(非404)",
		x.HTTPStatus == 200 && x.ErrorCode != "", "status="+itoa(x.HTTPStatus)+" raw="+x.Raw)

	// quota: GET（带信封 body）。
	used := harness.ServiceUsed(harness.AppKey, harness.Secret)
	rec.Check("GET quota 已注册", "返回 serviceUsed 数值", used >= 0, ftoa(used))

	// v9: GET，参数缺失也应返回 v9 错误码（非 404）。
	v := harness.QueryV9(harness.AppKey, "330129199109094312", "张三", "13809091009",
		harness.ShortReqid("h"), "x")
	rec.Check("GET v9 已注册", "返回 code 字段(非404)", v.HTTPStatus == 200 && v.Code != "",
		"status="+itoa(v.HTTPStatus)+" raw="+v.Raw)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func ftoa(f float64) string {
	return itoa(int(f))
}
