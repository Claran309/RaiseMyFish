param([string]$Workspace = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path)
$ErrorActionPreference = 'Stop'
$manifest = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'manifest.json') | ConvertFrom-Json
foreach ($entry in $manifest.files) {
    foreach ($target in @((Join-Path $Workspace $entry.path), (Join-Path (Join-Path $PSScriptRoot 'snapshot') $entry.path))) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "本轮交付变化：$($entry.path)" }
    }
}
foreach ($entry in $manifest.evidence_files) {
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "本轮证据变化：$($entry.path)" }
}
$oldRoot = Join-Path $Workspace 'plans/reports/phase-0.5-collector'
$old = Get-Content -Raw -LiteralPath (Join-Path $oldRoot 'manifest.json') | ConvertFrom-Json
foreach ($entry in $old.files) {
    $frozen = Join-Path (Join-Path $oldRoot 'snapshot') $entry.path
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $frozen).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "0.5旧快照变化：$($entry.path)" }
    if ($entry.path -in @('migrations/0001_init.sql','migrations/0002_event_leases.sql','migrations/seed.sql','go.mod','go.sum','personas/dafeiyu/meta.yaml','personas/dafeiyu/rules.md','internal/personaeval/score_v5.go')) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) { throw "冻结旧迁移/人格/判断器变化：$($entry.path)" }
    }
}
Write-Output "本轮源码/快照：$($manifest.files.Count)/$($manifest.files.Count)；证据：$($manifest.evidence_files.Count)；0.5旧快照：$($old.files.Count)/$($old.files.Count)；旧迁移/人格/v5保留。"
