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

## 四、下一步（交 Qoder/WorkBuddy）

```
1. 复核本鱼改的 4 处（发送失败 nil / 全量 ReAct / self_names / 人格解耦）
2. 修 A/B/C/D（观众实时上下文、错别字召回、ReAct 上下文）
3. E/F/G 按测试观感再调
```
