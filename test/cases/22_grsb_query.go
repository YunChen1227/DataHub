//go:build ignore

// 22_grsb_query: grsb 版本 POST /v1/openapi/zlx/querySrmxGRSB（x1 信封格式；
// 内部对接背景评估 BJPG-01 mock，AES/CBC 加密 data + accountId/prodId 请求头）。
// 全场景：查得(001，range 为解密后 {xm,sfz,jfdw,grsf,jfjs,cbjfzt,jfsj} 全字段 JSON)/
// 查无(999)/鉴权与参数错误。
//
// 该上游入参只有 idCard + name 两项且均必填、**不要手机号**，故 base() 不带 mobile，
// 查无场景也改用约定的查无身份证号（mock 的 GRSB_NOTFOUND_IDCARD）而非查无手机号。
//
// Run: go run test/cases/22_grsb_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "grsb"

// notFoundIDCard 与 scripts/mock_bgpg.go 的 GRSB_NOTFOUND_IDCARD 缺省值一致
// （合法 18 位格式，驱动上游 code 2-404 没有查询到数据）。
const notFoundIDCard = "000000000000000404"

func base() map[string]string {
	return map[string]string{"idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("22_grsb_query", "grsb 主接口全场景 (背景评估 BJPG-01, AES/CBC data)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得 (仅 name+idCard, 不传手机号)",
		"errorCode=0 & body.code=001 & range 含全部 7 个业务字段",
		r.ErrorCode == "0" && r.BodyCode == "001" && rangeHasAllFields(r.Range), r.Raw)

	nf := base()
	nf["idCard"] = notFoundIDCard
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 上游参数表 idCard + name 均必填：网关前置拦截，不调用上游/不计费。
	noName := base()
	delete(noName, "name")
	r = harness.Query(version, appKey, harness.Secret, noName, nil)
	rec.Check("缺 name 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noID := base()
	delete(noID, "idCard")
	r = harness.Query(version, appKey, harness.Secret, noID, nil)
	rec.Check("缺 idCard 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	badID := base()
	badID["idCard"] = "12345"
	r = harness.Query(version, appKey, harness.Secret, badID, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 本上游不要手机号：带上一个格式非法的 mobile 也不该被拦截，证明校验器口径与
	// 上游参数表一致（不臆造多余必填/多余校验）。
	extra := base()
	extra["mobile"] = "139xx"
	r = harness.Query(version, appKey, harness.Secret, extra, nil)
	rec.Check("多余 mobile 不参与校验", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 jfdw",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "jfdw"), r.Raw)
}

// rangeHasAllFields 断言 range 是解密后业务对象的 JSON 且 7 个字段一个不少
// （全字段原样透出，不裁剪成 grgjj 的三字段口径）。
func rangeHasAllFields(raw string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	for _, f := range []string{"xm", "sfz", "jfdw", "grsf", "jfjs", "cbjfzt", "jfsj"} {
		if _, ok := m[f]; !ok {
			return false
		}
	}
	return true
}
