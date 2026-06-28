# DataHub fixed test-suite entrypoint (Windows / PowerShell).
#
# Flow: make result dir test_res/<date> -> build + start mock gama(:9112) +
# mock_income(:9113) + mock_rental(:9114) + mock_blacklist(:9115) + relay(:8080,
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
    go build -o $relayExe ./cmd/relay
    if ($LASTEXITCODE -ne 0) { throw "go build relay failed" }

    # postgres 模式：在启动 relay 前重建各版本库 (datahub_*_db)。
    $cfgText = Get-Content -Raw -Path (Join-Path $repo $ConfigFile)
    if ($cfgText -match 'driver:\s*"?postgres"?') {
        Write-Host "postgres mode: recreating three version databases ..."
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
