//go:build ignore

// 04_found_count: 验证"只统计成功查得数、无额度限制"。读取 /quota 前值 -> 发 N 次
// 成功 + M 次查无 -> 读后值，断言 serviceUsed 增量恰为 N（查无不计），且全过程无任何
// 1001/1006(505005/505006) 拦截。
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
	rec := harness.NewRecorder("04_found_count", "成功查得数统计 + 无额度限制")
	defer rec.Finish()

	before := harness.ServiceUsed(harness.AppKey, harness.Secret)
	if before < 0 {
		rec.Fail("读取 serviceUsed(前)", "数值 >= 0", fmt.Sprintf("%v", before), "无法读取 /quota.serviceUsed")
		return
	}
	fmt.Printf("  serviceUsed(before) = %v\n", before)

	noLimit := true
	for i := 0; i < nSuccess; i++ {
		r := harness.QueryX1(harness.AppKey, harness.Secret, base(), nil)
		if r.ErrorCode == "505005" || r.ErrorCode == "505006" {
			noLimit = false
		}
		rec.Check(fmt.Sprintf("成功查询 #%d", i+1), "errorCode=0 & body.code=001",
			r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)
	}
	for i := 0; i < mNotFnd; i++ {
		nf := base()
		nf["mobile"] = "13800000000"
		r := harness.QueryX1(harness.AppKey, harness.Secret, nf, nil)
		rec.Check(fmt.Sprintf("查无查询 #%d", i+1), "errorCode=0 & body.code=999",
			r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)
	}

	after := harness.ServiceUsed(harness.AppKey, harness.Secret)
	fmt.Printf("  serviceUsed(after) = %v\n", after)
	delta := after - before
	rec.Check("成功查得数增量 == 成功次数", fmt.Sprintf("delta == %d (查无不计)", nSuccess),
		delta == float64(nSuccess), fmt.Sprintf("delta=%v (want %d)", delta, nSuccess))
	rec.Check("无额度限制(无 1001/1006)", "全程不出现 505005/505006", noLimit, "出现了余额/上限拦截码")
}
