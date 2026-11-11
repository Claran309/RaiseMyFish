# 修评论 + 评论/私信思考式 ReAct 派发单（2026-11-11）

> 主人优先级最高：**先修评论区**，且**评论/私信也要「思考式」ReAct**（跟弹幕一样，但可更长思考——评论/私信没有弹幕的严格延迟要求）。

## 批 1：修评论（#77）

### 1. 评论自动发现 412 风控
**现状**：`space_videos` / `space_dynamics` 返回 **HTTP 412**（B站风控）→ 自动发现失败 → 只轮询手动列表（manual_videos=0）→ 评论区看不到。

**排查方向**（按顺序）：
1. **WBI 签名**：space 接口需要 WBI（w_rid/wts）——检查 `space_videos` 的 WBI 是否生效（项目有 `signWBI`）
2. **频率**：space 接口风控（间隔拉长？）
3. **凭据**：cookie 是否有效（SESSDATA/buvid3）
4. **参数**：mid/order/pn 是否正确
5. **fallback**：自动发现失败时，**读手动视频列表**（`[channels.comment] videos`）——确保手动列表可配

**验收**：
- space_videos 不再 412（或明确 412 原因）
- 自动发现能拉到自己的视频
- 评论采集能拉到评论（真实视频）

### 2. 评论通道完整测试
- 采集：视频评论 + 楼中楼
- 回复：对评论回复（带 @）
- 账号隔离（评论用自己账号）

## 批 2：评论/私信「思考式」ReAct（#78 扩展）——**已由 Qoder 开工（不用派）**

### 目标
评论/私信 ReAct **跟弹幕一样有思考阶段**，但**允许更长思考/更多工具步**（没有弹幕的严格延迟）。

| 通道 | 思考预算 | 延迟要求 |
|---|---|---|
| **live 弹幕** | 3 步 / 8 秒 | 严格（直播节奏） |
| **comment 评论** | **5-10 步 / 30-60 秒** | 宽松（用户等回复正常） |
| **dm 私信** | **5-10 步 / 30-60 秒** | 宽松 |

### 实现
1. **思考式 ReAct**（#69 BCD 的思考阶段）：
   - 先输出「思考」（thought），再决定工具/回复
   - 评论/私信可 **多轮思考**（think → tool → think → answer）
2. **预算按通道区分**：
   - live：8 秒（现状）
   - comment/dm：**30-60 秒**（可配）
3. **max_steps 按通道**：
   - live：3（现状）
   - comment/dm：**5-10**（可配）

### 验收
- 评论/私信 ReAct 有思考阶段（degrade=parse 减少）
- 评论/私信延迟放宽（不因 8 秒预算失败）
- 弹幕不受影响（仍 8 秒）

## 批 2.5：账号消息监听（关键——替换视频轮询）——**已交付待验收（Qoder）**

### 问题
现在评论采集是**轮询自己视频的评论**——只能看到「自己视频/动态」的评论。
**别人的视频下 @ 鱼 / 回复鱼**——`x/msgfeed` 消息里有，但**轮询视频看不到**。

### 目标
**监听账号消息**（B站「消息」）：
- **@我的**（`x/msgfeed/at`）——任何视频下 @ 鱼（含别人视频）
- **回复我的**（`x/msgfeed/reply`）——评论/动态下回复鱼
- **评论我的**（`x/msgfeed/comment`）——鱼视频/动态下的新评论

**然后在消息里回复**——这样**别人视频下的 @ 也能回**。

### 参考
`bili-cli` 已有：`bili feed mentions`（@我的）/ `bili feed replies`（回复我的）/ `bili feed inbox`（统一收件箱）——**看它的 API 实现**（`internal/bili` 可移植）。

### 实现
1. **消息采集器**：定时拉 `x/msgfeed/at` + `reply`（或 `feed inbox`）
2. **事件化**：每条消息 → 评论事件（带目标视频/动态 oid + 回复锚点）
3. **回复**：在**消息对应的评论区**回复（B站 API `x/v2/reply/add`——oid + root/parent）
4. **账号隔离**：消息用自己账号（同现有 dm）

**与视频轮询的关系（主人定）**：
- **消息监听替代视频轮询**（默认主用——覆盖别人视频下的 @）
- **视频轮询功能与代码保留**（不删，可配置切换/备用——`[collector.comment] feed_enabled=false`）

### 验收
- 别人视频下 @ 鱼 → 消息采到 → 鱼回复（在别人视频评论区）
- 自己视频/动态的新评论 → 消息采到 → 回复
- **视频轮询代码保留**（可切换启用，默认关）

## 批 3：评论多模态（评论图片）

### 目标
评论如果带图片 → **拉下来 → vision 读** → 注入 ReAct 上下文。

### 实现
1. **采集**：评论事件带图片 URL（B站评论图片）
2. **拉图**：下载图片到本地（参考 watch 的帧/vision 流程）
3. **识图**：vision 模型读图（描述/OCR）→ 注入上下文
4. **降级**：图拉不下来 → 忽略图（不阻塞评论回复）

### 验收
- 带图评论 → 图片描述进上下文 → 回复引用图片内容
- 无图评论不受影响
- 图失败降级（不阻塞）

## 执行规则

- **先批 1（修评论）**——评论区必须能用
- 再批 2（思考式）——评论/私信体验提升
- 不执行 git；不碰弹幕的 8 秒预算
- 交付格式照旧

## 交付状态（2026-11-11，Qoder）

- **批 2 已交付待验收**
  - 按通道 ReAct 预算：`[reasoning.comment]` / `[reasoning.dm]` 默认 5 步 / 45 秒（可配 5-10 步 / 30-60 秒）；live 仍 3 步 / 8 秒，未改。
  - 评论/私信恒走 ReAct（不受 `default_react` 影响）；ReAct 提示词按通道标注「评论/私信/直播弹幕」。
  - 评论发送：`[sender.comment] enabled=true`；root/parent 表达回复线程（平台显示「回复 @xxx」），账号快照 + durable 准入已有，本批补账号快照回归测试。
  - `[scheduler] worker_timeout_ms=120000`，为长 ReAct 之后留一次单步回落；弹幕 ReAct 自身仍 8 秒。
- **批 3 已交付待验收**
  - 采集：`content.pics[].img_src` → `Meta.pics`（https、长度、去重、封顶 4）；纯图评论正文占位 `[图片]`。
  - 识别：`[comment_vision]`，B站图床 https 白名单（重定向复检）、单图 4MB、整条评论共用一个超时；逐图描述/OCR，单图失败只跳过。
  - 读帧支持 `.webp`（评论图床常见），data URI 标 `image/webp`，已补回归测试。
  - 注入：`ContextInput.Visual`，单步 Brain 与 ReAct 共用；全部失败等于无图，不阻塞评论回复。
- **批 2.5 已交付待验收**
  - 来源切换：`[collector.comment] feed_enabled=true`（默认）监听账号消息；`feed_enabled=false` 切回原视频轮询。视频轮询代码与自动发现完整保留。
  - 消息监听：`x/msgfeed/reply` + `x/msgfeed/at`，默认 60 秒（10–3600 可配）；回复我的与 @我的合并去重；@我的只取评论里的 @（`item.type=reply`），动态正文 @ 跳过。B站没有 `x/msgfeed/comment` 端点（参考 bili-cli 与平台接口清单，只有 reply/at/like/unread）；「评论我的」并入 `reply` 收件箱。
  - 事件化：`comment:{oid}:{rpid}`，Meta 带 `source=bili-msgfeed`、`feed=reply|at`、`comment_type`、`root`、`parent`；发送器沿用 root/parent 在消息对应评论区回复。
  - 账号隔离：整轮冻结账号快照并持有账号切换准入（Batch.Close 释放）；水位按 selfMID 分账号保存到 `feed_state_path`（默认 `state/msgfeed-cursors.json`）。
  - 首次接入：优先平台 `last_view_at` 建立基线，无则取当前最新一条，不补答历史；水位只在事件全部落库确认后提交。
  - 面板：`/api/message` 增加 `feed_enabled` / `feed_interval_seconds`，`/message` 页显示消息监听模式（视频列表只在轮询模式可编辑）。
  - 暂不支持图文评论区（business_id=11）：发送器仅支持 1/12/17，消息监听跳过并记调试日志。
- **未做**：批 1（412）此前已修。
- **门禁**：`go build ./...`、`go vet ./...`、受影响包 `go test -race` 全绿；全量测试仍有既有红（本批未触碰）：`internal/memory`、`internal/memory/extractworker`（f39499d 已下线规则版 fact，旧测试未同步）、`internal/collector/dm`（工作区 bili UA 改动相关）。

