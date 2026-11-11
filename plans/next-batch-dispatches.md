# 下一批派发单（2026-11-11 下午）

> fishd 保持运行（本批不重启/不停——纯代码开发，交付后由 DSH 验证再重启）。
> 按待办优先级：消息完整性 > 回复质量 > 互动方式。

## 批 A：#83 消息游标拉（防顶号丢消息）

**问题**：主人顶号（登鱼账号）会把消息标已读 → msgfeed 的「未读」依赖 → 丢消息。

**改法**：
1. `internal/collector/msgfeed`：**按 last_view_at 游标拉**（游标之后全部，含已读）——不依赖未读状态
2. 读取 `x/msgfeed` 的 time 字段做游标（参考现有 `feed_state_path` 水位）
3. 游标推进（处理后）

**验收**：已读消息也能被拉取（模拟：标记已读后仍采到）；顶号场景不丢消息。

## 批 B：#73 ReAct degraded=parse（回复质量）

**问题**：ReAct 执行完成 `degraded=true, degrade_reason=parse`——模型输出没按 JSON 格式（action=final 解析失败）→ 降级单步，回复质量差。

**改法**：
1. **prompt 强化格式要求**（必须输出合法 JSON，给出正反例）
2. **解析容错**：宽松解析（提取 JSON 块/修复常见格式）
3. **降级兜底**：解析失败时用「正常文本」而非空/降级

**验收**：degrade_reason=parse 显著减少（日志）；降级时回复仍是正常文本。

## 批 C：#80 弹幕筛选路由（互动核心）

**问题**：`reply_only_mention`（只回 @）二选一太硬——要**小模型筛选器**。

**改法**：
1. **所有弹幕进筛选器**（小模型/规则）判断：
   - 是否发给鱼（@ 鱼/叫鱼名字）
   - 是否需要鱼回复
   - 是否跟鱼相关（话题/上下文）
   - 是否跟鱼之前说的上下文相关
2. **直接 @ → true**（免筛选）
3. **true 才回**；**@ 的带 @ 人，否则小模型判断 @ 谁或不 @**
4. **配置**：`[channels.live] routing_mode = "filter"`（vs 现有 mention_only/all）

**验收**：非 @ 但相关弹幕能回；纯闲聊不回；@ 的带 @。

## 批 D：#79 发弹幕改为 tool

**问题**：回复只能发一条弹幕；要**把「发弹幕」变成 tool**——单轮思考可多次发。

**改法**：
1. ReAct 工具加 `send_danmaku`（发弹幕）
2. **单轮可多次调用**（多段弹幕）
3. 与原回复通道协调（避免重复/超频）

**验收**：一次思考能发 2+ 条弹幕（如先吐槽再补一句）。

## 批 E：#84 动态评论轮询（动态评论漏采）

**问题**：msgfeed（reply/at）不含「评论我的动态」；评论轮询只轮视频 → **动态评论漏采**（实测：主人 13:43 动态评论没采到）。

**改法**：
1. **动态评论轮询**：定时拉自己的动态列表（`bili DynamicList`）→ 每个动态的评论区（oid/type=draw/dynamic）→ 采新评论
2. **事件化**：评论事件（带 oid/type/回复锚点）→ 评论通道
3. **回复**：`x/v2/reply/add`（动态评论区）
4. **配置**：`[collector.comment] dynamic_poll_enabled = true`（可关）
5. **去重**：与 msgfeed/视频轮询去重（rpid）

**验收**：主人动态的新评论（如 13:43 那条）能被采到并回复。

## 批 F：#85 动态评论轮询没出事件（紧急）

**现状**（DSH 已验证）：
- ✅ `space_dynamics` 412 已修（referer 去掉——发现 12 个动态）
- ✅ `comment_dynamic_setup` 装配（Group(feed, dynamics)）
- ✅ 视频评论采集正常（auto_discover）
- ❌ **动态评论事件 = 0**（14:57 主人发的「动态评论回复 test 柿崎」没采到）

**排查线索**：
1. `Group.run`（group.go:96）启动 children——确认 dynamics 采集器真的 run 了
2. `DynamicDiscoverer.Discover` 返回 12 个动态——但 `Collector.Fetch` 对每个动态拉评论是否执行/失败（**需要日志**——当前静默）
3. `RecentSince`（24h）——评论时间过滤
4. 动态评论的 oid/type（draw 的 415114992）——项目拉评论的 API 是否正确（对比 `bili comment list 415114992 --type draw` 能拉 10 条）
5. 最近日志有 `collector_error`（采集器暂时异常）——确认是哪个源报的

**验收**：主人 14:57 的动态评论能被采到（events 出现 comment in）+ 能回复。

## 执行规则

- **fishd 不重启**（本批纯代码，交付后 DSH 验证）
- 不执行 git；不改 contracts（除必要）
- 交付格式照旧（每批分开）

## 交付状态（2026-11-11）

- 批 A：已交付（msgfeed per-feed 基线 + 已处理游标拉取含已读；last_view_at 只观测不推进）。
- 批 B：已交付（prompt 正反例 + 宽松解析/修复 + 自然语言直采 + 降级改要纯文本）。
- 批 C：已交付（`routing_mode = filter` 规则筛选；普通 @ 也回带；非 live 不受影响）。
- 批 D：已交付（`send_danmaku` 工具：live、单轮≤3 条、每条≤40 字、去重/限频；发过弹幕后跳过普通回复）。
- 批 E：已交付（`dynamic_poll_enabled` 默认开：feed 模式下 msgfeed + 动态评论轮询并行，组合器把落库回执转给产出子采集器；动态首轮只补最近 24h；事件沿用 `comment:{oid}:{rpid}`、type=17，sender 走 `x/v2/reply/add`；面板同步模式文案）。
- 门禁：`go build ./...`、`go test ./...` 全绿；受影响包 `-race` 全绿；`go vet` 仅既有 `internal/memory/fact_test.go:47 unreachable code`。

