//go:build ignore

// 21_grgjj_failover: grgjj 双源串行寻源 (命中即停) 全场景。grgjj 挂两个可互相替代的
// 上游——主源 incomeag(收入A_g版, mock :9123) 优先、备源 bgjj(备用公积金, mock :9125)
// 优先级更低。用不同 mock 的 jfjs 值区分是哪个源应答的：
//   - 主源查得 (jfjs=7)：命中即停，备源零调用；
//   - 主源查无 → 回落备源查得 (jfjs=13, jfsj=202606, 字段由 {date,score,jfzt} 映射而来)；
//   - 两源皆查无 → 999。
//
// 依赖：mock_incomeag(:9123) 对 mobile 138/139…→999、其余→001(jfjs=7)；
//      mock_bgjj(:9125) 对 mobile 13800000000→201查无、其余→100查得(jfjs=13)。
//
// Run: go run test/cases/21_grgjj_failover.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "grgjj"

func body(mobile string) map[string]string {
	return map[string]string{"mobile": mobile, "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("21_grgjj_failover", "grgjj 双源串行寻源：命中即停 / 回落 / 全查无")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	// 1) 主源查得 → 命中即停：range 来自主源 incomeag (jfjs=7)，备源不参与。
	r := harness.Query(version, appKey, harness.Secret, body("13809091009"), nil)
	rec.Check("主源命中即停", "body.code=001 且 range 为主源结果 (jfjs=7)",
		r.ErrorCode == "0" && r.BodyCode == "001" &&
			strings.Contains(r.Range, "cbjfzt") && strings.Contains(r.Range, `"jfjs":"7"`), r.Raw)

	// 2) 主源查无 → 回落备源查得：range 来自备源 bgjj，字段由 {date,score,jfzt} 归一为
	//    {jfsj,jfjs,cbjfzt} (jfjs=13, jfsj=202606)。
	r = harness.Query(version, appKey, harness.Secret, body("13900000000"), nil)
	rec.Check("主源查无→回落备源查得", "body.code=001 且 range 为备源结果 (jfjs=13, jfsj=202606)",
		r.ErrorCode == "0" && r.BodyCode == "001" &&
			strings.Contains(r.Range, `"jfjs":"13"`) && strings.Contains(r.Range, `"jfsj":"202606"`) &&
			strings.Contains(r.Range, `"cbjfzt":"1"`), r.Raw)

	// 3) 两源皆查无 → 999。
	r = harness.Query(version, appKey, harness.Secret, body("13800000000"), nil)
	rec.Check("两源皆查无", "body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	// 4) 回落结果的 range 与主源同形 (下游无从察觉数据来自哪个源)：归一为契约字段，
	//    不泄漏备源原始字段名 (score/date)。
	r = harness.Query(version, appKey, harness.Secret, body("13900000000"), nil)
	rec.Check("双源 range 同形", "备源 range 归一为 cbjfzt/jfjs/jfsj，不含 score/date",
		strings.Contains(r.Range, "cbjfzt") && strings.Contains(r.Range, "jfjs") &&
			strings.Contains(r.Range, "jfsj") && !strings.Contains(r.Range, "score") &&
			!strings.Contains(r.Range, "date"), r.Raw)
}
