//go:build ignore

// 04_found_count: 验证"只统计成功查得数、无额度限制"且三版本统计互相独立。
// 对每个版本各自读 /quota 前值 -> 发 N 次成功 + M 次查无 -> 读后值，断言该版本
// serviceUsed 增量恰为 N（查无不计）；并断言只对 x1 发流量时，v9/v8 计数不变（隔离）。
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

	// 记录三版本初始计数，用于稍后验证隔离。
	v9Before := harness.ServiceUsed("v9", harness.AppKey, harness.Secret)
	v8Before := harness.ServiceUsed("v8", harness.AppKey, harness.Secret)

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

	// 版本隔离：对 x1 的流量不应影响 v9/v8 的成功查得数。
	v9After := harness.ServiceUsed("v9", harness.AppKey, harness.Secret)
	v8After := harness.ServiceUsed("v8", harness.AppKey, harness.Secret)
	rec.Check("v9 计数不受 x1 流量影响", "delta == 0",
		v9After == v9Before, fmt.Sprintf("before=%v after=%v", v9Before, v9After))
	rec.Check("v8 计数不受 x1 流量影响", "delta == 0",
		v8After == v8Before, fmt.Sprintf("before=%v after=%v", v8Before, v8After))
}
