//go:build ignore

// 22_month_cache: 自然月结果缓存（domain/cache）全场景。
//
// 观测手段：mock_income 的 uid = "income-{version}-{reqid}"，每次回源都不同。而缓存
// 命中时我方回放**首次回源的 uid**（对账用）却换成本次请求的新 reqid，于是
//   uid 相同 + body.reqid 不同  ⇔  这一次是缓存回放（没调上游）
// 是一个不依赖时序、不依赖日志的硬观测。再配合 /quota 的 totalCalls（只在真的调用
// 上游时 +1）与管理端审计的 fromCache 列交叉验证。
//
// 断言清单：
//  1. 命中不再调上游：totalCalls 只 +1，两次响应 uid 相同
//  2. 命中照常计费：serviceUsed +2（查得就计，与回源口径一致）
//  3. 命中结果一致：body.code / result.range 与首查完全相同
//  4. 命中流水号仍唯一：body.reqid 每次不同（下游去重逻辑不会误判成重复报文）
//  5. 查无(999) 同样缓存：totalCalls 只 +1、serviceUsed 不动
//  6. 身份要素不同就不共享：仅换 name 即回源（姓名参与上游入参）
//  7. 路由隔离：同一身份在 v8 查必须回源，拿不到 v9 的条目（v8/v9 共用 Redis 库与
//     license，靠 shareGroup 分开——这条挂了就是把 v9 的答案当 v8 的返回）
//  8. 审计口径：命中行 fromCache=true / calledUpstream=false / billed=true
//
// 未启用缓存（versions.v9.cache.enabled 未开）时全部 SKIP 并给出开启方法。
//
// Run: go run test/cases/22_month_cache.go
package main

import (
	"fmt"

	"github.com/datahub/relay/test/harness"
)

// 缓存装配在 v9 上验证：mock_income 的 uid 随 reqid 变化，是最直接的「回源 vs 回放」
// 观测点（mock_gama 的 seqNo 恒定，观测不到）。
const route = "v9"

const enableHint = "未启用自然月结果缓存：在配置里给 versions." + route +
	" 加 cache: {enabled: true, pepper: \"<随机串>\"} 后重跑"

func main() {
	rec := harness.NewRecorder("22_month_cache", "自然月结果缓存（命中不回源 / 照常计费 / 路由隔离）")
	defer rec.Finish()

	appKey, secret := harness.AppKeyFor(route), harness.Secret

	token, loginRaw := harness.AdminLogin()
	if token == "" {
		rec.Skip("管理员登录", "拿到 token（审计断言需要）", "登录失败: "+loginRaw)
	}

	// --- 探测：缓存是否启用 ---
	probe := harness.UniqueIdentity("探测", "")
	p1 := harness.Query(route, appKey, secret, probe, nil)
	if p1.ErrorCode != "0" || p1.BodyCode != "001" {
		rec.Fail("探测查询可查得", "errorCode=0 & body.code=001",
			fmt.Sprintf("errorCode=%s body.code=%s", p1.ErrorCode, p1.BodyCode), p1.Raw)
		return
	}
	harness.SettleWait() // 等 Bookkeeper 把缓存写进去（异步，见 SettleWait 注释）
	p2 := harness.Query(route, appKey, secret, probe, nil)
	cacheOn := p2.UID == p1.UID && p2.UID != ""
	if !cacheOn {
		fmt.Printf("  两次 uid: %q / %q -> 判定缓存未启用\n", p1.UID, p2.UID)
		for _, name := range []string{
			"命中不再调上游", "命中照常计费", "命中结果一致", "命中流水号仍唯一",
			"查无结果同样缓存", "身份要素不同不共享缓存", "路由间不共享缓存", "审计标记缓存命中",
		} {
			rec.Skip(name, "缓存生效", enableHint)
		}
		return
	}
	rec.Pass("缓存已启用（探测）", "二次查询 uid 与首查相同 = 回放而非回源", p1.UID)

	checkFoundHit(rec, appKey, secret, token)
	checkNotFoundHit(rec, appKey, secret)
	checkIdentitySensitivity(rec, appKey, secret)
	checkRouteIsolation(rec, appKey, secret)
}

// checkFoundHit 覆盖断言 1/2/3/4/8：查得数据的命中行为与计费/审计口径。
func checkFoundHit(rec *harness.Recorder, appKey, secret, token string) {
	id := harness.UniqueIdentity("查得", "")

	used0 := harness.ServiceUsed(route, appKey, secret)
	calls0 := harness.TotalCalls(route, appKey, secret)

	r1 := harness.Query(route, appKey, secret, id, nil)
	rec.Check("首查回源查得", "errorCode=0 & body.code=001 & range=7",
		r1.ErrorCode == "0" && r1.BodyCode == "001" && r1.Range == "7", r1.Raw)
	harness.SettleWait()

	r2 := harness.Query(route, appKey, secret, id, nil)
	harness.SettleWait()

	used1 := harness.ServiceUsed(route, appKey, secret)
	calls1 := harness.TotalCalls(route, appKey, secret)
	usedDelta, callsDelta := used1-used0, calls1-calls0
	fmt.Printf("  查得: serviceUsed %v->%v (Δ%v) | totalCalls %v->%v (Δ%v)\n",
		used0, used1, usedDelta, calls0, calls1, callsDelta)
	fmt.Printf("  查得: uid %q/%q | reqid %q/%q | 耗时 %vms/%vms\n",
		r1.UID, r2.UID, r1.Reqid, r2.Reqid, r1.LatencyMs, r2.LatencyMs)

	rec.Check("命中不再调上游", "totalCalls 只 +1（第二次走缓存）",
		callsDelta == 1, fmt.Sprintf("Δ=%v (uid: %q -> %q)", callsDelta, r1.UID, r2.UID))
	rec.Check("命中照常计费", "serviceUsed +2（查得就计，与回源口径一致）",
		usedDelta == 2, fmt.Sprintf("Δ=%v", usedDelta))
	rec.Check("命中结果一致", "body.code 与 result.range 与首查完全相同",
		r2.BodyCode == r1.BodyCode && r2.Range == r1.Range,
		fmt.Sprintf("code %s/%s range %s/%s", r1.BodyCode, r2.BodyCode, r1.Range, r2.Range))
	rec.Check("命中保留上游流水号", "body.uid 保持首查原值（供向上游对账）",
		r2.UID == r1.UID && r2.UID != "", fmt.Sprintf("%q -> %q", r1.UID, r2.UID))
	rec.Check("命中流水号仍唯一", "body.reqid 每次不同（下游去重不会误判重复）",
		r2.Reqid != r1.Reqid && r2.Reqid != "", fmt.Sprintf("%q -> %q", r1.Reqid, r2.Reqid))

	checkAudits(rec, token, r1, r2)
}

// checkAudits 覆盖断言 8：命中行必须自解释「未调上游却计费」。
func checkAudits(rec *harness.Recorder, token string, r1, r2 harness.X1Result) {
	if token == "" {
		rec.Skip("审计标记缓存命中", "命中行 fromCache=true", "无管理员 token")
		return
	}
	a1, raw1 := harness.AuditByRequestID(route, token, r1.LogID)
	a2, raw2 := harness.AuditByRequestID(route, token, r2.LogID)
	if a1 == nil || a2 == nil {
		rec.Fail("审计标记缓存命中", "两次请求都能在审计里定位到",
			fmt.Sprintf("a1=%v a2=%v", a1 != nil, a2 != nil), raw1+"\n"+raw2)
		return
	}
	rec.Check("回源行未标记 fromCache", "首查 fromCache=false & calledUpstream=true",
		!harness.AuditFlag(a1, "fromCache") && harness.AuditFlag(a1, "calledUpstream"),
		fmt.Sprintf("fromCache=%v calledUpstream=%v",
			harness.AuditFlag(a1, "fromCache"), harness.AuditFlag(a1, "calledUpstream")))
	rec.Check("审计标记缓存命中", "命中行 fromCache=true & calledUpstream=false & billed=true",
		harness.AuditFlag(a2, "fromCache") && !harness.AuditFlag(a2, "calledUpstream") &&
			harness.AuditFlag(a2, "billed"),
		fmt.Sprintf("fromCache=%v calledUpstream=%v billed=%v",
			harness.AuditFlag(a2, "fromCache"), harness.AuditFlag(a2, "calledUpstream"),
			harness.AuditFlag(a2, "billed")))
}

// checkNotFoundHit 覆盖断言 5：查无结果同样缓存（同样消耗上游调用，收益一样），但不计费。
func checkNotFoundHit(rec *harness.Recorder, appKey, secret string) {
	// mock_income 以 mobile==13800000000 触发 999；idCard/name 随机保证是新缓存键。
	id := harness.UniqueIdentity("查无", "13800000000")

	used0 := harness.ServiceUsed(route, appKey, secret)
	calls0 := harness.TotalCalls(route, appKey, secret)

	r1 := harness.Query(route, appKey, secret, id, nil)
	rec.Check("首查回源查无", "errorCode=0 & body.code=999",
		r1.ErrorCode == "0" && r1.BodyCode == "999", r1.Raw)
	harness.SettleWait()

	r2 := harness.Query(route, appKey, secret, id, nil)
	harness.SettleWait()

	usedDelta := harness.ServiceUsed(route, appKey, secret) - used0
	callsDelta := harness.TotalCalls(route, appKey, secret) - calls0
	fmt.Printf("  查无: serviceUsed Δ%v | totalCalls Δ%v | uid %q/%q\n",
		usedDelta, callsDelta, r1.UID, r2.UID)

	rec.Check("查无结果同样缓存", "body.code=999 且 totalCalls 只 +1",
		r2.BodyCode == "999" && callsDelta == 1,
		fmt.Sprintf("code=%s callsΔ=%v (uid %q -> %q)", r2.BodyCode, callsDelta, r1.UID, r2.UID))
	rec.Check("查无不计费", "serviceUsed 不变",
		usedDelta == 0, fmt.Sprintf("Δ=%v", usedDelta))
}

// checkIdentitySensitivity 覆盖断言 6：name 参与上游入参，换了名字就是另一次查询，
// 必须回源——否则会把「补了姓名的查询」错答成不带姓名那次的结果。
func checkIdentitySensitivity(rec *harness.Recorder, appKey, secret string) {
	id := harness.UniqueIdentity("要素", "")
	r1 := harness.Query(route, appKey, secret, id, nil)
	harness.SettleWait()

	renamed := map[string]string{"name": id["name"] + "改", "idCard": id["idCard"], "mobile": id["mobile"]}
	r2 := harness.Query(route, appKey, secret, renamed, nil)

	rec.Check("身份要素不同不共享缓存", "仅换 name 即回源（uid 变化）",
		r2.UID != r1.UID && r2.BodyCode == "001",
		fmt.Sprintf("uid %q -> %q, code=%s", r1.UID, r2.UID, r2.BodyCode))
}

// checkRouteIsolation 覆盖断言 7：v8/v9 共用同一 Redis 逻辑库与同一套 license，缓存
// 键靠 shareGroup 分开。这条挂了意味着把 v9 上游产品的答案当 v8 的返回。
func checkRouteIsolation(rec *harness.Recorder, appKey, secret string) {
	id := harness.UniqueIdentity("隔离", "")

	r9 := harness.Query("v9", appKey, secret, id, nil)
	harness.SettleWait()
	r8 := harness.Query("v8", appKey, secret, id, nil)

	// mock_income 的 uid 带路由名：拿到 income-v8-* 说明确实回源到了 v8 的上游，
	// 而不是读到 v9 写的那条缓存。
	v8FromOwnUpstream := r8.UID != r9.UID && r8.UID != "" &&
		len(r8.UID) > len("income-v8-") && r8.UID[:len("income-v8-")] == "income-v8-"
	rec.Check("路由间不共享缓存", "v8 查询回源到 v8 上游（uid 前缀 income-v8-），不复用 v9 的缓存",
		v8FromOwnUpstream, fmt.Sprintf("v9.uid=%q v8.uid=%q", r9.UID, r8.UID))
}
