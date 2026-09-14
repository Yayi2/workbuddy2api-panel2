# build.ps1 —— workbuddy2api 安全编译脚本
#
# 设计原则（重要）：
#   【绝不触碰账号数据】产物输出到 dist/（本项目既有约定），但 dist/ 与项目根下的
#   auths/、data/、config.json 是用户数据。本脚本只写 .exe，这些路径只读不写。
#
# 为什么需要它：
#   Go 默认把构建缓存写到 %LOCALAPPDATA%\go-build、模块缓存写到 %USERPROFILE%\go\pkg\mod，
#   在受限环境下缓存写入被拒会导致编译失败。本脚本把 GOCACHE/GOMODCACHE 锚定到
#   项目外的 .build-workbuddy2api 目录，产物则按本项目约定放进 dist/。
#   dist/ 里同时存在运行数据（auths/、data/、config.json），故编译前后都做只读核对。
#
# 用法：
#   .\build.ps1              编译到 dist\wb2api.exe（默认）
#   .\build.ps1 -Test        编译并跑测试
#   .\build.ps1 -Run         编译并从 dist\ 启动（Ctrl+C 停止）
#   .\build.ps1 -Clean       清建构缓存（项目外；【不影响】dist/ 与账号数据）

[CmdletBinding()]
param(
    [switch]$Test,
    [switch]$Run,
    [switch]$Clean
)

$ErrorActionPreference = 'Stop'

# ── 路径锚定 ────────────────────────────────────────────────────────────────
# $PSScriptRoot 是本脚本所在目录（项目根）。不要用 $PWD——从别处调用会锚错。
$ProjectDir = $PSScriptRoot
# 产物输出到 dist/（本项目既有约定，.gitignore 已忽略该目录）。
# 注意：dist/ 同时是**运行目录**——里面有 auths/、data/、config.json 等真实数据。
# 因此编译只允许覆盖 .exe，绝不触碰 dist/ 下的数据文件（见下方守卫）。
$DistDir    = Join-Path $ProjectDir 'dist'
$BinDir     = $DistDir
$ExePath    = Join-Path $BinDir 'wb2api.exe'

# 构建缓存仍放在项目外（缓存是纯临时文件，不该污染仓库）。
$BuildRoot  = Join-Path (Split-Path $ProjectDir -Parent) '.build-workbuddy2api'
$CacheDir   = Join-Path $BuildRoot 'cache'
$ModCache   = Join-Path $BuildRoot 'modcache'

# ── 数据目录守卫 ────────────────────────────────────────────────────────────
# dist/ 里混着可执行文件与用户数据（auths/、data/、config.json）。
# 本脚本只写 .exe 一类产物，绝不删除/覆盖下列路径。
$ProtectedData = @(
    (Join-Path $DistDir 'auths'),        # 运行目录里的账号凭据
    (Join-Path $DistDir 'data'),         # 运行目录里的池状态
    (Join-Path $DistDir 'config.json'),  # 运行目录里的配置（含 api_key）
    (Join-Path $ProjectDir 'auths'),     # 项目根的账号凭据（备用位置）
    (Join-Path $ProjectDir 'data'),      # 项目根的池状态（备用位置）
    (Join-Path $ProjectDir 'config.json')
)

function Assert-SafeDeleteTarget {
    param([string]$Target)
    $full = [System.IO.Path]::GetFullPath($Target)
    $proj = [System.IO.Path]::GetFullPath($ProjectDir)
    # 拒绝删除项目内任何路径：dist/ 里混有用户数据（auths/、data/、config.json），
    # 项目根的 auths/、data/ 同理。构建缓存只允许存在于项目之外。
    if ($full.StartsWith($proj, [StringComparison]::OrdinalIgnoreCase)) {
        throw "拒绝删除项目内路径（数据安全守卫）: $full"
    }
    $br = [System.IO.Path]::GetFullPath($BuildRoot)
    if ($full -eq $br -or $full.StartsWith($br, [StringComparison]::OrdinalIgnoreCase)) {
        return
    }
    throw "拒绝删除构建目录之外的路径: $full"
}

# ── Clean ───────────────────────────────────────────────────────────────────
if ($Clean) {
    Assert-SafeDeleteTarget $BuildRoot
    if (Test-Path $BuildRoot) {
        Remove-Item $BuildRoot -Recurse -Force
        Write-Host "已清空构建缓存: $BuildRoot" -ForegroundColor Yellow
    } else {
        Write-Host "构建缓存不存在，无需清理" -ForegroundColor DarkGray
    }
    Write-Host ""
    Write-Host "注意：-Clean 只清构建缓存（项目外），不会删除 dist/ 下的产物与数据。" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "账号数据完好（本脚本从不触碰）:" -ForegroundColor Green
    foreach ($p in $ProtectedData) {
        $mark = if (Test-Path $p) { "存在" } else { "不存在" }
        Write-Host ("  {0,-14} {1}" -f $mark, $p)
    }
    return
}

# ── 环境准备 ────────────────────────────────────────────────────────────────
foreach ($d in @($CacheDir, $ModCache, $BinDir)) {
    if (-not (Test-Path $d)) { New-Item -ItemType Directory -Force -Path $d | Out-Null }
}
$env:GOCACHE    = $CacheDir
$env:GOMODCACHE = $ModCache
$env:GOFLAGS    = '-mod=mod'

Write-Host "项目目录 : $ProjectDir"
Write-Host "构建缓存 : $CacheDir"
Write-Host "模块缓存 : $ModCache"
Write-Host "产物目录 : $DistDir"
Write-Host ""

# ── 编译 ────────────────────────────────────────────────────────────────────
# 四个入口全部编译。此前只编 ./cmd/server 会导致 dist/ 里的 login/credit/signin
# 停留在旧版本（用户看不出区别，直到用到才发现行为不一致）。
# 产物名沿用本项目既有约定：signin → signin_bin.exe（与 .gitignore 记录一致）。
$Targets = @(
    @{ Pkg = './cmd/server'; Out = 'wb2api.exe' },
    @{ Pkg = './cmd/login';  Out = 'login.exe' },
    @{ Pkg = './cmd/credit'; Out = 'credit.exe' },
    @{ Pkg = './cmd/signin'; Out = 'signin_bin.exe' }
)

Push-Location $ProjectDir
try {
    Write-Host "[1/3] go build（$($Targets.Count) 个入口）..." -ForegroundColor Cyan
    foreach ($t in $Targets) {
        $out = Join-Path $DistDir $t.Out
        & go build -o $out $t.Pkg
        if ($LASTEXITCODE -ne 0) { throw "go build $($t.Pkg) 失败 (exit $LASTEXITCODE)" }
        $size = [math]::Round((Get-Item $out).Length / 1MB, 2)
        Write-Host ("      {0,-16} {1,6} MB" -f $t.Out, $size) -ForegroundColor Green
    }

    if ($Test) {
        Write-Host "[2/3] go test ./... ..." -ForegroundColor Cyan
        & go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test 失败 (exit $LASTEXITCODE)" }
        Write-Host "      测试通过" -ForegroundColor Green
    } else {
        Write-Host "[2/3] 跳过测试（加 -Test 运行）" -ForegroundColor DarkGray
    }
} finally {
    Pop-Location
}

# ── 运行 ────────────────────────────────────────────────────────────────────
if ($Run) {
    Write-Host ""
    Write-Host "启动服务（Ctrl+C 停止）..." -ForegroundColor Cyan
    # 从项目目录启动：config.json / auths/ / data/ 都按相对路径解析。
    Push-Location $ProjectDir
    try { & $ExePath } finally { Pop-Location }
    return
}

Write-Host ""
Write-Host "数据目录状态（只读检查，未做任何修改）:" -ForegroundColor Green
foreach ($p in $ProtectedData) {
    if (-not (Test-Path $p)) {
        Write-Host ("  不存在        {0}" -f $p)
        continue
    }
    $item = Get-Item $p
    if ($item.PSIsContainer) {
        $n = (Get-ChildItem $p -Force -ErrorAction SilentlyContinue).Count
        Write-Host ("  {0,-12} 项    {1}" -f $n, $p)
    } else {
        Write-Host ("  {0,-12} 字节  {1}  (最后修改 {2})" -f $item.Length, $p, $item.LastWriteTime)
    }
}
Write-Host ""
Write-Host "运行: .\build.ps1 -Run" -ForegroundColor Cyan
