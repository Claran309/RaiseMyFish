# 开发提示词库（给主人的复制粘贴用）

> **使用前必读**：`plans/coordination.md` 是本轮协作裁决。DSH 独立验收与会话辅助，WorkBuddy 仅接轻量子任务；旧模板中的整模块派发不得直接照抄。

> 本目录是**主人给开发 agent 下任务的提示词**。按角色分文件，按任务分节。
> 用法：把对应任务那一节整段复制给对应 agent；完成后把它的汇报贴回给本鱼验收。
> 前提：agent 能访问项目目录 `D:/CodeStudy/RaiseMyFish`。

## 文件

| 文件 | 给谁 | 内容 |
|---|---|---|
| `codex-phase0.md` | Codex（首席，最终裁决） | 0.1 / 0.2 / 0.3 / 0.4 / 0.6（只做架构重活） |
| `workbuddy-phase0.md` | WorkBuddy（副手） | 0.7 / 0.5 / 0.8 / 0.9 / 0.10（耗 token 的实现类） |
| `dsh-flash-phase0.md` | DSH + flash（零散辅助） | 补测试 / 同步文档 / 小修 / 脚本 |

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
