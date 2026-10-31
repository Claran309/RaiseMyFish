# 0.6 Brain放行单 · P0-MEMORY-1

> 开发者：Codex；独立验收/会话辅助：DSH。主人明确要求在0.7b提交后确认Memory最小类型并放行0.6，授权调整P0内实施顺序，不跨Phase。

## 基线与范围

必读AGENTS、coordination、contracts/brain.md、contracts/persona.md、contracts/memory.md、docs/05-brain.md、docs/09-data-model.md及当前Phase计划。Pack已接收；Memory基线为P0-MEMORY-1，以包含memory契约与本单的同一Git提交定位。0.7b实际提交号见Codex COMM-007。

本轮仅发布放行条件，不在本次提交里实现Brain。收到下一次开发会话任务后，Codex在自己的报告确认本单和写入范围；不由WorkBuddy接管核心逻辑。

允许开发路径：

- internal/brain/及provider子目录：Brain/Provider接口细化、OpenAI兼容net/http客户端、上下文组装、预算、回复长度与失败分类；不实现工具调用。
- internal/memory/context.go、context_test.go：仅P0-MEMORY-1 DTO/纯校验，不做Reader真实查询、提炼、版本化或缓存。
- internal/event/types.go及对应类型校验测试：仅event.md已有类型/枚举，供Task/Scope引用；不趁机实现Bus/恢复状态机。
- internal/store/usage.go、internal/database/usage.go及对应测试：仅0.6必需的UsageRecorder/repository最小切片，写已有llm_usage表，不完成其他Event/Task/MemoryRepo，不改迁移schema。
- internal/config/、config/fishd.toml与cmd/fishd：只补Brain所需Provider/routing/超时/预算与装配接缝，保持未实现通道关闭；不伪装成已可消费事件。
- go.mod/go.sum、对应技术契约/设计/测试文档、Codex报告和教学说明：依赖与接口变化由Codex统一管理。

不改WorkBuddy仍持有的人设正文/eval；不恢复旧0.7c草稿、不写DSH/WorkBuddy回执、不实现collector/scheduler/sender或其他Phase。

## 0.2未整体完成的处理

原分工要求0.6依赖0.2。本轮按主人放行指令调整为：0.6只补齐自身必需的UsageRepo切片并真实PG验证，其余0.2任务保持未完成；不把缺repo当成可以不记账的理由。Brain只依赖注入接口，GORM实现在database，cmd统一装配，不能在Brain内直接Exec/Raw。

这是同一P0内的必要依赖切片，不要求先写整套repository或总线。Memory真实召回仍归0.9，不因DTO已定义就宣称跨通道记忆已上线。

## 开发与验收要求

1. 先用本单、brain.md与现有数据表细化Provider/Usage最小Go类型、typed error和配置，再写失败测试并实现；工具预留字段只保持空值，不引入工具执行协议。未明确的语义先记录待定并裁决，不自行扩大方向。
2. 输入为Task和显式ContextInput；Persona.Load只在装配阶段读取；按Memory.Scope校验后组织system静态人格、user场景/共享事实/记忆/event/instructions。不记录完整prompt或凭据。
3. OpenAI兼容请求走net/http和可注入client，所有外部调用有超时、总时长/重试上限和取消路径；超时/5xx最多重试2次，4xx按既有Brain契约不可重试，不悄悄改429语义；空回复最多补一次，禁止两层叠加成无限重试。
4. 单次预算配置化；先裁低价值记忆，再裁非soul人格，soul必须保留。如果最小必要内容本身超预算则返回明确错误，不带超限prompt强行请求。回复按Unicode字符截断到通道上限，禁止以字节切中文。
5. 每个实际Provider调用尝试都记provider:model、token/耗时/成功失败到llm_usage。错误路径不能吞掉；UsageRepo通过注入接口，纯测试用fake，落库集成用真实PG。缺使用量/价格不能伪造「已知零费用」，应按可空列记录未知并说明费用估算配置；字段/错误接口在编码前细化契约。
6. mock HTTP覆盖成功、超时、取消、5xx、4xx、空回复、格式错误、预算超限、中文长度和隐私范围拒绝；拒绝路径Provider零调用。记忆裁剪不改原slice，静态system重复调用稳定。
7. 全仓test/vet/build/lint、可用Linux race、真实PG用量记账通过；已有迁移/infra/人格测试保持通过。真实模型调用和0.7c人格评估另有明确成本/授权与输出证据，不以mock代替≥90%。

交付给DSH确定快照、命令与脱敏结果、失败/未执行项、恢复或预算风险、是否暂停写入；DSH独立验收后Codex再提交/push。此放行只允许开工，不代表0.6或真实记忆完成。
