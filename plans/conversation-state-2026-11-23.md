# 会话状态升级执行记录（C 系列）

> 来源：`D:/CodeStudy/growing/项目/会话状态升级清单-20261123.md`
> 设计说明：[docs/conversation-state.md](../docs/conversation-state.md)
> 原则：私信按 peer、评论按 thread root、直播保持无状态；默认全关，按通道灰度。

## 状态总表

| 项 | 内容 | 状态 |
|---|---|---|
| C0 | 会话键 + feature flag | ✅ `internal/conversation/key.go`；`[conversation]` 总开关/通道开关 |
| C1 | 私信会话状态 | ✅ `events.conversation_id` + `conversation_state`；Recent 按会话读最近 N 轮 |
| C2 | 评论 thread 状态与灰度 | ✅ 键带 `oid+root`；`comment_rollout_percent` 按会话键确定性分桶 |
| C3 | 直播 30 秒短窗口 | ✅ `internal/conversation/window.go`；纯内存、不落库、默认关 |
| C4 | 上下文装配 | ✅ `<conversation>` 段（摘要 + 最近 N 轮），统一转义与预算裁剪 |
| C5 | 会话摘要 worker | ✅ `conversation_summary_jobs` + 租约/重试 + 幂等写回；失败保留旧摘要 |
| C6 | 并发与会话锁 | ✅ 调度器按 `conversation_id` 串行，空键回退旧 `channel:session` |
| C7 | 隐私与隔离 | ✅ DM 只在 private Scope；摘要只进 `conversation_state`，不投影公开知识库 |
| C8 | 可观测与 30 条评测 | ✅ `/api/conversations` + 面板区块；`cmd/eval` 会话评测 30 条 |

## 验收证据

### C5 摘要 worker

- `go test ./internal/conversationsummary`：正常写回、失败保留旧摘要、过期跳过、失租不算失败、
  无模型抽取式兜底、摘要裁剪。
- `go test -tags=integration ./internal/database -run TestConversationSummary`：
  轮数/空闲触发、幂等入队、租约栅栏、失败回队与超过上限置 failed。

### C8 观测与评测

- `GET /api/conversations?hours=24`：`hit_rate / turns_avg / summary_age_seconds /
  prompt_token_delta_pct / p95_latency_delta_ms / repetition_rate`；
  无对照样本时增量为 `null`，不是 `0`。
- 面板「会话状态」区块：命中率、摘要覆盖率、token/延迟对照、重复回复率。
- 30 条会话用例（`personas/dafeiyu/eval/conversation-cases.jsonl`）离线结果：
  追问解决率 **100%**（基线 10%）、串人/键冲突 **0**、重复注入 **0**、
  prompt token 变化 **+0.4%**。
  延迟与单条成本无法离线测得，报告里保持 `null`，需真实模型评测补。

### 灰度与回滚

- 第 1 周：`dm_enabled=true`，`dm_rollout_percent=10`；
- 第 2 周：私信全量，`comment_enabled=true`、`comment_rollout_percent=10`；
- 第 3 周：评论 50% → 按 C8 数据决定是否全量；
- 直播短窗口最后做，默认 `live_window_seconds=0`；
- 异常时关 `enabled` 即回退旧逻辑，不需要回滚数据。

## 待定 / 后续

- 真实模型下的追问解决率、P95 延迟与单条成本增量：需要主人在有密钥时授权一次小流量评测。
- 历史事件 `conversation_id` 回填：当前只对新事件生效，按需再补。
