param([string]$Workspace = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path)
$ErrorActionPreference = 'Stop'
$manifest = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'manifest.json') | ConvertFrom-Json
foreach ($entry in $manifest.files) {
    foreach ($target in @((Join-Path $Workspace $entry.path), (Join-Path (Join-Path $PSScriptRoot 'snapshot') $entry.path))) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant() -ne $entry.sha256) {
            throw "文件与交付快照不一致：$($entry.path)"
        }
    }
}
foreach ($entry in $manifest.evidence_files) {
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) {
        throw "本轮证据变化：$($entry.path)"
    }
}
$oldRoot = Join-Path $Workspace 'plans/reports/phase-0.2-storage'
$old = Get-Content -Raw -LiteralPath (Join-Path $oldRoot 'manifest.json') | ConvertFrom-Json
foreach ($entry in $old.files) {
    $frozen = Join-Path (Join-Path $oldRoot 'snapshot') $entry.path
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $frozen).Hash.ToLowerInvariant() -ne $entry.sha256) {
        throw "0.2旧快照变化：$($entry.path)"
    }
    if ($entry.path -in @('migrations/0001_init.sql','migrations/seed.sql')) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) {
            throw "已应用SQL变化：$($entry.path)"
        }
    }
}
Write-Output "本轮交付与源码快照通过：$($manifest.files.Count)/$($manifest.files.Count)；0.2旧快照：$($old.files.Count)/$($old.files.Count)；v1 SQL原字节保留。"
