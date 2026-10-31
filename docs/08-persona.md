# 08 · 人格系统与回归测试

> 人格是产品核心，不是配置项。Phase 0 就要做，且要有回归测试。

## 1. 人格包（Persona Pack）

人格是**可热更新的数据包**，不是代码里的字符串：

```
personas/dafeiyu/
├─ meta.yaml        # 名称、版本、适用通道、TTS 音色引用
├─ soul.md          # 身份、性格、价值观（稳定，进 system）
├─ rules.md         # 说话规则、禁忌、隐私红线（稳定，进 system）
├─ samples.md       # 语气样本（稳定，进 system）
├─ context.md       # 场景化补充（直播/私信/QQ 各自的口径）
└─ eval/
   ├─ cases.jsonl   # 回归用例（用户消息 + 期望特征）
   └─ baseline.md   # 基准输出（可人工 review）
```

### 注入顺序

```
system:
  soul → rules → samples
user content 末尾:
  <context> 当前场景口径（按通道选） + 共享状态 + 召回记忆
```

## 2. 与现有资产的关系

现有四份人设是宝贵的 prompt 工程资产：

- `~/.bili-ai/persona.md` / `persona-live.md` / `persona-shared.md`
- `~/.dsh/persona-qq.md`（2026-10-31 新写，**在 `.dsh/` 而不是 `.bili-ai/`**）

| 现有 | 迁移到 |
|---|---|
| persona.md 的身份/玩梗/词库/咒术 | `soul.md` + `samples.md` |
| persona.md 的人味铁律/AI 味清单 | `rules.md` |
| persona-live.md 的直播规则 | `context.md`（live 段） |
| persona-shared.md 的娇娇/纯爱/兜底 | `soul.md` + `context.md` |
| persona-qq.md（`~/.dsh/persona-qq.md`） | `context.md`（qq_private / qq_group 段） |

**迁移原则**：先原样搬，再按回归测试调，不要重写。

## 3. 跨通道一致性

同一人格，不同场合浓度：

| 通道 | 浓度 | 规则 |
|---|---|---|
| 直播弹幕 | 淡 | 短、梗优先、不喊主人 |
| 评论 | 淡 | 更短、接梗 |
| 私信 | 浓 | 可撒娇、喊主人（仅主人） |
| QQ 私聊 | 浓 | 同上 |
| QQ 群 | 淡 | 被 @ 才回 |

浓度差异放 `context.md`，soul/rules 共享。

## 4. 回归测试

### 4.1 用例格式（`eval/cases.jsonl`）

```json
{"id":"live-001","channel":"live","input":"你是不是喜欢这条正在直播的大肥鱼","expect":["认/护短","不否认"],"forbid":["解释梗","长句","喊主人"]}
{"id":"dm-001","channel":"dm","input":"今天好累","expect":["哼/鲸~","短句"],"forbid":["列表","建议"]}
```

### 4.2 执行方式

- `fishctl persona eval --pack dafeiyu --brain openai`
- 每条用例跑一次，用规则 + 可选 LLM 评分器判断 pass/fail
- 输出报告：通过率、失败用例、token 消耗

### 4.3 验收门槛

| 阶段 | 门槛 |
|---|---|
| P0 | ≥ 90% 通过 |
| 后续 | 不下降超过 5% |

**任何 prompt / 模型变化都必须跑回归**。

## 5. 版本与 A/B

- `meta.yaml` 带版本号
- 支持 `fishctl persona switch --pack dafeiyu@v2`
- 记录每次切换的时间与结果（便于回滚）

## 6. 人格与记忆的边界

| 放人格 | 放记忆 |
|---|---|
| 身份、性格、语气、禁忌 | 具体事实、说过的话、承诺 |
| 稳定（不随对话变） | 动态（随交互变） |

别把「今天余额 40」写进人格——那是记忆/状态。

## 7. 测试要求

- 回归集至少 30 条，覆盖：直播/评论/私信/QQ × 正常/被夸/被骂/看不懂/隐私提问
- 每条用例带 `forbid`（禁止特征），比 `expect` 更重要
- 报告必须可读：失败用例要打印实际输出

## 8. 防坑清单

- ❌ 人格写死在代码里（不可 A/B、不可回滚）
- ❌ 人格与记忆混写（状态变化会「改人格」）
- ❌ 改 prompt 不跑回归（语气漂移没人发现）
- ❌ 把所有场合用一份 prompt（公开场合过度亲密）
