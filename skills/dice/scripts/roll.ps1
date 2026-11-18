$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$count = 1
if ($env:FISH_SKILL_ARGS -match '^[1-6]$') {
    $count = [int]$env:FISH_SKILL_ARGS
}
$results = 1..$count | ForEach-Object { Get-Random -Minimum 1 -Maximum 7 }
Write-Output ("掷了 {0} 个骰子：{1}" -f $count, ($results -join '、'))
