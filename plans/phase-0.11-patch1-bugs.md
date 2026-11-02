# 0.11 补丁批次 1 · 修 bug（统计口径 + 发送核验）

> 背景：冒烟发现两个 bug；先修这两个，再补记忆/知识接线。
> 本批只修 bug，不接新功能；DSH 验收后再发批次 2。

## 任务 1：统计口径 bug（成功率 102.1%）

### 现象

`fishctl status` 日报：收到 159 / 回复 144 / 跳过 18 / 失败 6 → 成功率 **102.1%**（回复 > 可回复）。

### 根因（DSH 初步判断）

`internal/database/stats.go` 的 `Replied` 统计口径：send_log 的 ok 去重事件数未限制在「窗口内 in 事件」里，或把 out 事件也算进去了。

### 修复要求

1. `Replied` 只计「窗口内 `dir='in'` 事件」的 send_log ok 去重数（`event_id IN (SELECT id FROM events WHERE dir='in' AND at BETWEEN ...)`）
2. `observe.Render` 增加防御：`Replied` 超过 `Received - Skipped` 时按 `Received - Skipped` clamp，成功率不超过 100%
3. 测试：造 159 收/18 跳/144 回的矛盾数据 → 断言成功率 ≤100%；正常数据比例正确

## 任务 2：发送核验 unknown_manual 排查

### 现象

3 条 send_log `error=unknown_manual`（POST 后无法确认是否发出）。

### 要求

1. 排查 `internal/sender/live` 的核验流程：POST 后 gethistory 核验窗口/重试次数是否够
2. 找出 unknown 的真实原因（超时/回执缺失/非稳定 ID）
3. 修复：延长核验窗口或增加核验重试（不重复 POST）；无法确认时保留 unknown 并记录原因分类
4. 测试：mock 超时/回执缺失场景，断言不重复 POST、unknown 分类正确

## 禁止

- 不改 contracts、不接记忆/知识（批次 2 做）、不碰人格
- 不调真实模型/B站（用 mock）

## 交付格式

- 文件列表、测试结果、根因说明、未做项、是否暂停写入
