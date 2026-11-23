# 真实模型评测记录（2026-11-24）

> 模型：`deepseek:deepseek-chat`（经本地代理访问官方 API）
> 代码基线：`0c46aaa`（Reflection/缓存实验代码在其后提交）
> 产物：`dist/eval-real/`（本地保留，不入库）

## 1. 人格真实评测（35 条）

- 运行：`fishctl persona eval --rules-version persona-lexical-v5 --compare-rules persona-lexical-v1`
- 完成 35/35，自动安全原始输出 **35/35（100%）**，投递输出 35/35（100%）。
- 用量：输入 338,440 tokens、输出 544 tokens；费用 **$0.102186**。
- 旧 v1 规则对照：15/35（42.9%），仅作口径对照，不是本轮失败。
- 结论：自动安全线达标；`manual_review_status=pending`，仍需人工硬核查，
  不把自动安全率当最终验收。

## 2. Reflection 录制对照（50 条）

- 运行：`fishctl persona reflect`（同一输入 Reflect=off/on 各一次，ReAct 单工具环境；
  LLM judge 判定 improved/harmed/neutral）。
- 结果：changed **94%**（47/50），improved **21/50（42%）**，harmed **1/50（2%）**，
  neutral 28；未触发 5% 伤害告警。
- 延迟：反思侧平均 **+613 ms/条**；输入 token 平均 **+9,512/条**
  （自检会把上下文与草稿再发一次，这是主要成本）。
- 结论：反思确实能改写（94%），但“变好”只有 42%、2% 变坏；
  当前默认只对长草稿/用过工具/评论私信显式开启的策略是合理的，不建议全量默认开。
- 录制文件可直接复算：
  `go run ./cmd/eval --reflection-runs <dir>/reflection-runs.jsonl`，
  复算得到同样的 changed/improve/harm 数字。

## 3. Prompt 缓存 A/B（20 条，DeepSeek）

对照方式：

- stable：当前线上顺序（人格/协议前缀固定，动态资料在后）；
- unstable：在 system 最前插入 512 字符随机前缀，破坏“稳定前缀”假设。

每侧先热身一次，再测量一次；结果：

| 侧 | 输入 tokens | 缓存命中 tokens | 命中率 | 费用 USD |
|---|---:|---:|---:|---:|
| stable | 196,602 | 193,021 | 98.2% | 0.059281 |
| unstable | 202,802 | 199,040 | 98.1% | 0.061165 |

- 两侧都接近全量命中；unstable 只是多了随机前缀本身的 token，费用反而略高。
- 结论：**DeepSeek 的缓存不依赖当前这种“前缀顺序”假设**，现有布局已经充分命中；
  不做额外前缀重排，也不引入缓存专用代码。报告保留为负结果，避免后续重复实验。

## 4. 未做/待人工

- 35 条人格结果的逐条人工硬核查（自动安全≠最终验收）。
- Reflection judge 的抽样人工复核（LLM judge 可能有偏好）。
- 以上两项都需要人读回复，无法由本 agent 代替。
