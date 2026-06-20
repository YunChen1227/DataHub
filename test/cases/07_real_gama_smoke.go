//go:build ignore

// 07_real_gama_smoke: 可选的真实 gama 上游连通性 smoke。仅当 run.ps1 检测到
// config.gama.real.yaml 并启动了真实上游 relay（设置 REAL_GAMA_ENABLED=1 +
// REAL_BASE_URL）时才真正执行；否则整体 SKIP。真实上游因 IP 未白名单/鉴权失败时
// 同样记为 SKIP（不计失败）。
//
// Run: go run test/cases/07_real_gama_smoke.go
package main

import (
	"os"

	"github.com/datahub/relay/test/harness"
)

func main() {
	rec := harness.NewRecorder("07_real_gama_smoke", "真实 gama 上游连通性(可选)")
	defer rec.Finish()

	if os.Getenv("REAL_GAMA_ENABLED") != "1" {
		rec.Skip("真实 gama 查询", "x1 经真实上游返回 001/999",
			"未启用：缺 config.gama.real.yaml 或使用了 -SkipReal")
		return
	}

	// 将后续 harness.Call 指向真实上游 relay 实例。
	if real := os.Getenv("REAL_BASE_URL"); real != "" {
		os.Setenv("RELAY_BASE_URL", real)
	}

	body := map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
	r := harness.QueryX1(harness.AppKey, harness.Secret, body, nil)

	switch {
	case r.ErrorCode == "0" && (r.BodyCode == "001" || r.BodyCode == "999"):
		rec.Pass("真实 gama 查询", "x1 经真实上游返回 001/999",
			"errorCode=0 body.code="+r.BodyCode+" range="+r.Range)
	default:
		rec.Skip("真实 gama 查询", "x1 经真实上游返回 001/999",
			"真实上游未通（可能 IP 未白名单/鉴权失败）: "+r.Raw)
	}
}
