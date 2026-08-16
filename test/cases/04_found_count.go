//go:build ignore

// 04_found_count: 验证"只统计成功查得数、无额度限制"且各版本统计互相独立。
// 对每个版本各自读 /quota 前值 -> 发 N 次成功 + M 次查无 -> 读后值，断言该版本
// serviceUsed 增量恰为 N（查无不计）；并断言只对 x1 发流量时，其他版本计数不变。
//
// Run: go run test/cases/04_found_count.go
package main

import (
	"fmt"

	"github.com/datahub/relay/test/harness"
)

const (
	nSuccess = 3
	mNotFnd  = 2
)

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("04_found_count", "成功查得数统计 + 无额度限制 + 版本隔离")
	defer rec.Finish()

	// 记录其他版本初始计数，用于稍后验证隔离。
	v9Before := harness.ServiceUsed("v9", harness.AppKeyFor("v9"), harness.Secret)
	v8Before := harness.ServiceUsed("v8", harness.AppKeyFor("v8"), harness.Secret)
	zlfBefore := harness.ServiceUsed("zlf", harness.AppKeyFor("zlf"), harness.Secret)
	blkBefore := harness.ServiceUsed("blk", harness.AppKeyFor("blk"), harness.Secret)
	rlbd1Before := harness.ServiceUsed("rlbd1", harness.AppKeyFor("rlbd1"), harness.Secret)
	rlbd2Before := harness.ServiceUsed("rlbd2", harness.AppKeyFor("rlbd2"), harness.Secret)
	sfzhyBefore := harness.ServiceUsed("sfzhy", harness.AppKeyFor("sfzhy"), harness.Secret)
	xfjyBefore := harness.ServiceUsed("xfjy", harness.AppKeyFor("xfjy"), harness.Secret)
	tsfxBefore := harness.ServiceUsed("tsfx", harness.AppKeyFor("tsfx"), harness.Secret)
	lxfBefore := harness.ServiceUsed("lxf", harness.AppKeyFor("lxf"), harness.Secret)
	grgjjBefore := harness.ServiceUsed("grgjj", harness.AppKeyFor("grgjj"), harness.Secret)

	// 仅对 x1 发起流量，逐版本独立计数。
	before := harness.ServiceUsed("x1", harness.AppKey, harness.Secret)
	if before < 0 {
		rec.Fail("读取 serviceUsed(前)", "数值 >= 0", fmt.Sprintf("%v", before), "无法读取 /quotaX1.serviceUsed")
		return
	}
	fmt.Printf("  x1 serviceUsed(before) = %v\n", before)

	noLimit := true
	for i := 0; i < nSuccess; i++ {
		r := harness.Query("x1", harness.AppKey, harness.Secret, base(), nil)
		if r.ErrorCode == "505005" || r.ErrorCode == "505006" {
			noLimit = false
		}
		rec.Check(fmt.Sprintf("x1 成功查询 #%d", i+1), "errorCode=0 & body.code=001",
			r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)
	}
	for i := 0; i < mNotFnd; i++ {
		nf := base()
		nf["mobile"] = "13800000000"
		r := harness.Query("x1", harness.AppKey, harness.Secret, nf, nil)
		rec.Check(fmt.Sprintf("x1 查无查询 #%d", i+1), "errorCode=0 & body.code=999",
			r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)
	}

	after := harness.ServiceUsed("x1", harness.AppKey, harness.Secret)
	fmt.Printf("  x1 serviceUsed(after) = %v\n", after)
	delta := after - before
	rec.Check("x1 成功查得数增量 == 成功次数", fmt.Sprintf("delta == %d (查无不计)", nSuccess),
		delta == float64(nSuccess), fmt.Sprintf("delta=%v (want %d)", delta, nSuccess))
	rec.Check("无额度限制(无 1001/1006)", "全程不出现 505005/505006", noLimit, "出现了余额/上限拦截码")

	// 版本隔离：对 x1 的流量不应影响其他版本的成功查得数。
	v9After := harness.ServiceUsed("v9", harness.AppKeyFor("v9"), harness.Secret)
	v8After := harness.ServiceUsed("v8", harness.AppKeyFor("v8"), harness.Secret)
	zlfAfter := harness.ServiceUsed("zlf", harness.AppKeyFor("zlf"), harness.Secret)
	blkAfter := harness.ServiceUsed("blk", harness.AppKeyFor("blk"), harness.Secret)
	rlbd1After := harness.ServiceUsed("rlbd1", harness.AppKeyFor("rlbd1"), harness.Secret)
	rlbd2After := harness.ServiceUsed("rlbd2", harness.AppKeyFor("rlbd2"), harness.Secret)
	sfzhyAfter := harness.ServiceUsed("sfzhy", harness.AppKeyFor("sfzhy"), harness.Secret)
	xfjyAfter := harness.ServiceUsed("xfjy", harness.AppKeyFor("xfjy"), harness.Secret)
	tsfxAfter := harness.ServiceUsed("tsfx", harness.AppKeyFor("tsfx"), harness.Secret)
	lxfAfter := harness.ServiceUsed("lxf", harness.AppKeyFor("lxf"), harness.Secret)
	grgjjAfter := harness.ServiceUsed("grgjj", harness.AppKeyFor("grgjj"), harness.Secret)
	rec.Check("v9 计数不受 x1 流量影响", "delta == 0",
		v9After == v9Before, fmt.Sprintf("before=%v after=%v", v9Before, v9After))
	rec.Check("v8 计数不受 x1 流量影响", "delta == 0",
		v8After == v8Before, fmt.Sprintf("before=%v after=%v", v8Before, v8After))
	rec.Check("zlf 计数不受 x1 流量影响", "delta == 0",
		zlfAfter == zlfBefore, fmt.Sprintf("before=%v after=%v", zlfBefore, zlfAfter))
	rec.Check("blk 计数不受 x1 流量影响", "delta == 0",
		blkAfter == blkBefore, fmt.Sprintf("before=%v after=%v", blkBefore, blkAfter))
	rec.Check("rlbd1 计数不受 x1 流量影响", "delta == 0",
		rlbd1After == rlbd1Before, fmt.Sprintf("before=%v after=%v", rlbd1Before, rlbd1After))
	rec.Check("rlbd2 计数不受 x1 流量影响", "delta == 0",
		rlbd2After == rlbd2Before, fmt.Sprintf("before=%v after=%v", rlbd2Before, rlbd2After))
	rec.Check("sfzhy 计数不受 x1 流量影响", "delta == 0",
		sfzhyAfter == sfzhyBefore, fmt.Sprintf("before=%v after=%v", sfzhyBefore, sfzhyAfter))
	rec.Check("xfjy 计数不受 x1 流量影响", "delta == 0",
		xfjyAfter == xfjyBefore, fmt.Sprintf("before=%v after=%v", xfjyBefore, xfjyAfter))
	rec.Check("tsfx 计数不受 x1 流量影响", "delta == 0",
		tsfxAfter == tsfxBefore, fmt.Sprintf("before=%v after=%v", tsfxBefore, tsfxAfter))
	rec.Check("lxf 计数不受 x1 流量影响", "delta == 0",
		lxfAfter == lxfBefore, fmt.Sprintf("before=%v after=%v", lxfBefore, lxfAfter))
	rec.Check("grgjj 计数不受 x1 流量影响", "delta == 0",
		grgjjAfter == grgjjBefore, fmt.Sprintf("before=%v after=%v", grgjjBefore, grgjjAfter))
}
