# P0 收尾待办清单（2026-11-02 更新）

> 用于跟踪 P0 收尾阶段的所有未完成项。完成一项划掉/标注。

## 代码待办

| # | 项 | 归属 | 状态 |
|---|---|---|---|
| 1 | ~~ReAct 思考链~~ | Qoder/WorkBuddy | ✅ 验收通过（b4b8795） |
| 2 | ~~ReAct token 记账~~ | Qoder/WorkBuddy | ✅ 验收通过（ce6d5d2） |
| 3 | **主动弹幕重构**（`plans/phase-0.19-proactive-refactor.md`：复用 Brain + 注入 Visual） | Qoder/WorkBuddy | ⏳ 派发单就绪 |
| 4 | ~~Resume budget 降级~~（伪待办：Resume 不调 brain，只有 sender.Resume） | — | ✅ 已核实不需要 |
| 5 | **梗库扩充**（500 → 7000 条：从 DSH `~/.dsh/meme-wiki/memes.db` 转换导入 K0 格式） | Qoder/WorkBuddy | ⏳ 新 |
| 6 | **梗白学习**（meme-learn：从弹幕学习新梗入库） | P2 | ⏳ 新 |
| 7 | **ReAct 工具扩充**（gift/follow/verify 等，现在只有 4 个：记忆/知识/状态/历史） | P6 | ⏳ 新 |
| 8 | **常驻 Edge**（0.13c：截图/点赞） | Qoder/WorkBuddy | ⏳ 可选 |
| 9 | **观众记忆**（PerViewer，`plans/phase-1-viewer-memory.md`） | P1 | ⏳ 待排期 |
| 10 | **fact 采集**（P1） | P1 | ⏳ 待排期 |

## 验收/测试

| # | 项 | 状态 |
|---|---|---|
| 11 | **2 小时正式验收**（P0 退出标准） | ⏳ 待开播 |
| 12 | ~~拉拉梗真实验证~~ | ✅ 完成（22:50:07「@Claran309 拉拉？！被发现了」） |
| 13 | 知识库真实命中验证（budget 降级后的梗召回） | ⏳ 等触发 |
| 14 | 主动弹幕内容质量（重构后） | ⏳ 等重构 |

## 运维

| # | 项 | 状态 |
|---|---|---|
| 15 | **本地提交待 Codex push** | ⏳ 等 Codex |
| 16 | 计划文档更新 | ✅ 本文 + 架构/记忆计划 |

## 已完成（2026-11-02）

@过滤、@结构化匹配、@回复带@对方（昵称修复）、梗词表、租约失效、budget 降级、知识库启用、鲸/颜文字/emoji、拉拉梗触发、主动弹幕 30s、人格收敛、错误链可见化、presence 状态注入、记忆提炼接线、ReAct、ReAct 记账。
