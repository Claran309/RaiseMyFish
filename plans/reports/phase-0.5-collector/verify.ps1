param([string]$Workspace = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path)
$ErrorActionPreference = 'Stop'
$manifest = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'manifest.json') | ConvertFrom-Json
foreach ($entry in $manifest.files) {
    foreach ($target in @((Join-Path $Workspace $entry.path), (Join-Path (Join-Path $PSScriptRoot 'snapshot') $entry.path))) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "本轮源码/依赖变化：$($entry.path)" }
    }
}
foreach ($entry in $manifest.evidence_files) {
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "本轮证据变化：$($entry.path)" }
}
$oldRoot = Join-Path $Workspace 'plans/reports/phase-0.4-scheduler'
$old = Get-Content -Raw -LiteralPath (Join-Path $oldRoot 'manifest.json') | ConvertFrom-Json
$protected = 0
foreach ($entry in $old.files) {
    $frozen = Join-Path (Join-Path $oldRoot 'snapshot') $entry.path
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $frozen).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "0.4旧快照变化：$($entry.path)" }
    # config/fishd.toml和internal/config/config.go是本轮授权增量；其余核心保持验收基线。
    if ($entry.path -match '^(internal/(scheduler|store|event|database)/|contracts/|migrations/|go\.mod$|go\.sum$)') {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "已验收核心变化：$($entry.path)" }
        $protected++
    }
}
Write-Output "本轮源码/快照：$($manifest.files.Count)/$($manifest.files.Count)；证据：$($manifest.evidence_files.Count)；0.4旧快照：$($old.files.Count)/$($old.files.Count)；已验收核心未改：$protected。"
