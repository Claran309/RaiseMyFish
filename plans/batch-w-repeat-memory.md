# 批 W：#93 重复度触发提炼 + 放宽提炼器

## 背景（已实测）

- 2026-11-12 直播里「奇迹鱼」（= X奇迹与你X）被提及 **14 次**
- 提炼器每轮 `scanned 0~1 / extracted 0 / skipped 1` —— **全部跳过**
- 结果：说了 14 次也没进记忆，鱼回答「奇迹鱼？没听过」
- 根因：提炼器是**单条判断**且被收紧成「只记稳定偏好/身份/承诺」，闲聊/称呼一律过滤（之前清过 122 条垃圾记忆的补偿措施）

## 目标

1. **重复度触发**：同一实体在窗口内反复出现 → 强制提炼（说多了就该记住）
2. **放宽提炼器**：允许记录「反复出现的称呼/关系」，同时不把垃圾带回来

## 修法

### A. 重复度触发（核心）

1. **实体抽取**：从事件文本里抽候选实体（人名/昵称/称呼）：
   - 命中 `personas/*/viewer_aliases.yaml` 的别名
   - 命中观众档案的显示名
   - `@` 结构化目标（Meta.reply_to_name）
2. **重复计数**：滑动窗口（默认 7 天）+ 阈值（默认 5 次，配置 `[memory] repeat_promote_threshold`）
3. **达标即提炼**：同一实体在窗口内 ≥N 次 → 生成一条 `fact`：
   - title：`称呼：奇迹鱼 = X奇迹与你X`
   - summary：`弹幕里「奇迹鱼」指的是 X奇迹与你X（7 天内提及 14 次）`
   - importance 初始 0.5，evidence 记次数 + 代表事件 ID（最多 5 条）
4. **去重**：已有同 subject+同标题的记忆不重复写；次数变化只更新 summary 的计数
5. **不达标不写**：1~N-1 次仍然只是闲聊（保持现有过滤）

### B. 放宽提炼器（LLMExtractPrompt）

新增允许（原来一律过滤）：
- **反复出现的称呼/别名**（配合 A 的次数）
- **明确的关系陈述**（「X 是 Y 的谁」，只记明确句，不记猜测）
- **小圈子黑话/梗的固定含义**（出现 ≥N 次且上下文一致）

保持过滤（不许回来）：
- 单次玩笑、骂人、命令（「帮我查」）、钓况流水账
- 纯语气词、表情、复读
- 任何以第一人称编造的自我身份（防「我是梁文峰」复现）

### C. 防垃圾护栏

- 新事实必须带 `evidence`（次数 + 事件 ID），无证据不写
- importance ≤ 0.6 起步；后续被主人纠正/删除即降权
- 同实体 24h 内最多触发 1 次提炼（防抖）

## 验收

- 单测：实体抽取 / 窗口计数 / 达标触发 / 未达标不写 / 去重 / 防抖
- 真机回放：用今天 14 条「奇迹鱼」事件跑一遍 → 生成 1 条 fact，鱼能答出「奇迹鱼是 X奇迹与你X」
- 负面：单次「我是梁文峰」不生成记忆

## 执行规则

fishd 不重启（交付后 DSH 验证）；不执行 git；不改 personas 文本（配置项走 TOML）。

## 实施状态（2026-11-13，待 DSH 验收）

- [x] W1 config：`[memory] repeat_promote_threshold`（默认 5，0/1 关闭；校验 0~1000）
- [x] W2 memory 纯函数：`RepeatEntities`（别名/档案名/@ 目标）、`FilterRepeatEntities`（忽略鱼自身名）、`CountRepeatMentions`、`DedupeRepeatCandidates`、`CorrectedRepeatAliases`、`RepeatFactTitle/Key`（`internal/memory/repeat.go` + 单测）
- [x] W3 database：`EventRepo.PublicIncomingSince`（只取公开入站、`at >= since`、上限保护）、`MemoryRepo.RepeatFactByTitle` / `DownweightFact`、`RepeatFactStore` 窄接口
- [x] W4 extractworker：`RepeatPromotion` 子任务接入 `RunOnce`/`runJobs`；达标写 `live/global` fact（title `称呼：奇迹鱼 = X奇迹与你X`，summary 带 7 天次数，importance 0.5，evidence=次数+最多 5 条事件 ID）；24h 防抖 + 同标题去重；主人纠正 → 降权 0.2 不再新建；已归档不复活；失败只记 stats 不影响提炼主流程
- [x] W4 护栏：fact 无 evidence 不写；LLM 提炼器允许「反复称呼/明确关系/黑话」，拦截 bare 第一人称身份句（「我是梁文峰」等）且不调模型；fact importance 上限 0.6
- [x] W5 fishd：装配 `RepeatPromotion`（`cfg.Memory.RepeatPromoteThreshold`、窗口 7 天、防抖 24h、`IgnoreNames` 取调度器 `self_names`）；typed-nil 防护；启动/每轮日志带 repeat 统计
- [x] 文档：`docs/06-memory.md` 增「2.1 重复度触发（#93）」
- [x] 门禁：`go build ./...`、`go vet ./...`、`go vet -tags integration ./internal/database/`、`go test ./... -count=1`、`go test -race ./internal/memory/... ./internal/config/ ./cmd/fishd/ -count=1` 全绿
- [ ] 真机回放（DSH）：14 条「奇迹鱼」→ 1 条 fact 且鱼能答；「我是梁文峰」不生成（fishd 不重启，本轮不跑）
