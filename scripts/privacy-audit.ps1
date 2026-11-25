# 列出/移除已跟踪的个人材料；默认只报告，-Apply 才改 git 索引（不 commit）。
param(
  [switch]$Apply
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$all = git ls-files
$personal = $all | Where-Object {
  $_ -match '^(config/fishd\.toml|docs/handoff-.*\.md|docs/reviews/|docs/update/|docs/releases/|shots/|cards/|agents/|plans/|personas/)'
}
$bytes = 0
foreach ($p in $personal) {
  $item = Get-Item -LiteralPath $p -ErrorAction SilentlyContinue
  if ($item) { $bytes += $item.Length }
}

Write-Host ("个人/过程文件：{0} 个，{1:N2} MB" -f $personal.Count, ($bytes / 1MB))
Write-Host "高风险命中（只列文件）："
# 真实房间号/UID 不写进本脚本；需要时用本地私密 pattern 运行：
#   rg -l -i '<本机私密 pattern>' config docs plans shots cards personas agents scripts
rg -l -i 'SESSDATA=|bili_jct=|D:/CodeStudy|真实直播间|观众昵称' config docs plans shots cards personas agents scripts 2>$null

if (-not $Apply) {
  Write-Host ""
  Write-Host "只报告，未改索引。确认后执行："
  Write-Host "  .\scripts\privacy-audit.ps1 -Apply"
  Write-Host "然后用 git status 复核；本脚本不会 commit/push。"
  exit 0
}

# 保留本地文件，仅从索引移除，避免下一次提交继续上传。
$personal | ForEach-Object { git rm --cached --quiet -- $_ }
Write-Host "已从索引移除 $($personal.Count) 个文件；本地文件仍在。请复核 git status。"
