param([string]$Workspace = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path)
$ErrorActionPreference = 'Stop'
$manifest = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'manifest.json') | ConvertFrom-Json
$checked = 0
foreach ($entry in $manifest.files) {
    $live = Join-Path $Workspace $entry.path
    $frozen = Join-Path (Join-Path $PSScriptRoot 'snapshot') $entry.path
    foreach ($target in @($live, $frozen)) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant() -ne $entry.sha256) {
            throw "文件与交付快照不一致：$($entry.path)"
        }
    }
    $checked++
}
Write-Output "交付文件与快照校验通过：$checked/$($manifest.files.Count)"
