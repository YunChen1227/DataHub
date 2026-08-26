# DataHub 单路由测试入口 (Windows / PowerShell)。
#
# 与 test/run.ps1 (全量套件、线上 PG/Redis) 的区别：本脚本只针对**一条路由**——
# 只构建并启动该路由上游对应的 mock，只跑 test/cases/*_<route>_*.go，relay 走
# memory 存储（不碰线上 PG/Redis，无需建库）。没有被测路由需要的 mock 一律不启动。
#
# 流程：生成该路由的内存态配置 -> 构建 relay + 所需 mock -> 启动 -> 等 /healthz
#      -> 按序跑该路由的用例 -> 汇总 REPORT.md -> 停服。
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File .\test\route.ps1 -Route grsb
#   powershell -ExecutionPolicy Bypass -File .\test\route.ps1 -Route grgjj -KeepAlive
#   powershell -ExecutionPolicy Bypass -File .\test\route.ps1 -Route x1 -ConfigFile config.local.yaml
param(
    [Parameter(Mandatory = $true)][string]$Route,
    [switch]$KeepAlive,
    [string]$ConfigFile
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

# 路由 -> {所需 mock (名字+端口), 内存态 upstreams 配置块}。
# 凭证一律对齐 scripts/mock_*.go 里的 env 缺省值（全部为假值，仅本地 mock 可用）。
# 新增路由时在此追加一项即可（见 .cursor/skills/test-route/SKILL.md）。
$routeMap = @{
    "x1" = @{
        mocks = @(@{ name = "mock_gama"; port = 9112 })
        yaml  = @'
      - kind: "gama"
        baseURL: "http://127.0.0.1:9112/enol/api/v1/doCheck"
        appId: "demo-gama-appid"
        appSecret: "demo-gama-secret"
        apiKey: "gama_ctmz_layer_score"
'@
    }
    "v9" = @{
        mocks = @(@{ name = "mock_income"; port = 9113 })
        yaml  = @'
      - kind: "income"
        baseURL: "http://127.0.0.1:9113/yrzx/finan/net/10w/v9"
        account: "demo-income-account"
        key: "demo-income-key"
'@
    }
    "v8" = @{
        mocks = @(@{ name = "mock_income"; port = 9113 })
        yaml  = @'
      - kind: "income"
        baseURL: "http://127.0.0.1:9113/yrzx/finan/net/10w/v8"
        account: "demo-income-account"
        key: "demo-income-key"
'@
    }
    "zlf" = @{
        mocks = @(@{ name = "mock_rental"; port = 9114 })
        # licenseFile/oss 留空：memory 模式不上传授权书，mock 不校验 licenseUrl。
        yaml  = @'
      - kind: "rental"
        baseURL: "http://127.0.0.1:9114/api/lightning/product/query"
        institutionId: "demo-rental-institution"
        aesKey: "demo-rental-aes1"
'@
    }
    "blk" = @{
        mocks = @(@{ name = "mock_blacklist"; port = 9115 })
        yaml  = @'
      - kind: "blacklist"
        baseURL: "http://127.0.0.1:9115/enol/api/v1/doCheck"
        appId: "demo-blk-appid"
        appSecret: "demo-blk-secret"
        apiKey: "blackIntV35"
        encryptionType: 2
'@
    }
    "rlbd1" = @{
        mocks = @(@{ name = "mock_facecompare"; port = 9117 })
        yaml  = @'
      - kind: "facecompare"
        baseURL: "http://127.0.0.1:9117/v4/face_id_card/yisuo/compare"
        appId: "demo-rlbd1-appid"
        appSecret: "demo-rlbd1-secret"
'@
    }
    "rlbd2" = @{
        mocks = @(@{ name = "mock_facecompare"; port = 9117 })
        yaml  = @'
      - kind: "facecompare"
        baseURL: "http://127.0.0.1:9117/v4/face_id_card/yisuo/compare"
        appId: "demo-rlbd2-appid"
        appSecret: "demo-rlbd2-secret"
'@
    }
    "sfzhy" = @{
        mocks = @(@{ name = "mock_idverify"; port = 9118 })
        yaml  = @'
      - kind: "idverify"
        baseURL: "http://127.0.0.1:9118/api/idCardThreeElements"
        appId: "demo-sfzhy-appid"
        appSecret: "demo-sfzhy-secret"
'@
    }
    "xfjy" = @{
        mocks = @(@{ name = "mock_consumetxn"; port = 9119 })
        # mock 挂在根路径，baseURL 不带 path。appId=sceneid、appSecret=appkey。
        yaml  = @'
      - kind: "consumetxn"
        baseURL: "http://127.0.0.1:9119"
        appId: "demo-xfjy-sceneid"
        appSecret: "demo-xfjy-appkey"
'@
    }
    "tsfx" = @{
        mocks = @(@{ name = "mock_complaint"; port = 9120 })
        # AES key/iv 由 appSecret 派生，aesKey 字段已不再使用（complaint.go 兼容保留）。
        yaml  = @'
      - kind: "complaint"
        baseURL: "http://127.0.0.1:9120"
        apiKey: "demo-tsfx-apikey"
        appSecret: "demo-tsfx-sign"
'@
    }
    "lxf" = @{
        mocks = @(@{ name = "mock_lxscore"; port = 9122 })
        # appId=customerId、apiKey=customerProdId、appSecret=encryptKey (8 字符 DES 密钥)。
        yaml  = @'
      - kind: "lxscore"
        baseURL: "http://127.0.0.1:9122/report/encode"
        appId: "demo-lxf-customer"
        apiKey: "demo-lxf-prod"
        appSecret: "lxfdemo1"
'@
    }
    "grgjj" = @{
        mocks = @(@{ name = "mock_incomeag"; port = 9123 }, @{ name = "mock_bgjj"; port = 9125 })
        # 双源串行寻源：主源 incomeag(priority 0) + 备源 bgjj(priority 10)；
        # certPath 留空走明文 HTTP（mock 不做双向认证）。
        yaml  = @'
      - kind: "incomeag"
        priority: 0
        baseURL: "http://127.0.0.1:9123/yrzx/common/v2/credit/v2"
        account: "demo-grgjj-account"
        key: "demo-grgjj-key"
        aesKey: "MDEyMzQ1Njc4OWFiY2RlZmdoaWprbG1u"
      - kind: "bgjj"
        priority: 10
        baseURL: "http://127.0.0.1:9125/api/nlv2/zl4"
        account: "0000000000005077"
        key: "P8rT2wXyZ9aBcDeFgHiJkLmNoPqRsTuV"
'@
    }
    "grsb" = @{
        mocks = @(@{ name = "mock_bgpg"; port = 9126 })
        # account=请求头 accountId、apiKey=请求头 prodId、aesKey=encryptKey (hex 文本)。
        yaml  = @'
      - kind: "bgpg"
        baseURL: "http://127.0.0.1:9126/api/getData"
        account: "demo-grsb-account"
        apiKey: "BJPG-01"
        aesKey: "0031cee6808eb6d5b0e07536218f1234"
'@
    }
}

$Route = $Route.Trim().ToLower()
if (-not $routeMap.ContainsKey($Route)) {
    $known = ($routeMap.Keys | Sort-Object) -join ", "
    throw "未知路由 '$Route'；已支持：$known。新增路由请先在 test/route.ps1 的 `$routeMap 中登记。"
}
$spec = $routeMap[$Route]

# 只跑该路由自己的用例（约定命名 test/cases/NN_<route>_*.go）。
$cases = @(Get-ChildItem (Join-Path $repo "test\cases\*_${Route}_*.go") -ErrorAction SilentlyContinue | Sort-Object Name)
if ($cases.Count -eq 0) {
    throw "找不到 $Route 的用例文件 test/cases/*_${Route}_*.go；请先按 add-upstream skill 补齐用例。"
}

$date = Get-Date -Format "yyyy-MM-dd"
$resultDir = Join-Path $repo "test_res\$date"
New-Item -ItemType Directory -Force -Path $resultDir | Out-Null

# 未显式指定配置时，生成只含该路由的内存态配置（其余路由无上游，不参与测试）。
$generated = $false
if (-not $ConfigFile) {
    $ConfigFile = "test_res/$date/config.route.$Route.yaml"
    $cfg = @"
# 由 test/route.ps1 自动生成的 $Route 单路由内存态测试配置（非生产/非敏感，凭证均为
# scripts/mock_*.go 的假值缺省）。只配 $Route 一条路由：其余路由无上游、不参与测试。
addr: ":8080"
storage:
  driver: "memory"
upstream:
  timeout: "6s"
billing:
  requeryInterval: "10s"
demo:
  appSecret: "demo-app-secret"   # = test/harness.Secret
  seed: true
admin:
  bootstrapUser: "admin"
  bootstrapPass: "admin12345"
  jwtSecret: "route-test-only-not-secret"
  tokenTTL: "8h"
versions:
  ${Route}:
    upstreams:
$($spec.yaml)
"@
    Set-Content -Path (Join-Path $repo $ConfigFile) -Value $cfg -Encoding UTF8
    $generated = $true
}

$mockNames = ($spec.mocks | ForEach-Object { "$($_.name)(:$($_.port))" }) -join " + "
Write-Host "== DataHub 单路由测试：$Route =="
Write-Host "  repo      : $repo"
Write-Host "  config    : $ConfigFile$(if ($generated) { ' (自动生成, memory)' })"
Write-Host "  mocks     : $mockNames"
Write-Host "  cases     : $(($cases | ForEach-Object { $_.BaseName }) -join ', ')"
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
        try { if ((Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 3).StatusCode -eq 200) { return $true } } catch {}
        Start-Sleep -Milliseconds 400
    }
    return $false
}

$anyFail = $false
try {
    Write-Host "building relay + $($spec.mocks.Count) mock(s) ..."
    $relayExe = Join-Path $resultDir "relay.exe"
    go build -o $relayExe ./cmd/relay
    if ($LASTEXITCODE -ne 0) { throw "go build relay failed" }
    foreach ($m in $spec.mocks) {
        $exe = Join-Path $resultDir "$($m.name).exe"
        go build -o $exe "./scripts/$($m.name).go"
        if ($LASTEXITCODE -ne 0) { throw "go build $($m.name) failed" }
    }

    foreach ($m in $spec.mocks) {
        $exe = Join-Path $resultDir "$($m.name).exe"
        $p = Start-Process -FilePath $exe -WorkingDirectory $repo -PassThru `
            -RedirectStandardOutput (Join-Path $resultDir "$($m.name).log") `
            -RedirectStandardError (Join-Path $resultDir "$($m.name).err.log")
        [void]$procs.Add($p)
    }

    $relay = Start-Process -FilePath $relayExe -WorkingDirectory $repo -PassThru `
        -RedirectStandardOutput (Join-Path $resultDir "relay.log") `
        -RedirectStandardError (Join-Path $resultDir "relay.err.log")
    [void]$procs.Add($relay)

    Write-Host "waiting for relay /healthz ..."
    if (-not (Wait-Health "http://localhost:8080/healthz")) {
        throw "relay /healthz not ready; see $resultDir\relay.err.log"
    }
    Write-Host "relay is up."

    foreach ($c in $cases) {
        Write-Host "---- running $($c.BaseName) ----"
        go run $c.FullName 2>&1 | Tee-Object -FilePath (Join-Path $resultDir "$($c.BaseName).log")
        if ($LASTEXITCODE -ne 0) { $anyFail = $true }
    }

    Write-Host "---- aggregating report ----"
    go run (Join-Path $repo "test\report.go") $resultDir
    if ($LASTEXITCODE -ne 0) { $anyFail = $true }

    if ($KeepAlive) {
        Write-Host ""
        Write-Host "-KeepAlive：mock + relay 仍在运行 (http://localhost:8080)，按 Enter 停服。"
        [void](Read-Host)
    }
}
finally {
    Write-Host "---- stopping services ----"
    Stop-All
}

Write-Host ""
Write-Host "== done. 本次重跑套件：$(($cases | ForEach-Object { $_.BaseName }) -join ', ') =="
Write-Host "== report: $resultDir\REPORT.md（同目录下其它套件为当日历史结果） =="
if ($anyFail) { exit 1 } else { exit 0 }
