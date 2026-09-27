//go:build ignore

// probe_dtjd: 读取 CONFIG_FILE 中 versions.dtjd 的凭证，直连守信「多头借贷行为」上游一次查询。
//
// 用法（在 DataHub 仓库根目录）:
//   CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/probe_dtjd.go ./scripts/probe_shouxin_common.go
//
// Windows:
//   powershell -ExecutionPolicy Bypass -File .\scripts\probe_dtjd.ps1
package main

import "os"

func main() {
	os.Exit(runShouxinProbe("dtjd", "dtjd / 多头借贷行为 (multiloan)"))
}
