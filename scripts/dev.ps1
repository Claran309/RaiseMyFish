# RaiseMyFish · 开发期一键启停（Windows PowerShell 5.1+）
#
# 用法（在仓库根目录或任意目录执行均可，脚本自己定位根目录）：
#   .\scripts\dev.ps1 status                         查看 PG / 日志目录 / fishd 进程
#   .\scripts\dev.ps1 build                          只编译，不启动
#   .\scripts\dev.ps1 start  [-Config 配置] [-EnvFile .env] [-StopTimeoutSeconds 15]
#                                                    编译 bin\fishd.exe 并前台运行
#   .\scripts\dev.ps1 stop                           停止 fishd（先温和，超时强杀整棵进程树）
#
# 生命周期（P0-G06）：
#   - 先 `go build -o bin\fishd.exe ./cmd/fishd` 再启动，不再用 `go run`，
#     从根上消除 go run 的「父进程 + 临时子进程」结构，停止时不会留孤儿。
#   - start 前台等待；在 start 所在窗口按 Ctrl+C 时，控制台把 Ctrl+C 同时送给 fishd，
#     fishd 自己走 os.Interrupt 优雅退出；脚本随后等待退出，超过 -StopTimeoutSeconds 强杀。
#   - 跨终端 stop 无法在 Windows 上向任意进程投递 SIGINT，只能先 taskkill（不带 /F）
#     请求关闭，超时后 taskkill /T /F 结束整棵树（含 node 采集子进程）。
#
# 验证步骤：
#   1) .\scripts\dev.ps1 status
#        -> 显示 PostgreSQL、logs\ 与 fishd PID/进程状态；有 fishd 在跑时退出码 0，否则 1
#   2) .\scripts\dev.ps1 build
#        -> 生成 bin\fishd.exe；失败退出码 1
#   3) .\scripts\dev.ps1 start            （建议用禁用通道/模型的临时配置做生命周期验证）
#      在另一个 PowerShell：Get-Process fishd；Get-ChildItem logs
#      -> 应只有一个 fishd.exe；logs\ 已自动创建；.dev-fishd.pid 为 JSON（含 pid/start_utc）
#   4) 在 start 窗口按 Ctrl+C
#      -> 窗口打印 fishd 已退出；Get-Process fishd 应为空（无孤儿）
#   5) 再执行一次 start（保持第一个实例运行）
#      -> 提示「已在运行」并以退出码 1 退出，不会重复启动
#   6) 没有 PID 文件的旧实例（例如历史 go run 实例）：
#        .\scripts\dev.ps1 status   能看到；start 会拒绝；stop 能按进程名兜底结束整棵树
#
# 注意：本脚本是开发期辅助，不代替 `fishctl status`（后者校验 PG/迁移/心跳并返回权威退出码）。

param(
    [Parameter(Position = 0)]
    [ValidateSet("start", "stop", "status", "build")]
    [string]$Action = "status",

    # 传给 fishd 的配置与 dotenv；相对路径按仓库根目录解析
    [string]$Config = "config/fishd.toml",
    [string]$EnvFile = ".env",

    # 停止时等待优雅退出的秒数，超时强杀
    [int]$StopTimeoutSeconds = 15
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$BinDir = Join-Path $Root "bin"
$Exe = Join-Path $BinDir "fishd.exe"
$LogDir = Join-Path $Root "logs"
$PidFile = Join-Path $Root ".dev-fishd.pid"
$StateFile = Join-Path $Root "data/fishd-status.json"

function Resolve-RepoPath([string]$Path) {
    if ([System.IO.Path]::IsPathRooted($Path)) { return $Path }
    return Join-Path $Root $Path
}

function Test-PG {
    try {
        $tcp = New-Object System.Net.Sockets.TcpClient
        $tcp.Connect("127.0.0.1", 5432)
        $tcp.Close()
        return $true
    } catch { return $false }
}

# 按 PID 取进程；StartTime/Path 对他人进程可能无权限，单独 try。
function Get-ProcById([int]$ProcessId) {
    try { return Get-Process -Id $ProcessId -ErrorAction Stop } catch { return $null }
}

function Get-ProcStartUtc($Process) {
    try { return $Process.StartTime.ToUniversalTime() } catch { return $null }
}

function Get-ProcPath($Process) {
    try { return $Process.Path } catch { return $null }
}

# PID 文件记录 {pid,start_utc,exe,config}；兼容旧版纯数字。
function Read-PidRecord {
    if (-not (Test-Path $PidFile)) { return $null }
    $raw = Get-Content -Raw $PidFile -ErrorAction SilentlyContinue
    if (-not $raw -or -not $raw.Trim()) { return $null }
    try { return ($raw | ConvertFrom-Json -ErrorAction Stop) } catch { }
    $n = 0
    if ([int]::TryParse($raw.Trim(), [ref]$n) -and $n -gt 0) {
        return [pscustomobject]@{ pid = $n }
    }
    return $null
}

# 记录里的开始时间与当前进程比对，防 PID 复用误杀；旧记录无 start_utc 时只按 PID。
function Test-RecordOwnsProcess($Record, $Process) {
    if (-not $Record -or -not $Process) { return $false }
    if (-not $Record.start_utc) { return $true }
    $started = Get-ProcStartUtc $Process
    if (-not $started) { return $true }
    try {
        $recorded = [DateTime]::Parse(
            [string]$Record.start_utc,
            [System.Globalization.CultureInfo]::InvariantCulture,
            [System.Globalization.DateTimeStyles]::RoundtripKind).ToUniversalTime()
        return [Math]::Abs(($started - $recorded).TotalSeconds) -lt 2
    } catch { return $false }
}

function Write-PidRecord($Process) {
    $started = Get-ProcStartUtc $Process
    $record = [ordered]@{
        pid       = $Process.Id
        start_utc = if ($started) { $started.ToString("o") } else { $null }
        exe       = $Exe
        config    = $Config
    }
    ($record | ConvertTo-Json -Compress) | Out-File -Encoding ascii $PidFile
}

function Remove-PidRecord {
    Remove-Item $PidFile -Force -ErrorAction SilentlyContinue
}

# 按进程名找 fishd（含历史 go run 实例）；返回 pid/path/started。
function Get-RunningFishd {
    $found = @()
    foreach ($p in @(Get-Process -Name fishd -ErrorAction SilentlyContinue)) {
        $found += [pscustomobject]@{
            Pid     = $p.Id
            Path    = Get-ProcPath $p
            Started = Get-ProcStartUtc $p
        }
    }
    return $found
}

function Ensure-LogsDir {
    if (-not (Test-Path $LogDir)) {
        New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
        Write-Host "[✓] 已创建日志目录 $LogDir" -ForegroundColor Green
    }
}

function Build-Fishd {
    if (-not (Test-Path $BinDir)) {
        New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    }
    $started = Get-Date
    # -C 固定到仓库根目录：脚本从任意目录执行都能编译，不依赖调用方 cwd。
    & go build -C $Root -o $Exe ./cmd/fishd
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[!] go build 失败，未启动" -ForegroundColor Red
        exit 1
    }
    $seconds = ((Get-Date) - $started).TotalSeconds
    Write-Host ("[✓] 已编译 {0}（{1:N1}s）" -f $Exe, $seconds) -ForegroundColor Green
}

# 停止一个 fishd：先温和请求，超时强杀整棵进程树。返回是否已退出。
function Stop-FishdTree([int]$TargetPid, [int]$TimeoutSeconds) {
    if (-not (Get-ProcById $TargetPid)) { return $true }

    # 温和请求（不带 /F）。控制台程序可能忽略，因此必须保留超时强杀兜底。
    & taskkill.exe /PID $TargetPid 2>$null | Out-Null
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-ProcById $TargetPid)) { return $true }
        Start-Sleep -Milliseconds 250
    }

    Write-Host "[!] $TimeoutSeconds 秒内未优雅退出，强制结束进程树 (PID $TargetPid)。" -ForegroundColor Yellow
    & taskkill.exe /PID $TargetPid /T /F 2>$null | Out-Null
    Start-Sleep -Milliseconds 500
    return -not [bool](Get-ProcById $TargetPid)
}

function Assert-NotRunning {
    $record = Read-PidRecord
    if ($record) {
        $proc = Get-ProcById ([int]$record.pid)
        if ($proc -and (Test-RecordOwnsProcess $record $proc)) {
            Write-Host "[!] fishd 已在运行（PID $($record.pid)，来自 $PidFile），不重复启动。" -ForegroundColor Yellow
            Write-Host "    查看：.\scripts\dev.ps1 status    停止：.\scripts\dev.ps1 stop" -ForegroundColor Yellow
            exit 1
        }
        if ($proc) {
            Write-Host "[!] PID 文件记录的 PID $($record.pid) 已被其他进程占用（PID 复用），清理旧记录。" -ForegroundColor Yellow
        }
        Remove-PidRecord
    }

    $running = @(Get-RunningFishd)
    if ($running.Count -gt 0) {
        Write-Host "[!] 已有 fishd 进程在运行，不重复启动：" -ForegroundColor Yellow
        foreach ($r in $running) {
            Write-Host ("    PID {0}  启动 {1}  {2}" -f $r.Pid, $r.Started, $r.Path) -ForegroundColor Yellow
        }
        Write-Host "    确认要停：.\scripts\dev.ps1 stop（会结束整棵进程树）" -ForegroundColor Yellow
        exit 1
    }

    # 兜底：进程名被改过时，用状态文件再确认一次
    if (Test-Path $StateFile) {
        try {
            $state = Get-Content -Raw $StateFile | ConvertFrom-Json
            $sp = Get-ProcById ([int]$state.pid)
            if ($sp -and $sp.ProcessName -eq "fishd") {
                Write-Host "[!] 状态文件显示 fishd 仍在运行（PID $($state.pid)），不重复启动。" -ForegroundColor Yellow
                exit 1
            }
        } catch { }
    }
}

function Start-Dev {
    $configPath = Resolve-RepoPath $Config
    $envPath = Resolve-RepoPath $EnvFile

    if (-not (Test-Path $envPath)) {
        Write-Host "[!] 缺少 $envPath，请从 .env.example 复制并填写" -ForegroundColor Red
        exit 1
    }
    if (-not (Test-Path $configPath)) {
        Write-Host "[!] 配置文件不存在：$configPath" -ForegroundColor Red
        exit 1
    }
    if (-not (Test-PG)) {
        Write-Host "[!] PostgreSQL 未运行（127.0.0.1:5432）" -ForegroundColor Red
        exit 1
    }

    Ensure-LogsDir
    Assert-NotRunning
    Build-Fishd

    $stdout = Join-Path $LogDir "fishd-stdout.log"
    $stderr = Join-Path $LogDir "fishd-stderr.log"

    Write-Host "[*] 启动 fishd（配置 $Config；此窗口 Ctrl+C 优雅退出）..." -ForegroundColor Cyan
    $proc = Start-Process -FilePath $Exe `
        -ArgumentList @("--config", $configPath, "--env-file", $envPath) `
        -WorkingDirectory $Root -NoNewWindow -PassThru `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    Write-PidRecord $proc
    Write-Host "[✓] fishd 已启动 (PID $($proc.Id))" -ForegroundColor Green

    try {
        Wait-Process -Id $proc.Id -ErrorAction Stop
        Write-Host "[*] fishd 已自然退出" -ForegroundColor Cyan
    } finally {
        # Ctrl+C 中断 Wait-Process 时也走到这里：fishd 已收到 Ctrl+C，
        # 这里只负责等它退出；超时则强杀整棵树并清理 PID 文件。
        $stopped = Stop-FishdTree $proc.Id $StopTimeoutSeconds
        if (-not $stopped) {
            Write-Host "[!] fishd (PID $($proc.Id)) 仍未退出，请手动检查" -ForegroundColor Red
        }
        Remove-PidRecord
    }
}

function Stop-Dev {
    $targets = @()
    $record = Read-PidRecord
    if ($record) {
        $proc = Get-ProcById ([int]$record.pid)
        if ($proc -and (Test-RecordOwnsProcess $record $proc)) {
            $targets += [int]$record.pid
        } elseif ($proc) {
            Write-Host "[!] PID 文件与当前进程不匹配（PID $($record.pid) 可能被复用），不按该 PID 停止。" -ForegroundColor Yellow
        }
    }
    Remove-PidRecord

    if ($targets.Count -eq 0) {
        # 没有 PID 文件（历史 go run 实例、手工启动）→ 按进程名兜底
        $targets = @(Get-RunningFishd | ForEach-Object { [int]$_.Pid })
    }
    if ($targets.Count -eq 0) {
        Write-Host "[*] 没有运行中的 fishd" -ForegroundColor Yellow
        return
    }

    $failed = @()
    foreach ($targetPid in $targets) {
        Write-Host "[*] 停止 fishd (PID $targetPid)..." -ForegroundColor Cyan
        if (Stop-FishdTree $targetPid $StopTimeoutSeconds) {
            Write-Host "[✓] 已停止 (PID $targetPid)" -ForegroundColor Green
        } else {
            Write-Host "[!] 停止失败 (PID $targetPid)" -ForegroundColor Red
            $failed += $targetPid
        }
    }
    if ($failed.Count -gt 0) { exit 1 }
}

function Show-Status {
    Write-Host "=== RaiseMyFish 开发状态 ===" -ForegroundColor Cyan
    Write-Host ("PostgreSQL : " + $(if (Test-PG) { "运行中" } else { "未运行" }))
    Write-Host ("日志目录   : " + $(if (Test-Path $LogDir) { "$LogDir（已存在）" } else { "$LogDir（不存在，start 时自动创建）" }))
    Write-Host ("可执行文件 : " + $(if (Test-Path $Exe) { "$Exe" } else { "$Exe（未编译）" }))

    $record = Read-PidRecord
    if ($record) {
        $proc = Get-ProcById ([int]$record.pid)
        if ($proc -and (Test-RecordOwnsProcess $record $proc)) {
            Write-Host ("fishd      : 运行中（PID $($record.pid)）" + $(if ($record.start_utc) { "，启动 $($record.start_utc)" } else { "" }))
        } elseif ($proc) {
            Write-Host ("fishd      : PID 文件记录的 $($record.pid) 已被其他进程占用（PID 复用）") -ForegroundColor Yellow
        } else {
            Write-Host ("fishd      : 已退出（PID 文件残留，PID $($record.pid)）") -ForegroundColor Yellow
        }
    } else {
        Write-Host "fishd      : 无 PID 文件"
    }

    $running = @(Get-RunningFishd)
    if ($running.Count -gt 0) {
        Write-Host "同名进程   :" -ForegroundColor Cyan
        foreach ($r in $running) {
            Write-Host ("    PID {0}  启动 {1}  {2}" -f $r.Pid, $r.Started, $r.Path)
        }
    } else {
        Write-Host "同名进程   : 无"
    }

    if (Test-Path $StateFile) {
        try {
            $state = Get-Content -Raw $StateFile | ConvertFrom-Json
            Write-Host ("状态文件   : pid=$($state.pid) state=$($state.state) db=$($state.database) updated=$($state.updated_at)")
        } catch {
            Write-Host "状态文件   : 解析失败（$StateFile）" -ForegroundColor Yellow
        }
    }

    if ($running.Count -gt 0) { exit 0 }
    exit 1
}

switch ($Action) {
    "start"  { Start-Dev }
    "stop"   { Stop-Dev }
    "status" { Show-Status }
    "build"  { Ensure-LogsDir; Build-Fishd }
}
