# DataHub fixed test-suite entrypoint (Windows / PowerShell).
#
# Flow: make result dir test_res/<date> -> build + start mock gama(:9112) +
# mock_income(:9113) + mock_rental(:9114) + mock_blacklist(:9115) + mock_facecompare(:9117) + mock_idverify(:9118) + mock_consumetxn(:9119) + mock_complaint(:9120) + mock_lxscore(:9122) + mock_incomeag(:9123) + mock_bgjj(:9125) + mock_bgpg(:9126) + relay(:8080,
# live Aliyun PG+Redis) -> wait /healthz -> (optional) start real-gama relay(:8090)
# -> run test/cases/*.go in order -> aggregate REPORT.md -> stop services.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File .\test\run.ps1
#   powershell -ExecutionPolicy Bypass -File .\test\run.ps1 -ConfigFile config.aliyun.e2e.yaml -SkipReal
param(
    [string]$ConfigFile = "config.aliyun.e2e.yaml",
    [switch]$SkipReal
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

$date = Get-Date -Format "yyyy-MM-dd"
$resultDir = Join-Path $repo "test_res\$date"
New-Item -ItemType Directory -Force -Path $resultDir | Out-Null

Write-Host "== DataHub test suite =="
Write-Host "  repo      : $repo"
Write-Host "  config    : $ConfigFile"
Write-Host "  resultDir : $resultDir"

$env:CONFIG_FILE       = $ConfigFile
$env:RESULT_DIR        = $resultDir
$env:RELAY_BASE_URL    = "http://localhost:8080"
$env:REAL_GAMA_ENABLED = "0"

$procs = New-Object System.Collections.ArrayList

function Stop-All {
    foreach ($p in $procs) {
        try { if ($p -and -not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } } catch {}
    }
}

function Wait-Health([string]$url, [int]$tries = 40) {
    for ($i = 0; $i -lt $tries; $i++) {
        try {
            $r = Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 3
            if ($r.StatusCode -eq 200) { return $true }
        } catch {}
        Start-Sleep -Milliseconds 500
    }
    return $false
}

$anyFail = $false
try {
    $mockExe      = Join-Path $resultDir "mock_gama.exe"
    $incomeExe    = Join-Path $resultDir "mock_income.exe"
    $rentalExe    = Join-Path $resultDir "mock_rental.exe"
    $blacklistExe = Join-Path $resultDir "mock_blacklist.exe"
    $facecompareExe = Join-Path $resultDir "mock_facecompare.exe"
    $idverifyExe  = Join-Path $resultDir "mock_idverify.exe"
    $consumetxnExe = Join-Path $resultDir "mock_consumetxn.exe"
    $complaintExe = Join-Path $resultDir "mock_complaint.exe"
    $lxscoreExe   = Join-Path $resultDir "mock_lxscore.exe"
    $incomeagExe  = Join-Path $resultDir "mock_incomeag.exe"
    $bgjjExe      = Join-Path $resultDir "mock_bgjj.exe"
    $bgpgExe      = Join-Path $resultDir "mock_bgpg.exe"
    $relayExe     = Join-Path $resultDir "relay.exe"
    Write-Host "building mocks + relay ..."
    go build -o $mockExe ./scripts/mock_gama.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_gama failed" }
    go build -o $incomeExe ./scripts/mock_income.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_income failed" }
    go build -o $rentalExe ./scripts/mock_rental.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_rental failed" }
    go build -o $blacklistExe ./scripts/mock_blacklist.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_blacklist failed" }
    go build -o $facecompareExe ./scripts/mock_facecompare.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_facecompare failed" }
    go build -o $idverifyExe ./scripts/mock_idverify.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_idverify failed" }
    go build -o $consumetxnExe ./scripts/mock_consumetxn.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_consumetxn failed" }
    go build -o $complaintExe ./scripts/mock_complaint.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_complaint failed" }
    go build -o $lxscoreExe ./scripts/mock_lxscore.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_lxscore failed" }
    go build -o $incomeagExe ./scripts/mock_incomeag.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_incomeag failed" }
    go build -o $bgjjExe ./scripts/mock_bgjj.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_bgjj failed" }
    go build -o $bgpgExe ./scripts/mock_bgpg.go
    if ($LASTEXITCODE -ne 0) { throw "go build mock_bgpg failed" }
    go build -o $relayExe ./cmd/relay
    if ($LASTEXITCODE -ne 0) { throw "go build relay failed" }

    # postgres 模式：在启动 relay 前重建各版本库 (datahub_*_db)。
    $cfgText = Get-Content -Raw -Path (Join-Path $repo $ConfigFile)
    if ($cfgText -match 'driver:\s*"?postgres"?') {
        Write-Host "postgres mode: recreating per-domain databases (with demo seed) ..."
        $env:SEED_DEMO = "1"          # e2e 需要各路由的 demo license；生产建库不要设置
        $env:RESET_DESTRUCTIVE = "1"  # 仅测试库：允许 DROP 重建（脚本会硬拒绝生产库）
        go run ./scripts/recreate_databases.go
        if ($LASTEXITCODE -ne 0) { throw "recreate_databases failed" }
    } else {
        Write-Host "memory mode: skipping database recreate."
    }

    $mock = Start-Process -FilePath $mockExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_gama.log") -RedirectStandardError (Join-Path $resultDir "mock_gama.err.log")
    [void]$procs.Add($mock)

    $income = Start-Process -FilePath $incomeExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_income.log") -RedirectStandardError (Join-Path $resultDir "mock_income.err.log")
    [void]$procs.Add($income)

    $rental = Start-Process -FilePath $rentalExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_rental.log") -RedirectStandardError (Join-Path $resultDir "mock_rental.err.log")
    [void]$procs.Add($rental)

    $blacklist = Start-Process -FilePath $blacklistExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_blacklist.log") -RedirectStandardError (Join-Path $resultDir "mock_blacklist.err.log")
    [void]$procs.Add($blacklist)

    $facecompare = Start-Process -FilePath $facecompareExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_facecompare.log") -RedirectStandardError (Join-Path $resultDir "mock_facecompare.err.log")
    [void]$procs.Add($facecompare)

    $idverify = Start-Process -FilePath $idverifyExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_idverify.log") -RedirectStandardError (Join-Path $resultDir "mock_idverify.err.log")
    [void]$procs.Add($idverify)

    $consumetxn = Start-Process -FilePath $consumetxnExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_consumetxn.log") -RedirectStandardError (Join-Path $resultDir "mock_consumetxn.err.log")
    [void]$procs.Add($consumetxn)

    $complaint = Start-Process -FilePath $complaintExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_complaint.log") -RedirectStandardError (Join-Path $resultDir "mock_complaint.err.log")
    [void]$procs.Add($complaint)

    $lxscore = Start-Process -FilePath $lxscoreExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_lxscore.log") -RedirectStandardError (Join-Path $resultDir "mock_lxscore.err.log")
    [void]$procs.Add($lxscore)

    $incomeag = Start-Process -FilePath $incomeagExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_incomeag.log") -RedirectStandardError (Join-Path $resultDir "mock_incomeag.err.log")
    [void]$procs.Add($incomeag)

    $bgjj = Start-Process -FilePath $bgjjExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_bgjj.log") -RedirectStandardError (Join-Path $resultDir "mock_bgjj.err.log")
    [void]$procs.Add($bgjj)

    $bgpg = Start-Process -FilePath $bgpgExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "mock_bgpg.log") -RedirectStandardError (Join-Path $resultDir "mock_bgpg.err.log")
    [void]$procs.Add($bgpg)

    $relay = Start-Process -FilePath $relayExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "relay.log") -RedirectStandardError (Join-Path $resultDir "relay.err.log")
    [void]$procs.Add($relay)

    Write-Host "waiting for relay /healthz ..."
    if (-not (Wait-Health "http://localhost:8080/healthz")) {
        throw "relay /healthz not ready; see $resultDir\relay.err.log (PG/Redis connect or migration failure)"
    }
    Write-Host "relay is up."

    $realCfg = Join-Path $repo "config.gama.real.yaml"
    if (-not $SkipReal -and (Test-Path $realCfg)) {
        Write-Host "starting real-gama relay (:8090) from config.gama.real.yaml ..."
        $prev = $env:CONFIG_FILE
        $env:CONFIG_FILE = "config.gama.real.yaml"
        $realRelay = Start-Process -FilePath $relayExe -WorkingDirectory $repo -PassThru -RedirectStandardOutput (Join-Path $resultDir "relay_real.log") -RedirectStandardError (Join-Path $resultDir "relay_real.err.log")
        $env:CONFIG_FILE = $prev
        [void]$procs.Add($realRelay)
        if (Wait-Health "http://localhost:8090/healthz" 20) {
            $env:REAL_GAMA_ENABLED = "1"
            $env:REAL_BASE_URL = "http://localhost:8090"
            Write-Host "real-gama relay is up."
        } else {
            Write-Host "real-gama relay not ready; 07 will be SKIP."
        }
    } else {
        Write-Host "real-gama smoke disabled (no config.gama.real.yaml or -SkipReal); 07 will be SKIP."
    }

    $cases = Get-ChildItem (Join-Path $repo "test\cases\*.go") | Sort-Object Name
    foreach ($c in $cases) {
        $name = [IO.Path]::GetFileNameWithoutExtension($c.Name)
        $log = Join-Path $resultDir "$name.log"
        Write-Host "---- running $name ----"
        go run $c.FullName 2>&1 | Tee-Object -FilePath $log
        if ($LASTEXITCODE -ne 0) { $anyFail = $true }
    }

    Write-Host "---- aggregating report ----"
    go run (Join-Path $repo "test\report.go") $resultDir
    if ($LASTEXITCODE -ne 0) { $anyFail = $true }
}
finally {
    Write-Host "---- stopping services ----"
    Stop-All
}

Write-Host ""
Write-Host "== done. report: $resultDir\REPORT.md =="
if ($anyFail) { exit 1 } else { exit 0 }
