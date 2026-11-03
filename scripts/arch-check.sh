#!/usr/bin/env bash
# 架构依赖检查（可挂 CI）。
#
# 规则：
#   ① cmd/* 不得 import 其他 cmd 包；
#   ② internal/* 不得 import cmd/*（依赖方向只能是 cmd → internal）；
#   ③ internal 包之间不得存在循环依赖。
#
# 实现：go list -deps 取每个包的直接 imports 与传递 deps；
# internal 之间的直接边同时作为依赖图输出（[graph] 行）。
#
# 退出码：0 全部通过；1 存在违规或 go list 失败。
set -euo pipefail
cd "$(dirname "$0")/.."

module="$(go list -m)"
if [ -z "$module" ]; then
  echo "[fail] 无法解析模块名" >&2
  exit 1
fi

# 模板输出 ImportPath|Imports(直接)|Deps(传递，含自身)；外部依赖在 awk 里按前缀过滤。
if ! listing="$(go list -deps -f '{{.ImportPath}}|{{join .Imports " "}}|{{join .Deps " "}}' ./...)"; then
  echo "[fail] go list 失败（可能存在编译错误或循环依赖）" >&2
  exit 1
fi

printf '%s\n' "$listing" | awk -v cmdRoot="$module/cmd/" -v internalRoot="$module/internal/" '
function starts_with(s, prefix) {
  return index(s, prefix) == 1
}
{
  split($0, cols, "|")
  pkg = cols[1]
  imports = cols[2]
  depText = " " cols[3] " "
  nImport = split(imports, imp, " ")

  # 依赖图：只展示 internal 包之间的直接依赖
  if (starts_with(pkg, internalRoot)) {
    for (i = 1; i <= nImport; i++) {
      if (starts_with(imp[i], internalRoot)) {
        print "[graph] " pkg " -> " imp[i]
      }
    }
  }

  # ① cmd 不 import 其他 cmd
  if (starts_with(pkg, cmdRoot)) {
    for (i = 1; i <= nImport; i++) {
      if (imp[i] != pkg && starts_with(imp[i], cmdRoot)) {
        print "[violation] 规则①：" pkg " import 了 " imp[i]
        bad1++
      }
    }
  }

  # ② internal 不 import cmd
  if (starts_with(pkg, internalRoot)) {
    for (i = 1; i <= nImport; i++) {
      if (starts_with(imp[i], cmdRoot)) {
        print "[violation] 规则②：" pkg " import 了 " imp[i]
        bad2++
      }
    }
  }

  # ③ internal 包之间无环：A 直接 import B，而 B 的传递依赖里又有 A
  if (starts_with(pkg, internalRoot)) {
    for (i = 1; i <= nImport; i++) {
      target = imp[i]
      if (target != pkg && starts_with(target, internalRoot) && index(depText, " " pkg " ") > 0) {
        print "[violation] 规则③：" pkg " -> " target " 形成循环依赖"
        bad3++
      }
    }
  }
}
END {
  if (bad1 > 0) {
    print "[fail] ① cmd 不得 import 其他 cmd（发现 " bad1 " 处）"
  } else {
    print "[ok] ① cmd 不依赖其他 cmd"
  }
  if (bad2 > 0) {
    print "[fail] ② internal 不得 import cmd（发现 " bad2 " 处）"
  } else {
    print "[ok] ② internal 不依赖 cmd"
  }
  if (bad3 > 0) {
    print "[fail] ③ internal 包之间存在循环依赖（发现 " bad3 " 处）"
  } else {
    print "[ok] ③ internal 包之间无循环依赖"
  }
  exit (bad1 + bad2 + bad3) > 0 ? 1 : 0
}'
