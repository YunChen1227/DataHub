# grgjj 双源串行寻源 (命中即停) 内存态冒烟。
# 只起 mock_incomeag(:9123) + mock_bgjj(:9125) + relay(:8080, memory, config.smoke.yaml)，
# 跑 test/cases/21_grgjj_failover.go，并用 bgjj 的 /__count 断言「主源命中即停时备源零调用」。
#
# Usage: powershell -ExecutionPolicy Bypass -File .\test\smoke_grgjj_failover.ps1
$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

$out = Join-Path $repo "test_res\smoke_failover"
New-Item -ItemType Directory -Force -Path $out | Out-Null

$env:CONFIG_FILE    = "config.smoke.yaml"
$env:RELAY_BASE_URL = "http://localhost:8080"

$procs = New-Object System.Collections.ArrayList
function Stop-All { foreach ($p in $procs) { try { if ($p -and -not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } } catch {} } }
function Wait-Health([string]$url, [int]$tries = 40) {
    for ($i = 0; $i -lt $tries; $i++) {
        try { if ((Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 3).StatusCode -eq 200) { return $true } } catch {}
        Start-Sleep -Milliseconds 400
    }
    return $false
}

$fail = $false
try {
    $incomeagExe = Join-Path $out "mock_incomeag.exe"
    $bgjjExe     = Join-Path $out "mock_bgjj.exe"
    $relayExe    = Join-Path $out "relay.exe"

    Write-Host "building relay + mocks ..."
    go build -o $incomeagExe ./scripts/mock_incomeag.go; if ($LASTEXITCODE -ne 0) { throw "build mock_incomeag failed" }
    go build -o $bgjjExe ./scripts/mock_bgjj.go;          if ($LASTEXITCODE -ne 0) { throw "build mock_bgjj failed" }
    go build -o $relayExe ./cmd/relay;                    if ($LASTEXITCODE -ne 0) { throw "build relay failed" }

    $mi = Start-Process -FilePath $incomeagExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $out "mock_incomeag.log") -RedirectStandardError (Join-Path $out "mock_incomeag.err.log")
    [void]$procs.Add($mi)
    $mb = Start-Process -FilePath $bgjjExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $out "mock_bgjj.log") -RedirectStandardError (Join-Path $out "mock_bgjj.err.log")
    [void]$procs.Add($mb)
    $rl = Start-Process -FilePath $relayExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $out "relay.log") -RedirectStandardError (Join-Path $out "relay.err.log")
    [void]$procs.Add($rl)

    if (-not (Wait-Health "http://localhost:8080/healthz")) { throw "relay /healthz not ready; see $out\relay.err.log" }
    Write-Host "relay is up."

    # 命中即停断言：记录一次主源查得前后的备源调用计数，应保持不变。
    $before = (Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:9125/__count").Content
    Write-Host "---- running 21_grgjj_failover ----"
    go run (Join-Path $repo "test\cases\21_grgjj_failover.go") 2>&1 | Tee-Object -FilePath (Join-Path $out "21_grgjj_failover.log")
    if ($LASTEXITCODE -ne 0) { $fail = $true }
    $after = (Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:9125/__count").Content

    # 用例里 3 次备源应答请求 (两次 13900000000 回落查得 + 一次 13800000000 全查无)。
    # 主源命中的那次 (13809091009) 不得触达备源；故备源被调用次数应恰为 3。
    $delta = [int]$after - [int]$before
    Write-Host "bgjj /__count delta = $delta (期望 3：13900000000 x2 + 13800000000 x1；主源命中的 13809091009 不触达备源)"
    if ($delta -ne 3) { Write-Host "命中即停断言失败：备源调用次数异常"; $fail = $true }
    else { Write-Host "命中即停断言通过：主源查得时备源零调用。" }
}
finally { Stop-All }

if ($fail) { Write-Host "SMOKE FAILED"; exit 1 } else { Write-Host "SMOKE PASSED"; exit 0 }
