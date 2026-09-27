//go:build ignore

// probe_dtly: 读取 CONFIG_FILE 中 versions.dtly 的凭证，直连守信「多头履约行为」上游一次查询。
//
// 用法:
//   CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/probe_dtly.go ./scripts/probe_shouxin_common.go
//
// Windows:
//   powershell -ExecutionPolicy Bypass -File .\scripts\probe_dtly.ps1
package main

import "os"

func main() {
	os.Exit(runShouxinProbe("dtly", "dtly / 多头履约行为 (manyoverdue)"))
}
