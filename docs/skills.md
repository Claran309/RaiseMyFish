# 技能包系统（Skills）

> 状态：MVP 已实现。技能是**文件驱动**的：给模型一段操作说明，可选一个本地脚本。
> 它不替代 ReAct 工具，而是让「怎么做事」可以按目录热插拔。

---

## 1. 目录结构

```
skills/
  white-rice/
    SKILL.md
  dice/
    SKILL.md
    scripts/
      roll.ps1
```

每个技能一个目录，目录名就是技能名（小写字母/数字/短横线）。

## 2. SKILL.md 格式

```markdown
---
name: dice
description: 观众要求掷骰子、随机数或抽签时，用本地脚本产生真实随机结果
version: 1
entry: scripts/roll.ps1   # 可选；没有脚本就只是说明
timeout_seconds: 5        # 可选，默认 10，不超过 [skills].max_timeout_seconds
---

# 掷骰子技能

适用场景、调用约定、回复方式……
```

字段规则：

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | ✅ | 必须与目录名一致，`[a-z0-9-]`，1-64 字符 |
| `description` | ✅ | 给 `skill_list` 看的用途摘要，最多 200 字 |
| `version` | ✅ | 正整数 |
| `entry` | ❌ | 技能目录内的相对脚本路径，支持 `.ps1/.bat/.cmd/.sh` |
| `timeout_seconds` | ❌ | 脚本超时，默认 10 秒 |

正文是给模型读的完整操作说明；`skill_read` 返回正文。

## 3. ReAct 工具

装配 `[skills] enabled=true` 后，所有 ReAct 通道会多三个工具：

| 工具 | 作用 | args |
|---|---|---|
| `skill_list` | 列出技能名与用途 | 忽略 |
| `skill_read` | 读技能说明或技能内参考文件，按页返回 | 技能名；或 JSON：`{"name":"character-card-author","file":"genres/_index.md","offset":0,"limit":1800}` |
| `skill_run` | 执行技能声明的脚本 | JSON：`{"name":"dice","args":"2"}`；也接受裸技能名（无参数） |

脚本通过环境变量接收参数：

- `FISH_SKILL_NAME`：技能名
- `FISH_SKILL_DIR`：技能目录
- `FISH_SKILL_ARGS`：`skill_run` 的 args 原文（≤4KB）

脚本的 stdout/stderr 合并返回，受 `max_output_bytes` 限制。

### 上下文预算

技能**不会把整包正文自动注入** ReAct prompt：

- `skill_list` 只返回名称 + 简短描述，`Toolbox` 上限 900 字符；
- `skill_read` 默认只返回 1800 字符，`file` 可读技能内文本参考（`.md/.txt/.json/.yaml/.yml/.csv/.toml`），用 `offset/limit` 分页，单次最多 2400 字符；
- `skill_run` 输出经 `max_output_bytes` 裁剪。

因此像 Foreverse 这种带 47 份分类指南的大包，只会在模型明确需要时读一小段，而不是整包进上下文。

## 4. 配置

```toml
[skills]
enabled = true
dir = "skills"
allow_scripts = true       # false 时 skill_run 一律拒绝
check_seconds = 30         # 热重载检查间隔
max_timeout_seconds = 30
max_output_bytes = 8000
allowed = []               # 空=全部；只允许列出的技能名
```

热重载是**按需**的：`skill_list/skill_read/skill_run` 调用时若超过 `check_seconds` 才重新扫描目录，不额外开 goroutine。

## 5. 安全边界

- 脚本路径必须留在技能目录内，拒绝绝对路径和 `..` 越界。
- 只允许 `.ps1/.bat/.cmd/.sh` 四类入口。
- 脚本进程只继承 `PATH/SystemRoot/WINDIR/PATHEXT/TEMP/TMP/USERPROFILE/HOME`，不继承 `FISH_LLM_KEY`、`FISH_DB_DSN`、B站 cookie 等秘密。
- 脚本以**当前用户权限**运行，不是沙箱：只应安装可信技能。
- `allow_scripts=false` 时所有 `skill_run` 拒绝，但 `skill_list/skill_read` 仍可用。
- 技能名、描述、正文和脚本输出都会进入模型上下文，不要写凭据。

## 6. 已安装示例

- `white-rice`：白饭梗说明（纯说明，无脚本）
- `dice`：真实掷骰子脚本
- `foreverse-character-card-skills`：Foreverse 角色卡创作套件（四个子技能完整放在 `references/`，按需分页读）
- `csp`：Character Skill Producer（离线角色蒸馏器，含 references/scripts，不在直播中直接调用）

## 7. 限制（下一版再做）

- 没有 Web 面板管理（新增/编辑/删除技能仍用文件）。
- 没有按技能配置网络/文件权限（目前靠进程用户权限和目录约束）。
- 没有技能级 LLM 模型路由（技能说明由当前 ReAct 模型读取）。
- 没有嵌入/向量检索技能（技能数量大时再考虑）。
