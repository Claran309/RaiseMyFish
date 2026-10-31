# 开发提示词库（给主人的复制粘贴用）

> **使用前必读**：`plans/coordination.md` 是本轮协作裁决。DSH 独立验收与会话辅助，WorkBuddy 仅接轻量子任务；旧模板中的整模块派发不得直接照抄。

> 本目录是**主人给开发 agent 下任务的提示词**。按角色分文件，按任务分节。
> 用法：把本轮协作约束、通用要求与获派子任务一起带入对应会话；完成后将报告交 DSH 独立验收。不要只复制旧任务正文。
> 前提：agent 能访问项目目录 `D:/CodeStudy/RaiseMyFish`。

## 文件

| 文件 | 给谁 | 内容 |
|---|---|---|
| `codex-phase0.md` | Codex（首席开发与技术裁决） | 核心逻辑、公共接口、统一装配和集成 |
| `workbuddy-phase0.md` | WorkBuddy（轻量开发） | 素材、简单加载、通道薄适配、规则函数、日报展示、脚本子任务 |
| `dsh-flash-phase0.md` | DSH（独立验收与会话辅助） | 派发、验收、交接、文档与状态维护 |

## 使用节奏

1. **先发 Codex 0.1**，等它完成
2. DSH 按 coordination 看板派发：WorkBuddy 可先做 0.7a 纯素材；其他子任务等依赖和接口确认。完整人格回归在 0.6 后执行。
3. 每个 agent 完成后，把它给的汇报贴给本鱼 → 本鱼跑测试验收 + 更新 `plans/phase-0.md`
4. 有争议 → 主人转给 Codex 裁决，本鱼执行文档修改

## 通用尾部（每条提示词都带上）

```
完成后按 plans/assignment.md 的「交接格式」：
1) 跑 go test ./... 和 go vet ./...（或 golangci-lint）
2) 暂停获派文件的写入，在自己的 plans/reports/<agent>.md 交接；DSH 独立验收，Codex 统一集成提交/push
3) 给主人一段汇报：改了什么、怎么验证、风险/未决项
```
