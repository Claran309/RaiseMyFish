# 稳定版测试发现清单（2026-11-05）

> 测试中发现的全部问题。**记录为准，修复交给 Qoder/WorkBuddy**（本鱼只记录与验收）。
> 测试环境：主人直播间 30308220（Claran309），fishd 全量 ReAct + 人格解耦版。

## 一、本鱼这次「自己动手」改了的东西（已生效，待 Qoder 复核）

| # | 改动 | 文件 | 说明 |
|---|---|---|---|
| 1 | **发送失败不崩循环** | `cmd/fishd/generate.go` | sender.Send 失败且已结清（settleFailure 成功）后返回 nil，不返回 error（否则 scheduler.complete 崩整个消费循环） |
| 2 | **全量 ReAct**（去短句单步） | `cmd/fishd/react.go` | `shouldUseReAct` default_react=true 时恒返回 true；删除 `minReactRunes` |
| 3 | **@自己修复** | `config/fishd.toml` | `self_names` 加「魔鱼魔鱼吃白饭饭饭饭」 |
| 4 | **人格与直播间解耦** | `personas/dafeiyu/context.md`、`soul.md`、`samples.md`、`rules.md` | 「娇娇的直播间」→「当前正在看的直播间（主播名由状态注入）」；示例泛化；娇娇保留为人设关系但注明「不是唯一/以 [当前状态] 为准」 |

> 测试同步更新：`cmd/fishd/serve_test.go`（send 失败不再透传、全量 ReAct 断言、短句进 ReAct）。

## 二、测试发现、尚未修的问题

| # | 问题 | 现象 | 建议 |
|---|---|---|---|
| A | **观众实时上下文** | 回复弹幕时，鱼的上下文里「该观众最近 5 条弹幕」来自档案快照（每小时重建），滞后 | Generate 注入时实时查 `EventRepo.ViewerRecent(uid,5)` 拼进 Viewer 片段 |
| B | **错别字/昵称模糊召回** | 观众名打错字（「X奇迹与你」vs「X奇迹与你X」）→ 档案按 from_id 精确查不到 | 昵称模糊匹配（子串/编辑距离/拼音）+ 语义混合兜底 |
| C | **ReAct 没有上下文** | ReAct 的 prompt 只有 System（人设）+ Question（弹幕），没有 Memory.Recent（鱼的发言）/Viewer/State | ReAct Input 增加上下文段（与单步 Brain 一致） |
| D | **鱼自己的发言/主动弹幕上下文** | 单步 Brain 有（Memory.Recent 含 out）；ReAct 看不到 | 同 C（ReAct 注入 Memory.Recent） |
| E | **回复质量偏怪** | 实测「在困惑在困惑」「这直播看着还挺热闹元」等 | 人格修改后观察；必要时加强 samples 质量约束 |
| F | **@ 限流观感** | 用户觉得「对所有人回复」——实际只回 @（非 @ 全 skipped），回复间隔 10s 冷却 | 若想更克制：cooldown 30-60s 或 rate 3/min |
| G | **主动弹幕与回复混杂** | 用户观感「混乱」——主动弹幕（30s+）看起来像对所有人说话 | 可调主动弹幕间隔或去重策略 |

## 三、已排除（不是 bug）

| 项 | 结论 |
|---|---|
| 「重回复」 | 用户连发两条相同弹幕（两条独立事件各自回复） |
| 对所有人回复 | reply_only_mention 正常，非 @ 均 skipped |
| 旧进程 @自己 | 21:02 的 @自己 是重启前旧进程发出的 |
| 私信 uncertain 崩溃 | 已修（发送失败结清后返回 nil） |


## 五、主人反馈的 8 个新问题（2026-11-05 测试后，待核实/待修）

| # | 问题 | 核实结果 | 优先级 |
|---|---|---|---|
| H1 | **漏弹幕**（非 WS 强连接？） | danmaku-await 用 **WSS**（强连接）——不是轮询；但 WS 断线重连窗口可能漏，需查断线日志 | 高 |
| H2 | **@ 格式错误**（纯文本 @ 别人看不到） | ✅ **确认**：发送是纯文本「@X奇迹与你X ...」，danmaku.mjs **无 reply_mid/at_uid 参数**——B站不显示 @ 提示 | 高 |
| H3 | **直播间注入耦合**（问在看哪→答别人房间） | brainState 有 presence 优先+watch 兜底；**presence 残留旧房间会串台**（进程内内存态，重启清空） | 高 |
| H4 | **记忆混乱**（问「弓长三益」说不知道） | ✅ **数据在**：events 12 条 + viewer_profiles 1 条——**召回失败**（工具/召回路径问题） | 高 |
| H5 | **错别字**（名字打错谐音→不知道） | ✅ 档案按 from_id 精确查；错别字/谐音不匹配 | 高 |
| H6 | **幻觉**（问「奇迹与你」→ 答紫晶？） | 实测回复是「本鱼啊，大肥鱼一条」（意图误判）——未查到「紫晶」；另 21:54:43 回复「不认识」但档案有 161 句 | 高 |
| H7 | **ReAct 上下文不完善** | 应注入：(a) 最近回复的人 (b) 最近给鱼发了什么+鱼回复了什么 (c) 直播间最近 N 条弹幕——当前 ReAct 只有 System+Question | 高 |
| H8 | **意图误判**（「X奇迹与你X 是谁」→ 回「本鱼啊」） | ✅ **确认**：「X奇迹与你X 是谁」被当成「你是谁」——应识别「XX 是谁」= 问那个用户 | 高 |

### 核实证据

- 弓长三益：events 12 条、viewer_profiles 1 条——数据在，**召回没到**。
- X奇迹与你X：档案 161 句；但 21:54:43 回复「不认识，本鱼没听过这号人」；21:56:15「X奇迹与你X 是谁」→ 21:56:17 回「本鱼啊，大肥鱼一条」（意图误判）。
- @ 发送：out 文本「@X奇迹与你X ...」纯文本；protocol.mjs 有 reply_mid 解析（收），但 danmaku.mjs 发送无 reply_mid 参数。


### H7 扩展：ReAct 上下文完整清单（设计）

> 核心：ReAct 的 prompt 应 = 单步 Brain 的完整上下文（当前只有 System+Question）。

| 组 | 项 | 来源 |
|---|---|---|
| **状态** | 当前在干什么（watching/replying/sending） | presence 状态表 |
| | 分身在干什么（多状态并存） | presence |
| | 当前时间 | 时钟 |
| | 直播间上下文（主播/房间/在播什么） | watch |
| **对话** | 最近回复的人（本条发送者） | events |
| | 该观众最近发言（5-10 条） | events/viewer |
| | 鱼最近回复（避免重复/接续） | events out |
| | 直播间最近 N 条弹幕（10-20） | events |
| | 跨通道最近 | memory.CrossChannel |
| **记忆** | 回复人档案（老熟人） | viewer_profiles |
| | 记忆召回（相关记忆） | memory.Records |
| | 知识召回（梗库 RAG） | knowledge |
| | 长期设定 | soul |
| **现场** | 直播画面描述 | vision |
| | 观众名单（谁在说话） | events |


### H7 扩展 2：主动弹幕的上下文（复用被动 + 最近主动弹幕）

> 现状：主动弹幕只注入 Visual + 预召回记忆——「傻傻的」；且不知道最近自己发过什么。

| 应注入 | 来源 | 说明 |
|---|---|---|
| **最近发过的主动弹幕**（5-10 条） | events out（proactive） | 避免重复/接话（现在只有 Levenshtein 去重，没有上下文） |
| **直播间最近聊什么**（最近 N 条弹幕） | events | 知道弹幕区在聊什么，主动弹幕能接话 |
| **当前状态** | presence | 在干什么 |
| **记忆/知识召回** | memory/knowledge | 与画面/话题相关的梗与记忆 |
| **观众名单** | events | 谁在（可以提） |

**核心**：主动弹幕 = 被动弹幕的完整上下文 + Visual（画面描述），而不是只有 Visual。

## 四、下一步（交 Qoder/WorkBuddy）

```
1. 复核本鱼改的 4 处（发送失败 nil / 全量 ReAct / self_names / 人格解耦）
2. 修 A/B/C/D（观众实时上下文、错别字召回、ReAct 上下文）
3. E/F/G 按测试观感再调
```
