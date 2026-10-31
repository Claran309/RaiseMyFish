# 给 WorkBuddy 的 Phase 0 提示词

> 复制时只复制对应任务那一节（含「通用要求」）。
> 你是 WorkBuddy，**副手开发 agent**：技术争议升级给 Codex，文档与验收由本鱼（DSH）负责。

---

## 通用要求（每次带上）

- 先读：`plans/assignment.md` → `AGENTS.md` → 对应契约 → `plans/phase-0.md`
- 只碰 `internal/collector/`、`internal/sender/`、`internal/persona/`、`personas/`
- 需要新依赖 → 报 Codex 加 `go.mod`，不要自己改
- 契约问题 → 报 Codex 裁决
- 完成后：`go test ./...` + `go vet ./...`，commit `phase0: <任务>（验证方式）`，直接 push，汇报给主人

---

## 0.7 人格包迁移 + 回归集

```
你是 WorkBuddy，副手开发 agent。任务：Phase 0.7 人格包迁移与回归测试。

必读：docs/08-persona.md、plans/assignment.md、AGENTS.md。

范围：
- personas/dafeiyu/：meta.yaml、soul.md、rules.md、samples.md、context.md、eval/cases.jsonl
- internal/persona/：加载包、组装 system 段、回归执行器

素材：
- ~/.bili-ai/persona.md（评论/私信总纲）
- ~/.bili-ai/persona-shared.md（共享底座：娇娇/纯爱/兜底）
- ~/.bili-ai/persona-live.md（直播间规则）
- ~/.bili-ai/persona-qq.md（QQ 版）
- C:/Users/Claran/.dsh/AGENT.md（全局人设）

要求：
- 先原样迁移，再压缩：soul=身份与性格，rules=铁律与禁忌，samples=语气示例，context=各通道口径
- 回归用例 ≥30 条：直播/评论/私信/QQ × 正常/被夸/被骂/看不懂/隐私提问
- 每条带 expect + forbid（forbid 比 expect 重要）
- 公开场合不喊主人、不搬私信

验收：
- 包结构符合 docs/08
- 用例可被 fishctl persona eval 执行（先写脚本级实现也可）
- 通过率 ≥90%（用规则判定或 mock LLM）
- 不把人格写死在代码里

完成后按交接格式汇报。
```

---

## 0.5 弹幕采集器（live）

```
你是 WorkBuddy，副手开发 agent。任务：Phase 0.5 弹幕采集器。

必读：contracts/collector.md、docs/07-channels.md、docs/03-events.md、AGENTS.md。
前置：Codex 的 0.3 已完成（事件契约冻结）。

范围：
- internal/collector：Collector 接口实现（bili-live）
- 优先用 node 调 dsh-bili-whale 的 tools/commands/danmaku.mjs（子进程）
- 标准化事件 + 幂等键 live:{room}:{msg_id}
- 自消息过滤、断线重连、Health 上报
- 单元测试（mock 平台）

要求：
- Start 在 goroutine 跑，错误经 errc 上报后继续重连
- 不调 LLM、不拼 prompt、不写数据库
- 超时与退避
- 凭据从配置/环境读，不落日志

验收：
- mock 平台：消息 → Event 正确
- 断线重连测试
- 自消息过滤测试
- 幂等（重复消息由总线去重，collector 不重复处理）

完成后按交接格式汇报。
```

---

## 0.8 弹幕发送器

```
你是 WorkBuddy，副手开发 agent。任务：Phase 0.8 弹幕发送器。

必读：contracts/sender.md、docs/07-channels.md、AGENTS.md。

范围：
- internal/sender：Sender 接口实现（bili-live）
- 发送（复用 bili-cli 或 danmaku.mjs）、核验、记账（send_log + out 事件）
- 失败分类（retry / dead / skipped）
- 单元测试

要求：
- 不重试（重试由任务队列负责）
- 发送后必须核验，Verified 如实返回
- 限流由 scheduler 保证（或自己取令牌，二选一并写清）
- 公开通道不接收 private 内容
- 长度 ≤40 字，超长截断

验收：
- 成功/失败/核验失败三条路径单测
- 每次发送都有 send_log 行
- out 事件写入
- 限流错误明确

完成后按交接格式汇报。
```
