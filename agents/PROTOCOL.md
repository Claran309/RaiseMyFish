# Agent 协作协议（免复制粘贴）

> 三个 agent 没有实时消息通道，所以用**项目内共享文件**当留言板。
> 主人不需要在三个窗口之间复制粘贴，只需要各自说一句「按 agents/PROTOCOL.md 继续」。

## 一、文件结构

```
agents/
├─ PROTOCOL.md    本协议
├─ status.md      全局任务状态（本鱼维护，所有人只读）
├─ codex.md       Codex 的 inbox / outbox
├─ workbuddy.md   WorkBuddy 的 inbox / outbox
└─ dsh.md         本鱼（DSH）的 inbox / outbox
```

## 二、开工前（每个 agent 必做）

1. 读 `agents/status.md` —— 当前哪些任务已完成、哪些可开工
2. 读自己的 `agents/<名字>.md` 的 **INBOX** —— 有没有别人留给你的交接
3. 读 `plans/assignment.md` + 当前任务对应契约

## 三、完工后（每个 agent 必做）

在**自己的**文件里追加一条 OUTBOX：

```markdown
## OUTBOX · 2026-10-31 15:30
- to: workbuddy
- task: 0.3 事件总线
- status: done
- verify: go test ./... 全绿（12 用例）
- note: 事件契约已冻结，你可以开始 0.5/0.7/0.10
```

规则：

- 只写**自己的**文件，不碰别人的（避免冲突）
- 一条任务一条 OUTBOX，字段固定：`to / task / status / verify / note`
- status 取值：`done`（完成待验收）/ `blocked`（卡住，写原因）/ `question`（需要 Codex 裁决）

## 四、本鱼（DSH）的职责

- 主人每次来消息时，本鱼检查所有 OUTBOX：
  - 跑验收（测试/约定/越界）
  - 更新 `agents/status.md`
  - 在对应 agent 的 INBOX 里写验收结果
- 本鱼不替 Codex 做技术裁决，只记录与执行

## 五、Codex 的职责

- 最高裁决：看 INBOX 里 `question` 条目，给结论，写回对应 agent 的 INBOX
- 自己的 OUTBOX 写「给 WorkBuddy 的交接」，例如「0.3 完成，契约冻结，0.5/0.7 可以开始」

## 六、主人在三个窗口里说什么

- Codex：「按 agents/PROTOCOL.md 继续，看 INBOX」
- WorkBuddy：「按 agents/PROTOCOL.md 继续，看 INBOX」
- DSH（本鱼）：「检查 agents 里的 OUTBOX，更新状态」

## 七、边界（诚实说明）

- 这不是实时通信：agent 只在自己被唤起时读写文件
- 三个环境不共享进程与网络，文件是唯一可靠通道
- 项目目录在本机同一路径（`D:/CodeStudy/RaiseMyFish`），三方都能访问
- 如果某个 agent 无法读写该目录，就退回主人转达，但正常情况下不需要
