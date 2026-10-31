param([string]$Workspace = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path)
$ErrorActionPreference = 'Stop'
$manifest = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'manifest.json') | ConvertFrom-Json
foreach ($entry in $manifest.files) {
    foreach ($target in @((Join-Path $Workspace $entry.path), (Join-Path (Join-Path $PSScriptRoot 'snapshot') $entry.path))) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant() -ne $entry.sha256) {
            throw "本轮交付变化：$($entry.path)"
        }
    }
}
foreach ($entry in $manifest.evidence_files) {
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) {
        throw "本轮证据变化：$($entry.path)"
    }
}
$oldRoot = Join-Path $Workspace 'plans/reports/phase-0.3-event-bus'
$old = Get-Content -Raw -LiteralPath (Join-Path $oldRoot 'manifest.json') | ConvertFrom-Json
$unchanged = 0
foreach ($entry in $old.files) {
    $frozen = Join-Path (Join-Path $oldRoot 'snapshot') $entry.path
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $frozen).Hash.ToLowerInvariant() -ne $entry.sha256) {
        throw "0.3旧快照变化：$($entry.path)"
    }
    if ($entry.path -match '^(internal/|contracts/|migrations/)') {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Workspace $entry.path)).Hash.ToLowerInvariant() -ne $entry.sha256) {
            throw "0.3已验收源码变化：$($entry.path)"
        }
        $unchanged++
    }
}
Write-Output "本轮源码/快照：$($manifest.files.Count)/$($manifest.files.Count)；证据：$($manifest.evidence_files.Count)；0.3旧快照：$($old.files.Count)/$($old.files.Count)；已验收源码未改：$unchanged。"
