//go:build ignore

// 03_v9_query: 旧版兼容接口 GET /yrzx/finan/net/10w/v9 全场景（成功/查无/错签/
// 各类参数错误/幂等）。签名 verify = MD5(account+idCard+mobile+reqid+key) 大写。
//
// Run: go run test/cases/03_v9_query.go
package main

import (
	"github.com/datahub/relay/test/harness"
)

const (
	idCard = "330129199109094312"
	mobile = "13809091009"
	nfMob  = "13800000000"
)

func sign(idc, mob, reqid string) string {
	return harness.SignV9(harness.AppKey, idc, mob, reqid, harness.Secret)
}

func main() {
	rec := harness.NewRecorder("03_v9_query", "v9 兼容接口全场景")
	defer rec.Finish()

	// 1. 成功
	reqid := harness.ShortReqid("ok")
	r := harness.QueryV9(harness.AppKey, idCard, "张三", mobile, reqid, sign(idCard, mobile, reqid))
	rec.Check("v9 成功查得", "code=001 & range=7", r.Code == "001" && r.Range == "7", r.Raw)

	// 2. 查无
	rqn := harness.ShortReqid("nf")
	r = harness.QueryV9(harness.AppKey, idCard, "张三", nfMob, rqn, sign(idCard, nfMob, rqn))
	rec.Check("v9 查无结果", "code=999", r.Code == "999", r.Raw)

	// 3. 错误签名 -> 013
	rqb := harness.ShortReqid("bad")
	r = harness.QueryV9(harness.AppKey, idCard, "张三", mobile, rqb, "BADVERIFY")
	rec.Check("v9 错误签名", "code=013", r.Code == "013", r.Raw)

	// 4. account 为空 -> 009
	rqa := harness.ShortReqid("ea")
	r = harness.QueryV9("", idCard, "张三", mobile, rqa, sign(idCard, mobile, rqa))
	rec.Check("v9 account 为空", "code=009", r.Code == "009", r.Raw)

	// 5. reqid 为空 -> 008
	r = harness.QueryV9(harness.AppKey, idCard, "张三", mobile, "", sign(idCard, mobile, ""))
	rec.Check("v9 reqid 为空", "code=008", r.Code == "008", r.Raw)

	// 5b. reqid 超过 20 -> 008
	long := "reqid123456789012345678901234"
	r = harness.QueryV9(harness.AppKey, idCard, "张三", mobile, long, sign(idCard, mobile, long))
	rec.Check("v9 reqid 超长(>20)", "code=008", r.Code == "008", r.Raw)

	// 6. idCard 非法 -> 005
	rqi := harness.ShortReqid("ic")
	r = harness.QueryV9(harness.AppKey, "12345", "张三", mobile, rqi, sign("12345", mobile, rqi))
	rec.Check("v9 idCard 非法", "code=005", r.Code == "005", r.Raw)

	// 7. mobile 非法 -> 020
	rqm := harness.ShortReqid("mb")
	r = harness.QueryV9(harness.AppKey, idCard, "张三", "139xx", rqm, sign(idCard, "139xx", rqm))
	rec.Check("v9 mobile 非法", "code=020", r.Code == "020", r.Raw)

	// 8. verify 为空 -> 011
	rqv := harness.ShortReqid("ev")
	r = harness.QueryV9(harness.AppKey, idCard, "张三", mobile, rqv, "")
	rec.Check("v9 verify 为空", "code=011", r.Code == "011", r.Raw)

	// 9. 幂等：同 reqid 重复成功查询仍返回 001（不报错）
	rqIdem := harness.ShortReqid("id")
	v := sign(idCard, mobile, rqIdem)
	r1 := harness.QueryV9(harness.AppKey, idCard, "张三", mobile, rqIdem, v)
	r2 := harness.QueryV9(harness.AppKey, idCard, "张三", mobile, rqIdem, v)
	rec.Check("v9 同 reqid 幂等", "两次均 code=001",
		r1.Code == "001" && r2.Code == "001", "first="+r1.Code+" second="+r2.Code)
}
