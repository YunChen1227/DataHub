//go:build ignore

// probe_snhmd: 读取 CONFIG_FILE 中 versions.snhmd 的凭证，直连守信「司南黑名单」上游一次查询。
//
// 用法:
//   CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/probe_snhmd.go ./scripts/probe_shouxin_common.go
//
// Windows:
//   powershell -ExecutionPolicy Bypass -File .\scripts\probe_snhmd.ps1
package main

import "os"

func main() {
	os.Exit(runShouxinProbe("snhmd", "snhmd / 司南黑名单 (compassblack)"))
}
