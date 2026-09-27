# dtly（多头履约行为）上游连通性探测。凭证与 OSS 均从 CONFIG_FILE 读取。
param(
    [string]$ConfigFile = $(if ($env:CONFIG_FILE) { $env:CONFIG_FILE } else { "config.aliyun.prod.yaml" })
)
$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo
$env:CONFIG_FILE = $ConfigFile
go run ./scripts/probe_dtly.go ./scripts/probe_shouxin_common.go
exit $LASTEXITCODE
