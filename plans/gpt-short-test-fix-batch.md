# 短测试/侧写复核修复派发单（2026-11-10）

> 来源：`docs/reviews/2026-11-10-short-test-and-viewer-summary-review.md`（190 行，R01-R09）。
> GPT 结论：**可以在监护下继续使用直播回复；不建议现在批准三通道+侧写全部无人值守**。
> 优先级：R01/R02/R03 必修（P1）；R04-R09 改进（P2，按测试范围）。

## 批 1：三个 P1 修复（先做）

### R01 · P1 · 文本 @ 剥离时机在持久化之后
**位置**：`internal/sender/live/live.go:91`（Send/Resume）、`internal/database/send.go:156`（计划保存）、`send.go:292`（out 审计）
**问题**：先 Prepare 保存「@昵称 正文」及 SHA，再在内存 Attempt 里剥掉文本 @，实际 POST/Verify 收到「正文」；结算重新读持久计划，out/记忆仍保存原带 @ 文本，防重指纹也按原文本 → **两套表示**（不可变计划/实际正文/审计/防重不一致）。
**改法**：先生成**规范 wire text + 结构化 target**，再原子持久化；Open/Verify/out/SHA 使用同一快照；展示 @ 可单独作为字段；旧已 arm 尝试按创建版本处理，不改写。
**验收**：Prepare/Open/Verify/out 全文一致；target 持久化；旧 unknown/已发送升级。

### R02 · P1 · 来源删除/改密后三份侧写仍可公开使用
**位置**：`internal/database/viewers.go:312`（档案重建）、`:194`（SaveProfileSummary）、`internal/viewer/summary.go:195`
**问题**：档案重建清原文/向量但**保留 profile_summary**；稳定 ID 的 viewer_profile 记忆和 viewers.md 没有来源撤回传播；LLM 计算期间源变更无提交时检查。
**改法**：来源 ID + 版本/hash + 生成器版本；撤回/改密时先使派生结果不可见，再异步清理三份投影和向量；保存时 CAS 验证源未变。
**验收**：公开→改密/删除/清洗、计算中撤回、档案不再达标 → 档案/记忆/RAG/外发 embedding 都没有旧资料。

### R03 · P1/P2 · 降级与知识写失败被当成最终完成
**位置**：`internal/viewer/summary.go:183`（summarizeOne）、`internal/database/viewers.go:160`（ActiveViewers）
**问题**：先保存档案/记忆并推进 summary_updated_at，再写 viewers.md；后者失败无新发言不再重试；模型 timeout/坏 JSON 降统计也推进相同完成时间，**好侧写被覆盖**；模型恢复后无新发言不重试。
**改法**：分开 source_version、last_success、fallback/failed、next_retry 与投影 dirty 状态；DB 事务记录待发布任务（知识失败只补投影不重付费）；已有好版本在临时故障时保留，首次无摘要才降级；有上限退避。
**验收**：文件故障恢复/模型恢复/进程重启/无新发言补偿/旧好版本保留；模型调用次数有界。

## 批 2：改进项（P2，按测试范围）

| ID | 问题 | 改法 |
|---|---|---|
| **R04** | 甲问乙是谁时 viewer_search 丢弃乙的 profile_summary（只返回昵称/次数/原话） | 识别确定 UID 后补取 `GetProfile`，返回自动侧写 + summary_updated_at + 依据；甲/乙分别标识；歧义不强认 |
| **R05** | 推断写成观众本人断言（AssertedByID=观众、置信度 0.6） | subject=观众，**derived_by=模型/规则**，asserted_by 只用于明确自述；每字段保留证据与类型（自述/观察/推断）；表达「近期观察，仅参考」 |
| **R06** | 最近 20 条每轮重写全部，短时情绪覆盖长期偏好 | 分两层：稳定明确自述/已确认偏好 + 有期限近期话题/互动观察；按新证据修订而非重写；只在新消息量/话题变化/过期时触发 |
| **R07** | startViewerSummary 只看 interval，不检查 Memory.Enabled/Viewer.Enabled/冻结 | 遵循 memory/viewer 停用契约（或独立 enabled/frozen 并说明）；interval=0 停调度但不清理已有摘要 |
| **R08** | 只有日志，无专门 API/页面 | 面板显示：运行/最后成功/失败/fallback/来源数量/pending 知识发布 + 暂停/重建/撤回入口；httptest + 页面断言 |
| **R09** | ActiveViewers 是「近期活跃**或** hits 门槛」，无专门日预算/首次回填开关 | 先少量熟人试跑、分批回填；按新发言量/有效变化决定更新；后台优先级低于被点名回复 |

## 执行规则

- 批 1（R01-R03）先修：GPT 明确「投入长期使用前优先修三处」
- 每项独立验收（探针复跑：R02 来源撤回/R01 快照一致/R03 恢复补偿）
- 不执行 git；不改 contracts（除必要）
- 侧写 worker 在 R02/R03 修完前：**限定小批试跑**（不长期自动运行）
