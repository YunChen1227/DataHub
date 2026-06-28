//go:build ignore

// 10_blk_query: blk 版本 POST /v1/openapi/zlx/querySrmxBLK（x1 信封格式；
// 内部对接黑名单因子V35 mock）。全场景：成功(富对象 JSON range)/查无/鉴权与参数错误。
//
// Run: go run test/cases/10_blk_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "blk"

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("10_blk_query", "blk 主接口全场景 (黑名单因子V35)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKey, harness.Secret, base(), nil)
	rangeOK := parseRangeHit(r.Range)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 whether_hit=1",
		r.ErrorCode == "0" && r.BodyCode == "001" && rangeOK, r.Raw)

	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, harness.AppKey, harness.Secret, nf, nil)
	rec.Check("查无结果", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, harness.AppKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	badm := base()
	badm["mobile"] = "139xx"
	r = harness.Query(version, harness.AppKey, harness.Secret, badm, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	badi := base()
	badi["idCard"] = "12345"
	r = harness.Query(version, harness.AppKey, harness.Secret, badi, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, harness.AppKey, harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 hit_grade",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "hit_grade"), r.Raw)
}

func parseRangeHit(raw string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	hit, ok := m["whether_hit"].(float64)
	return ok && hit == 1
}
