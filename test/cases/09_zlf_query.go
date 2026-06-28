//go:build ignore

// 09_zlf_query: zlf 版本 POST /v1/openapi/zlx/querySrmxZLF（x1 信封格式；
// 内部对接租赁分V2-D mock）。全场景：成功/查无/鉴权与参数错误/二次成功。
//
// Run: go run test/cases/09_zlf_query.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "zlf"

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("09_zlf_query", "zlf 主接口全场景 (租赁分V2-D)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKey, harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range=546.6",
		r.ErrorCode == "0" && r.BodyCode == "001" && r.Range == "546.6", r.Raw)

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
	rec.Check("二次成功查得", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "546"), r.Raw)
}
