# K0 契约裁决与 DSH 同步单

2026-10-31，America/Los_Angeles，Codex 技术裁决。依据主人确认：实施计划有效，DSH 已同步知识设计摘要；正式契约由 Codex 裁决、DSH 同步。本文是契约编写依据，不代表代码已经实现或 P0.5 已放行。

## 1. 生效范围和任务归属

- 批准知识契约基线名 `P05-KNOWLEDGE-1`，由 DSH 新建 `contracts/knowledge.md`，同步 Brain 增量及契约索引；本文不直接修改这些文件。
- [实施计划](phase-0.5-implementation.md) 继续有效，[知识摘要](../docs/knowledge.md) 为对应设计入口。正式接口文件同步后成为代码实现真源；本文用于追溯裁决，不形成第二份可任意分叉的接口。
- 场景装配实验 `20261031T124251Z-43a917e1` 属于主对话，13/35 为负面结果；回滚和验收由主对话负责。本知识任务不重新实施 P0-A、不重跑模型、不操作其源码、契约或证据。
- K0 不依赖“scene 必须进入 instructions”这个实验方案。场景组装以主对话完成回滚后实际生效的 Brain 契约和确定快照为准；知识只进入动态资料段。
- P0.5 开发仍受实施计划 G2 约束。本次是文档/契约裁决，不豁免 P0 退出标准，不启动加载器、迁移、知识表或 RAG。

## 2. Brain 输入裁决

在 `brain.ContextInput` 新增唯一知识入口：

```go
Knowledge *knowledge.Context
```

与 Memory、Shared 并列，不扩展 `memory.Context`，不把知识伪装成记忆、共享事实或事件；`persona.Pack` 不增加 Knowledge 字段。Brain/Provider 的方法签名、发送/用量账本协议保持。

- `nil`：该次请求不启用知识，沿用当前有效 P0 基线的消息及预算行为。
- 非 nil：启用知识，即使 Items 为空，也必须携带合法目标 Scope 与 Revision；不能因为零命中或预算比例0而重新裁人格。
- 可信装配方根据 Event 创建 Scope；Knowledge/Memory/Shared 的目标范围及 Task/Event 引用必须一致。不能从正文、昵称、平台 Meta 或模型输出获取权限。
- 读取或加载失败必须报错，不能以 nil 或空 Items 隐藏失败。Brain 输入校验失败时 Provider 零调用，不写虚假用量。

## 3. 知识最小 Go 契约

文件类型不等于数据库模型。`knowledge` 不依赖 Brain、memory、persona、GORM 或具体 LLM 客户端。

```go
package knowledge

import (
    "context"
    "RaiseMyFish/internal/event"
)

type Scope struct {
    Channel event.Channel
    Session string
    Private bool
}

type Query struct {
    Scope Scope
    Text string
}

type Item struct {
    ID string
    Source string
    Text string
    Scenes []string
    Priority int
    Private bool
    Origin *Scope
}

type Context struct {
    Scope Scope
    Revision string
    Items []Item
}

type Reader interface {
    Recall(ctx context.Context, query Query) (Context, error)
}

func ValidateScope(scope Scope) error
func (c Context) Validate() error
```

`NewFileReader(ctx context.Context, dir string) (*FileReader, error)` 属于 K0 文件实现入口；装配方可调用，消费方只依赖 Reader。`FileReader` 的字段不进入公共契约，不暴露可变内部快照。

I/O/查询传 Context，纯校验无 I/O。取消/超时错误保持 `errors.Is` 可判定，错误包装 `%w`；不回显知识正文、密钥或完整 prompt。返回结果作为只读数据，包含 slice 及 Origin 的独立副本；调用方修改返回结果不能污染 Reader 或其他请求。

## 4. Scope、来源与查询隔离

- Channel 仅 live/comment/dm/qq；Session 非空。live/comment 的 Private 必须为 false，dm 必须为 true，qq 可为两种。
- Item.Scenes 非空、无重复，只接受 live/comment/dm/qq_private/qq_group，且必须允许当前目标场景。场景键不是 Event.Channel，也不授予身份权限。
- Item.ID、Source、Text 非空且合法 UTF-8，ID 在返回 Context 内唯一；Priority 为0–100。Revision 为64位小写十六进制 SHA256。
- 公开 Item：Private=false，Origin=nil；内容必须来自经审核的公开来源，仍需满足 Scenes。
- 私密 Item：Private=true，Origin 必须是合法的私密 Scope，且与目标 Channel/Session 完全一致。目标必须 Private=true。不能跨平台或跨会话扩大查询，不因“都是主人”而例外。
- DM/其他私密来源必须保留私密来源标记，导入器不能删除来源后降级公开。DTO 无法推断已被上游抹掉的来源，因此来源审核和 Reader 查询过滤不可由 DTO 或 prompt 代替。
- Reader 先过滤权限/场景，再匹配排序、统计及返回；Brain 再做范围校验。进入 P2 后这项过滤必须在 repository/SQL 查询层实现，不能先返回私密行再让上层剔除。

空合法查询文本或完全无命中返回带 Scope/Revision 的空 Context；无效 Scope、无效 UTF-8、读取失败与取消属于错误，不是无命中。正常 Reader 最多返回3条，按确定顺序排列；Brain 不自行扩召回或调用文件实现。

## 5. 文件、召回和版本

条目格式、文件清单和资源限额按实施计划第3.1节：一个 YAML front matter、schema=`fish-knowledge/v1`，四个根文件及梗批次；允许空 entries，缺少必需根文件报错。严格检查未知/重复字段、多文档、重复 ID、UTF-8、范围及资源限制；不输出半成品。K0 不增加新运行时依赖。

文件字段 `channel` 映射到 Item.Scenes；`visibility` 映射 Private；private 的 `scope` 映射 Origin。Item.Text 只包含审核后的知识正文/原词与接法，Source 是不含本机绝对路径的来源引用。keywords/tags/priority等检索元数据不提升为模型指令。

为消除“别名”和“关键词”的未编码分类歧义，K0 将 keywords 中的显式别名与其他触发词视为同一匹配等级，不根据词意猜测哪个是别名，也不新增 aliases 字段。排序固定为标题短语命中 > keywords短语命中 > 有效标签命中；同等级按最长命中词、不同命中词数量、priority降序，再ID升序。泛分类不触发，英文有单词边界；无命中不随机补梗。

Revision 依据实际读取的原始文件字节，独立于人格版本。算法固定为：相对路径统一 `/`，按相对路径字节升序，对每个实际加载文件计算原始字节 SHA256；拼接后再取 SHA256：

```text
fish-knowledge/v1\n
<relative_path>\t<lowercase_sha256_of_raw_file_bytes>\n
...
```

这里 `\n`/`\t` 表示真实换行/制表符；末行也带换行。相对路径只来自加载白名单，不含换行、制表符或绝对目录；路径移动到另一个本机目录不改变版本。BOM/CRLF 在解析时可规范化，但原始字节变化必须改变 Revision。评估器冻结的是实际使用的文件快照，不能只存路径或 mtime。

## 6. Brain 预算裁决

在 Options/config 新增知识预算整数百分比，默认10，合法0–15；大于15或负值拒绝配置。配置 `knowledge.enabled=false` 默认关闭；关闭时不读知识目录。

预算仍按现有 UTF-8 消息字节加64框架余量，不能宣称精确模型 token。令 B 为总上限，P 为完整人格/场景/事件/固定说明/空资料包装，M 为按现行优先级保留的记忆和共享状态：

```text
K_cap = min(floor(B * percent / 100), max(0, B - P - M))
```

- 先保留完整人格和当前事件，再保留记忆，知识使用余量；知识不挤占人格或反向驱逐记忆，没有保证最小配额。
- 非 nil Knowledge 且 P>B 时返回 budget，Provider 零调用；即使percent=0/Items为空也遵守。nil路径沿用当前有效P0行为，不把主对话场景实验混入K0变更。
- 知识占用是同一请求带知识与空知识版本的最终序列化增量，包含JSON转义/键/分隔。按候选顺序整条尝试，放不下可尝试后续较短条目，不截断接法。最终总估算重新检查不超过B。
- Knowledge 与 Memory/Shared 共用现有4096项/4MiB请求输入防护；库文件大小限制另算。裁剪只操作副本。
- system 精确保持人格拼接；知识只作为转义的 user context 资料，不放进system或可信instructions。不得借此重新试验场景摆放。

## 7. 审计和验收增量

DSH 在 persona-eval 契约中按加法记录：知识启用/预算比例、文件相对路径及原始hash、Revision、Reader/预算代码hash，每条候选ID与实际注入ID、裁掉ID、知识估算增量及总prompt估算。只记录必要元数据，不保存完整知识正文、prompt或本机源路径；不能把候选当实际注入。私密会话的审计同样不得向公开输出暴露其资料。

旧 run/旧规则/旧manifest不补字段或重写。新评估目录独占创建；输入不含expect/forbid、审计观察或baseline。独立留存原始输出、投递文本、实际usage与未知费用NULL。知识开关对照按计划进入对应步骤后执行，不因本裁决自动发起。

必测：合法空Context、目标错配、DM伪公开、跨私密会话、QQ群/私聊、零命中与错误区分、排序/Top3、文件和请求限额、原文转义、百分比及序列化增量、启用时完整人格保留、关闭路径兼容、Provider零调用反例、裁剪不污染快照，以及假HTTP→Brain→真实PG用量账本。质量门槛继续按100条检索评测和首批500条迁移计划执行。

## 8. DSH 同步清单

- [ ] 新建 `contracts/knowledge.md`，基线 P05-KNOWLEDGE-1，完整纳入第2–5节接口与语义。
- [ ] 在 `contracts/brain.md` 增补 Knowledge字段、校验、nil/启用预算差异及user资料边界；不要触碰主对话尚在处理的场景回滚内容。先与主对话确认可写窗口，再同步同一文件。
- [ ] 同步 `contracts/README.md` 索引及 `contracts/persona-eval.md` 的知识审计加法，保留旧版本行为说明。
- [ ] 在实施计划补记实验归主对话且不重复执行；将本次明确的keywords等级、Revision算法和必需根文件规则同步进对应条款，不覆盖历史证据。
- [ ] `docs/knowledge.md` 作为摘要引用正式契约；不要把本裁决写成K0功能完成或G2已通过。
- [ ] 做相对链接、规则一致性及 `git diff --check` 核对，登记确切文件快照；DSH本人记录完成回执。

Codex本轮只发布本裁决，不实施知识代码、不执行迁移、不运行真实模型、不承担主对话的场景回滚，也不代写DSH完成回执。
