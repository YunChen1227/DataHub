//go:build ignore

// 29_fqz_query: fqz 版本 POST /v1/openapi/zlx/querySrmxFQZ（x1 信封格式；
// 内部对接申请反欺诈-定制模型 mock，阿里云 SAF ExecuteRequest + POP RPC HMAC-SHA1 签名）。
// 全场景：查得(001，range={"score":"258"})/查无(999，score=-1，**本路由查无也计费**)/
// 上游侧错误(403 权限不足)/鉴权与参数错误。
//
// 该上游 ServiceParameters 只有 mobile(三选一中本网关只用明文 mobile) + scene(配置固定)，
// **不要姓名身份证**，故 base() 只带 mobile。
//
// Run: go run test/cases/29_fqz_query.go
package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "fqz"

// 与 scripts/mock_antifraud.go 的 env 缺省值一致。
const (
	notFoundMobile  = "13800000000" // 驱动上游 Code=200 + score=-1 未查得
	forbiddenMobile = "13800000403" // 驱动上游 Code=403 权限不足（不计费）
)

func base() map[string]string {
	return map[string]string{"mobile": "17325421010"}
}

func main() {
	rec := harness.NewRecorder("29_fqz_query", "fqz 主接口全场景 (申请反欺诈-定制模型, 阿里云 SAF POP RPC 签名)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得 (仅 mobile)", "errorCode=0 & body.code=001 & range.score=258",
		r.ErrorCode == "0" && r.BodyCode == "001" && scoreEquals(r.Range, "258"), r.Raw)

	// score=-1 未查得：报文形态照旧是 999，计费由 billing.billNotFoundRoutes 决定，
	// 查无前后 serviceUsed 必须 +1。
	before := harness.ServiceUsed(version, appKey, harness.Secret)
	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": notFoundMobile}, nil)
	rec.Check("查无结果 (score=-1)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)
	after := harness.ServiceUsed(version, appKey, harness.Secret)
	rec.Check("查无也计费", "serviceUsed +1", before >= 0 && after == before+1,
		fmt.Sprintf("before=%v after=%v", before, after))

	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": forbiddenMobile}, nil)
	rec.Check("上游 403 权限不足", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// mobile 必填：网关前置拦截，不调用上游/不计费。
	r = harness.Query(version, appKey, harness.Secret, map[string]string{}, nil)
	rec.Check("缺 mobile 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": "139xx"}, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 下游直接传摘要：信封 encryptionType=2/3，body.mobile 为 MD5/SHA256 小写 hex，
	// sign 照常对摘要值计算。mock 对特殊号按同口径摘要比对。
	enc2 := map[string]any{"encryptionType": 2}
	enc3 := map[string]any{"encryptionType": 3}
	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": md5Hex("17325421010")}, enc2)
	rec.Check("MD5 入参查得", "errorCode=0 & body.code=001 & range.score=258",
		r.ErrorCode == "0" && r.BodyCode == "001" && scoreEquals(r.Range, "258"), r.Raw)

	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": strings.ToUpper(md5Hex(notFoundMobile))}, enc2)
	rec.Check("MD5 入参(大写)查无", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": sha256Hex("17325421010")}, enc3)
	rec.Check("SHA256 入参查得", "errorCode=0 & body.code=001", r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": sha256Hex(notFoundMobile)}, enc3)
	rec.Check("SHA256 入参查无", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), enc2)
	rec.Check("encryptionType=2 却传明文", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, map[string]string{"mobile": md5Hex("17325421010")}, enc3)
	rec.Check("encryptionType=3 却传 MD5", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"encryptionType": 4})
	rec.Check("encryptionType 非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 本上游不要姓名/身份证：带上格式非法的 idCard 既不被拦截，也不影响查询。
	extra := base()
	extra["idCard"] = "12345"
	extra["name"] = "张三"
	r = harness.Query(version, appKey, harness.Secret, extra, nil)
	rec.Check("多余 name/idCard 不参与校验", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游 RequestId", "range 含 score 且整份 raw 不含 fqz-mock",
		strings.Contains(r.Range, "score") && !strings.Contains(r.Raw, "fqz-mock"), r.Raw)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// scoreEquals 断言 range 是上游 Data 对象的 JSON 且 score 原样透出。
func scoreEquals(raw, want string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	return m["score"] == want
}
