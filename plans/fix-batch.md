# 稳定版测试修复派发单（2026-11-05）

> 来源：`plans/test-findings.md`（测试发现 4 已改 + A-G + H1-H8 + ReAct/主动弹幕上下文设计）。
> 分 4 批按优先级；每批交付后本鱼验收。

## 批 1：链路正确性（最高优先级）

| # | 问题 | 修法 | 文件 |
|---|---|---|---|
| H2 | **@ 格式错误**（纯文本 @ 别人看不到） | 发送弹幕时带结构化 @：danmaku.mjs 增加 `reply_mid`（或 `at_uid`）参数；发送器从事件 Meta 取 `reply_to_id` 传入 | `tools/bili-whale/danmaku.mjs` + `internal/sender/live` |
| H4 | **记忆召回失败**（弓长三益数据在但说不知道） | 排查 memory_search/viewer 召回路径：viewer 档案按 from_id 查（弓长三益有档案）；ReAct 调 memory_search 时 query 应含「弓长三益」；补测试（真实数据回顾） | `internal/reasoning/tools.go` + `cmd/fishd/generate.go` |
| H8 | **意图误判**（「X奇迹与你X 是谁」→ 回「本鱼啊」） | prompt 里明确：「XX 是谁」= 问那个用户（不是问自己）；加「问题主体识别」规则到 persona 或 ReAct prompt | `personas/dafeiyu/rules.md` + ReAct prompt |
| H3 | **注入耦合**（问在看哪→答别人房间） | presence 的 watching 在采集启动时重置（清旧房间）；brainState 只用当前采集房间 + watch 配置对齐；补测试 | `cmd/fishd/presence.go` + `recall.go` |

## 批 2：上下文注入（核心体验）

| # | 问题 | 修法 | 文件 |
|---|---|---|---|
| H7 | **ReAct 上下文缺失** | ReAct 的 Input 从 `{System, Question}` 扩为 `{System, Context, Question}`；Context = 单步 Brain 的完整上下文（状态/对话/记忆/知识/现场 15 项，见 test-findings.md） | `internal/reasoning/agent.go` + `cmd/fishd/react.go` + `generate.go` |
| 主动弹幕 | **上下文傻** | 主动弹幕 = 被动完整上下文 + Visual；加「最近主动弹幕（5-10 条）」防重复 | `internal/proactive/proactive.go` |
| H5 | **错别字召回** | 昵称模糊匹配（子串/编辑距离/拼音）+ 语义混合兜底；查询「弓长三益」→ 档案「弓长三益」精确命中 | `internal/viewer/search.go` + `internal/memory/search.go` |
| A | **观众实时上下文** | Generate 注入时实时查 `ViewerRecent(uid,5)`（档案快照滞后 1h） | `cmd/fishd/generate.go` |

## 批 3：稳定性与质量

| # | 问题 | 修法 | 文件 |
|---|---|---|---|
| H1 | **漏弹幕** | 查 WS 断线重连窗口（danmaku-await 心跳）；断线后用 gethistory 补漏（补拉最近 N 条） | `tools/bili-whale/danmaku-await.mjs` |
| H6 | **幻觉** | ReAct 上下文注入后观察；必要时加「不确定就说不知道」规则 | persona + prompt |
| E | **回复质量怪** | 人格修改后观察；必要时加强 samples 质量约束 | `personas/dafeiyu/samples.md` |
| B | **ReAct 无上下文**（与 H7 合并） | 同 H7 | — |

## 批 4：调优

| # | 问题 | 修法 |
|---|---|---|
| F | **@ 回复不限流**（主人定） | 去掉/放宽限流：cooldown 与 rate 不构成限制（当前 10s/6 分钟也不拦）；保持平台被动保护（限流错误退避） |
| G | 主动弹幕与回复混杂 | 调主动弹幕间隔/去重策略 |
| C/D | ReAct 上下文（与 H7 合并） | 同 H7 |

## 禁止

- 不改 contracts、不碰调度/发送语义（除 H2 发送参数扩展）、不执行 git
- 每批交付格式照旧（文件/测试/配置/未做项/是否暂停）

## 验收

- 本鱼逐批验收（跑测试 + 浏览器截图 + 真实弹幕测试）
- 全部通过后 → 稳定版测试（2h 合并）→ 打包
