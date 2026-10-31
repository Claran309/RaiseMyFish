# 知识分层与 K0 实施计划（含 0.7c 前置优化）

> 最新显式放行：主人已继续K0-6装配/无调用check及K0-7的100条离线检索，范围见 [K0-6/7执行单](phase-0.5-k0-6-7-dispatch.md)。首次测量相关率46/56=82.14%、覆盖43/60<48，质量门禁未过；真实模型开/关各35条明确不跑。下方原规划/历史同步不据此改写验收事实。

> K0-5最新放行：人格已闭环，主人收紧知识上限为10%；本计划原0–15均由此覆盖。当前开发范围及结果见 [K0-5执行单](phase-0.5-k0-5-dispatch.md)，K0-6/7不随之自动放行。下方历史DSH同步记录不改写。

> 执行方式：后续使用 executing-plans 技能逐项实施；每次只推进一个阶段内的任务。不得因本计划列出协作角色而自动创建或派发其他会话。
> 2026-10-31，America/Los_Angeles。主人已认可知识分层方案，并要求“先写计划文档，后续按步实施”。本轮仅落盘计划，不启动实现、迁移或真实模型调用。

**目标**：将人格、知识、记忆分开，在 P0 完成人格优化，在 P0.5 引入可验证的文件知识召回；结构化知识与 RAG 留在 P2。

**架构**：人格继续提供静态 system 与可信场景规则；独立 knowledge Reader 按可信目标范围和事件正文召回资料，由装配方通过 `ContextInput.Knowledge` 交给 Brain。Brain 校验范围、统一裁剪记忆与知识预算，将知识放入 user 资料段。

**技术栈**：Go 1.25+、现有 YAML v3、标准库、现有 Brain/Provider/PG 用量账本。离线迁移使用 Python 标准库 sqlite3 只读导出源库，不增加 Go 运行依赖，不将 SQLite 用作本项目业务库。

## 1. 阅读入口、基线与阶段门禁

先读 [AGENTS](../AGENTS.md)、[分工](assignment.md)、[协作裁决](coordination.md)、[知识设计](../docs/knowledge.md)、[K0 阶段计划](phase-0.5.md)。P0 人格优化引用已有 [独立优化设计](phase-0.7c-optimization-design.md) 与 [A0 意图审计](reports/persona-intent-audit/20261031-a0/audit.md)，不覆盖它们。

本计划写作时 Git HEAD 为 `008beb6`：v2 评分功能与 57.1% 证据已接收。共享工作区仍有未提交文档；开工时重新核对 HEAD、实际文件哈希和写入者，不将本文提交号当作永久最新基线。

文档校验期间观察到并行会话新增 `internal/brain/context_test.go`、修改 `internal/brain/context.go`，并生成 `plans/reports/scene-assembly-preflight/` 和新 run `20261031T124251Z-43a917e1`。本计划作者没有修改这些文件或发起该调用，也未对其作独立验收。P0-A 开工前先核对这批产物、实际授权和 DSH 结论；已有有效交付应接续验收，不照着清单再次开发或重复运行35条。未验收结果不得替换已接收的20/35基线。

| 门禁 | 条件 | 可以推进 | 不能据此宣称 |
|---|---|---|---|
| G0 文档准备 | 本轮授权 | 本计划与后续任务拆分 | 已批准立即执行付费实验或跨 Phase |
| G1 P0 优化 | 明确进入 0.7c 对应步骤；DSH 确认文件归属及实际快照 | 第 5 节 P0-A0、P0-A；后续候选遵循原优化设计的放行条件 | P0 全部退出标准通过 |
| G2 P0.5 开工 | P0 退出标准通过；或主人明确记录接受未满足项并允许进入 P0.5 | 第 6 节 K0-1 至 K0-7 | 仅凭人格达标就默认豁免真实直播、恢复、观测等其他退出标准 |
| G3 扩量 | 首批 500 条及 K0 验收通过，DSH 确认 | 后续知识批次 | 未整理的源记录已全部可用 |
| G4 P2 | P2 独立放行 | K1、可选 K2 | 本轮可预建向量、知识表或嵌入 worker |

本轮所有实现复选框保持未完成，表示本计划没有代替 DSH 认领验收，不否认并行会话已有交付。后续明确进入某个包含真实评估的步骤时，按该步骤的一次完整批次执行；已有授权有效时不重复询问。不得将过去某一批 35 条的授权用于自动追加不满意重抽、换模型或另一批实验。

## 2. 三层边界与技术裁决

| 内容 | 归属 | 例子 |
|---|---|---|
| 身份、价值观、语气、称谓、禁忌、表达方式 | 人格 | 纯爱原则、公开不喊主人、看不懂就承认 |
| 经审核的资料、梗的含义与接法、世界观说明 | 知识 | 某个梗怎么接、娇娇与两条鱼的背景 |
| 有交互来源的事实、偏好、承诺、历史 | 记忆 | 某观众昨天说喜欢哪个游戏 |
| 实时余额、直播状态、运行配置 | 可信状态或配置 | 不用旧设定假装当前状态 |

知识目录位于人格包旁边，不代表知识属于 `persona.Pack`。Pack 五文件读取、`SystemPrompt()` 拼接和加载器职责保持；知识不存入 `Memory.Records`、SharedState 或伪造事件。

**契约方向确定为显式新增 `ContextInput.Knowledge *knowledge.Context`。** 不采用临时扩展 memory.Context 的路线；后续先同步正式契约，再修改代码。当前文件中的类型是实施目标，不代表正式契约已生效。

`nil` 表示知识功能未启用；启用且零命中必须返回带正确 Scope 与知识版本的空 Context。读取失败不能伪装零命中。知识 Context 与 Task/Event 在 Channel、Session、Private 上必须一致；不符时 Brain 返回 input 错误，Provider 零调用。

召回只接收事件正文及可信装配方提供的范围；不接收 eval 的 expect、forbid、kind、baseline 或人工审计结论。private、昵称及正文“我是主人”都不授予主人身份，不新增身份绑定系统。

## 3. K0 数据、召回与预算规范

### 3.1 目录和条目

首批目录为 `personas/dafeiyu/knowledge/`：`lore.md`、`memes.md`、`viewers.md`、`games.md`。后续梗批次置于 `memes/batch-0002.md` 等文件。加载清单固定为这四个文件及 `memes/batch-*.md`，不递归扫描其他目录、不读取外部源库或任意路径引用。

每个文件使用一个 YAML front matter；围栏外 Markdown 仅为维护说明，不参与召回。空知识文件使用 `entries: []`，不把占位说明当条目。

```yaml
---
schema: fish-knowledge/v1
entries:
  - id: meme-example-001
    title: 原词
    channel: [live, comment, qq_group]
    keywords: [原词, 别名]
    tags: [吐槽]
    priority: 50
    visibility: public
    source: meme-source:example
    summary: 原始解释
    usage: 原始接法
---
```

| 字段 | 约束 |
|---|---|
| schema | 必须为 `fish-knowledge/v1` |
| id | 全库唯一、非空、稳定；迁移 ID 由来源层及原始主键计算，不按行号生成 |
| title | 原词或知识主题，非空；原文保留 |
| channel | 非空去重列表，仅 live/comment/dm/qq_private/qq_group；这些是适用场景，不替换 Event.Channel |
| keywords | 显式触发词或别名；不得从整段解释任意扩词 |
| tags | 必填列表；允许空列表；“梗”“知识”等泛分类不作为独立召回触发词 |
| priority | 整数 0–100，必填；排序只在匹配强度之后使用 |
| visibility | 必填 public/private；缺失不默认公开 |
| scope | private 必填，含原始 channel/session/private=true；public 不填写 |
| source | 必填的不含凭据的来源引用；不得把本机绝对路径放入模型资料 |
| summary | 原始解释或知识正文；非梗条目必须非空 |
| usage | 梗条目必须非空，保留接法原文；非梗条目允许省略 |

条目类别由文件归属确定：lore/meme/viewer/game，不从内容猜测。源字段为空与源字段不存在分别在原始归档保留；激活条目仅满足上述规范后生成。

只允许常规 UTF-8 文件；允许 BOM/CRLF，规范化后不改正文内容。单文件上限 1 MiB、单条资料上限 16 KiB、全库上限 32 MiB/10000 条；限制是 I/O 防护，不是 prompt 配额。分批文件达到上限时拆文件，不调大 persona 的读取限制。使用受限读取、取消检查、严格 YAML 字段校验，拒绝重复字段/多文档/重复 ID/非法字段；不返回半成品。拒绝链接逃逸知识根目录，不展开 YAML 别名以绕过大小边界。

首批只加载经审核的公开梗、设定及公开观众资料。没有真实可用观众资料时，单元测试用合成样例验证 viewer 类，不编造观众画像；K0 三类真实素材验收项仍保持未完成。

私密资料若后续纳入，文件须置于被忽略的本地数据目录，通过可信配置显式加载；不可把私信和私密画像提交到人格目录。K0 先实现同会话过滤与反例测试，不自动从 Memory 提炼或复制私密内容。

### 3.2 召回

查询顺序为权限过滤 → 通道场景过滤 → 文本匹配 → 稳定排序 → 取前三条；过滤后的集合才可以参与命中统计和审计。公开资料仍须符合 channel；私密资料仅允许目标 Private=true 且来源 Channel/Session 完全一致。QQ 群与 QQ 私聊必须分开；DM 来源不能通过误标 public 进入公开结果。

匹配只做大小写、首尾空白等确定性规范化。标题/显式别名短语命中优先于其他关键词，再优先于有效标签；同级按最长命中词、不同命中词数量、priority 降序，最后 ID 升序。重复词不重复加分；纯标点、空文本、泛分类不触发；英文单词按边界匹配，中文短语按显式词匹配。不做分词模型、embedding、随机梗或 LLM 改写。

无命中返回空；没有资料不影响正常的“看不懂就承认”。不得为了有结果而返回排行榜最高的无关条目。Reader 返回的是候选，最终能注入多少由 Brain 决定。

### 3.3 预算

统一沿用当前保守口径：消息 UTF-8 字节数加 64 框架余量；配置名虽是 max_prompt_tokens，报告不得称其为精确 tokenizer 结果。

令 B 为总 prompt 上限，P 为完整人格、选中场景、当前事件及固定说明和空资料包装的估算；M 为按原有优先级保留的记忆/共享状态开销。知识上限为：

```text
r 默认 10%，允许配置 0–10%；大于 10% 直接拒绝配置
K_cap = min(floor(B * r), max(0, B - P - M))
最终 estimate(完整请求) <= B
```

记忆与知识共用 `B-P`，先保留记忆；知识不保底占位，可能被裁到零。知识开销按“带知识请求减去同一请求的空知识版本”的序列化差额计算，包含字段、转义、分隔；不能只累计 usage 字符数。按召回顺序尝试整条加入，单条放不下可跳过继续尝试后续较短条目，不截断正文和接法。

K0 启用时完整 P>B 直接 budget 错误，不裁 soul/rules/samples，不调用 Provider。K0 关闭时保留既有预算行为；这项条件变化必须在 Brain 契约明确。即使 K_cap=0，启用路径仍不得退回裁人格。只读复制入参，不原地改 slice。

system 只含人格；场景规则遵循 P0-A 的正式结果。知识、记忆、事件均使用结构化转义作为 user 资料，不成为场景指令。保留现有 4096 项/4 MiB 输入防护，将 Knowledge 项及被处理文本纳入；知识库全量加载上限与单次 Brain 上限是两件事。

## 4. 文件归属与预计影响

以下均为后续获派步骤的目标范围，本轮只新增本计划。

| 文件 | 职责 | 执行责任 |
|---|---|---|
| `contracts/knowledge.md`（新）、`contracts/brain.md`、`contracts/persona-eval.md`、`contracts/README.md` | 知识 DTO/Reader、预算、回归审计增量 | Codex 裁决，DSH 同步维护 |
| `internal/knowledge/types.go`、`validate.go`、`file.go`、`recall.go` 及对应 `_test.go`（新） | 类型、范围校验、文件快照、确定性召回 | Codex |
| `internal/brain/types.go`、`context.go`、`brain_test.go`；`context_test.go`、`knowledge_test.go`（新） | P0 场景组织及 P0.5 知识校验/预算 | Codex，分两个阶段提交 |
| `internal/config/config.go`、`knowledge.go`、`knowledge_test.go`（后两项新）；`config/fishd.toml` | 知识启用、目录和比例配置，默认关闭 | Codex |
| `cmd/fishd/brain.go`、`main.go` 及所属测试 | 运行时 Reader 装配、无调用诊断 | Codex |
| `cmd/fishctl/persona_eval.go`、`persona_eval_test.go`；`internal/personaeval/fixture.go`、`run.go` 及所属测试 | 评估器注入、知识冻结与审计 | Codex |
| `internal/database/usage_integration_test.go` | 假 HTTP → 实际 Brain/知识 → 真实 PG 账本组合路径 | Codex |
| `scripts/export-memes.py`、`scripts/test_export_memes.py`（新） | 只读导出、完整归档、稳定选择及幂等验证 | Codex 定接口；明确派发后 WorkBuddy 可做纯转换 |
| `personas/dafeiyu/knowledge/`（新）及独立候选人格目录 | 审核后的知识数据/候选数据 | 明确派发后的 WorkBuddy，Codex 集成 |
| `plans/reports/knowledge/`（新产物目录） | 迁移清单、质量抽查、预算/对照报告 | 开发者各写本人的交接；DSH 独立报告 |
| `docs/knowledge.md`、`docs/05-brain.md`、`docs/08-persona.md`、`plans/phase-0.5.md`、`plans/README.md` | 同步正式行为、入口和任务状态 | DSH；Codex提供明确变更说明 |

不修改既有 ADR、不建 P2 schema、不变更 Provider、发送路径、LLM 重试和记账协议。`go.mod/go.sum` 预计无新增依赖；出现依赖需求先由 Codex 复核是否突破 K0 范围。

## 5. P0 前置任务：人格与场景

### P0-A0：确认意图与对照基线

- [ ] 阅读已有 A0 审计及 JSON 来源哈希；DSH 独立检查全部 35 条，包括自动通过项，单列分歧，不由 Codex 代写验收。
- [ ] 固定 v2 主评分、v1 对照和既有 cases，不修改词表、分母或旧输出；人工语义观察单列。
- [ ] 将素材冲突清单与真实产品目标对齐；“情绪”不等同必须加“哼”，“不理解”不等同任何提问都兜底；凭据请求不能变为找主人批准。
- [ ] 记录新的工作快照及获派路径，确认上一写入者已暂停。

验证：`git diff --check`，以及读取已有 report/rules/cases 并核对审计引用哈希。预期引用来源一致、真实模型调用 0。失败则先修正新的审计记录，禁止重写旧证据。

### P0-A：先验证场景装配，不同时改人格

依据 [独立优化设计第 5 节](phase-0.7c-optimization-design.md)，只将可信 `ContextFor` 场景从“仅资料”的 context 中移入本轮 instructions；system 和原人格 v1 精确保持。末尾不再追加与场景冲突的宽松字数提示；防御截断不顺手修改。

- [ ] 在 `internal/brain/context_test.go` 先写下面用例并记录改动前失败，再修改 `context.go`。

```go
func TestSceneRulesStayOutsideDataContext(t *testing.T) {
    _, in := fixture()
    in.Persona.Contexts["live"] = "可信直播规则标记"
    in.Persona.Contexts["dm"] = "未选中私聊规则标记"
    messages, err := buildMessages(context.Background(), in, 32768)
    if err != nil { t.Fatal(err) }
    user := messages[1].Content
    end := strings.Index(user, "</context>")
    start := strings.Index(user, "<instructions>")
    if end < 0 || start < end { t.Fatal("分段顺序错误") }
    if strings.Contains(user[:end], "可信直播规则标记") ||
        !strings.Contains(user[start:], "可信直播规则标记") ||
        strings.Contains(user, "未选中私聊规则标记") {
        t.Fatal("场景规则被降为资料或混入其他场景")
    }
    if messages[0].Content != in.Persona.SystemPrompt() {
        t.Fatal("静态system发生变化")
    }
}
```

该测试使用现有 brain 包 `fixture()`；新增文件显式导入 context/strings/testing。其他必测场景为五场景选段、事件伪造结束标签仍被 JSON 转义、记忆不进入指令、预算保留场景、入参不变。

- [ ] 运行 `go test ./internal/brain ./cmd/fishctl ./internal/personaeval -count=1`；预期改动前新用例失败，改动后全部通过。
- [ ] 先同步 Brain 场景装配契约；不改 Pack 接口或 `SystemPrompt()` 约定。运行第 8 节开发门禁，DSH 独立复核确定源码快照。
- [ ] 明确进入真实评估步骤后，用同人格 v1、同 35 条、32768、temperature=0.7、输出上限128、同路由运行一次新批次，v2 主分/v1 对照；不为输出不满意重试或追加样本。

既有 CLI 命令模板如下，`config.local.eval.toml` 必须先核实为已启用、预算32768的本地配置，不在命令里传密钥：

```powershell
go run ./cmd/fishctl --config config.local.eval.toml persona eval --pack dafeiyu --route default --rules-version persona-lexical-v2 --compare-rules persona-lexical-v1 --timeout 20m
```

预期生成独占新 run，原始及投递输出均保留；35/35完成不等于达到90%。未达32/35时 CLI 非零属于评估失败结果，不代表可以删目录或自动重跑。保留五场景分数和逐条成败转移；服务端模型变化单列混杂因素。

### P0-B/H：候选人格与独立新用例

- [ ] A 仍不足时，按原优化设计的 B 范围整理独立候选包 `personas/dafeiyu-v2/`，保留原 `personas/dafeiyu/` 全部字节；需要继续修订时另升版本。
- [ ] 编辑清单必须覆盖规则优先级、句数/长度冲突、明确拒绝、公开称谓、陌生人私聊、避免编自身经历与无证据活动；示范输入不复制原35条case。
- [ ] 用冻结候选和 A 的相同代码/模型/参数单次跑原35条；候选尚未获派或产品语义冲突未定稿时，不提前编辑。
- [ ] 按原优化设计 H，由 DSH 准备并冻结35条新输入，在候选冻结后验证；原集与新集分别报告，不能合并掩盖一组失败。
- [ ] 至少32/35才满足各自90%门槛；人工另查硬违规。模型对照 M 独立放行，不包含在本计划的 K0 默认工作中。

P0-B/H 的具体编辑内容与新输入依赖 A 的结果；本计划不凭空给出未观察实验的修改答案。执行 B 前发布精确编辑清单，执行 H 前冻结用例意图及哈希。这是顺序门禁，不允许用空泛任务直接派发素材改写。

## 6. P0.5 实施任务

### K0-1：正式契约与最小类型

**前提**：G2 已满足并记录依据。先由 Codex裁决、DSH同步 `contracts/knowledge.md`（P05-KNOWLEDGE-1）和 Brain 增量，随后实现。P0 实验与本任务不得混成一个提交。

知识包的目标消费接口为：

```go
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
    Recall(context.Context, Query) (Context, error)
}
func ValidateScope(Scope) error
func (c Context) Validate() error
```

上述类型属于 `knowledge`，不依赖 Brain、persona 或 memory；Scope 复用现有范围语义，不为 K0 重构整个仓库的 Scope 类型。`Revision` 是排序后的相对文件名及实际读取内容形成的 SHA256；不是人格版本或目录修改时间。`Item.Text` 由已审核 summary/usage 组成，不含关键词/本机路径等无关元数据。

- [ ] 在 `internal/knowledge/validate_test.go` 编写有效空上下文、非法 Channel、空 Session、公开含 private、跨私密会话、DM伪公开、缺 Revision/Source、重复ID、非法优先级、无效UTF-8用例。
- [ ] `go test ./internal/knowledge -count=1`，先记录类型/行为未实现失败，再实现 `types.go/validate.go`；预期全部通过。
- [ ] 新增 `ContextInput.Knowledge` 及知识输入检查；旧调用方继续可传 nil。新增公开/私密反例，断言 Provider 调用次数始终为0。
- [ ] `go test ./internal/knowledge ./internal/brain -count=1`；预期旧 Memory/Shared Scope 测试保持通过。记录接口影响交 DSH 复核。

### K0-2：文件快照与格式加载

- [ ] 在 `file_test.go` 写入第3.1节完整有效样例，再分别测试空entries、BOM/CRLF、未知字段/重复键/多文档、重复ID、缺usage、非法场景、未知schema、超限、链接逃逸及取消。
- [ ] `go test ./internal/knowledge -run 'TestLoad|TestFile' -count=1`；先记录失败，再完成 `file.go`。复用现有 YAML v3，I/O错误带 `%w`，错误不回显正文。
- [ ] 实现 `NewFileReader(ctx context.Context, dir string) (*FileReader, error)` 与只读快照；启动或显式重建Reader时加载，不增加文件监听、全局缓存或后台goroutine。
- [ ] 在临时目录改变知识文件验证 Revision 变化；同一文件顺序/同一内容重复加载结果一致，调用方修改返回slice不污染快照。
- [ ] `go test ./internal/knowledge -count=1` 全绿后交 DSH 检查格式、权限和资源边界。

### K0-3：确定性召回与权限

- [ ] 先建立以下表驱动测试，再实现 `recall.go`。

| 用例 | 输入与资料 | 必须结果 |
|---|---|---|
| 标题优先 | title=甲乙丙的条目priority=1；只命中同词标签的条目priority=100 | 标题命中排前 |
| 稳定排序 | 匹配强度/长度/数量/priority都相同 | ID升序，重复运行一致 |
| 不重复计分 | 同一词在消息出现多次 | 不挤掉更具体命中 |
| 空和无关输入 | 空文本、标点、完全无命中 | 零条，无随机补梗 |
| 英文边界 | 词cat，输入concatenate | 不作为英文独立词命中 |
| 公开隔离 | live查询，含private资料及误标公开DM来源 | 不返回；非法源数据在加载/校验阶段拒绝 |
| 私聊隔离 | dm会话A查询，私密来源为dm会话B或qq会话A | 不返回 |
| QQ区分 | 群查询，条目仅声明qq_private | 不返回 |
| 上限 | 五条可用命中 | 只返回排序前三条，Context Scope/Revision正确 |
| 取消 | 查询前或长扫描期间取消ctx | 返回可判定取消错误，不把失败当空命中 |

- [ ] 运行 `go test ./internal/knowledge -run TestRecall -count=1`，保存先红后绿结果；召回代码只处理已加载快照，无网络/数据库调用。
- [ ] 由测试验证先过滤再匹配，审计只记录允许返回的ID，不暴露被过滤条目标题或命中数。

### K0-4：梗库只读迁移与首批500条

源链路已核实：`meme-ctx.mjs` → `meme.mjs` → `~/.dsh/meme-wiki/memes.db`。包装器会截短 usage、忽略其他字段，并写查询诊断日志，因此迁移不执行包装器。

写作时源库统计为 memes=6660、custom=172、同名重叠15、唯一标题约6817；内置42条缺usage。这里只是源清点，开工时须重核并冻结源快照，不将约7000写成已迁移数量。

- [ ] 在 `scripts/test_export_memes.py` 用临时SQLite源构造同名custom覆盖、空usage、换行/引号/Unicode、完整附加字段、重复导出、目标人工改动等用例。
- [ ] 运行 `python -m unittest discover -s scripts -p test_export_memes.py`；先记录失败，再实现标准库离线导出。SQLite连接使用只读URI，并在一个读取事务中获取原始数据。
- [ ] 原始逐行JSONL及全部源字段保存在被忽略的 `data/knowledge-import/<snapshot-id>/`；记录行数、源内容哈希、导出器版本。以这个一致快照作可复现来源，不依赖导出时仍在变动的源数据库文件哈希。
- [ ] 生成不含私密原文的manifest、冲突/缺接法清单和固定选词清单；审计目录使用全新 `plans/reports/knowledge/<batch-id>/`，独占创建。
- [ ] 先筛除缺接法、未经审核或违背人格硬规则的记录；从合格记录按来源层及标签分层选择500条，固定seed和ID清单。不按35条用例答案挑选，不用LLM补齐接法。
- [ ] 同名激活条目custom优先；两层原始数据均保留。源title/usage/summary/tags与附加字段归档不丢，激活文件只作明确字段映射及经记录的审核处理。
- [ ] 稳定序列化生成 `memes.md`；重复执行同一快照和选词清单，输出字节完全相同。目标内容相同则成功无改动，不同则报冲突，不覆盖人工编辑；新批次创建新文件。
- [ ] 将公开lore和viewer资料来源单独审核；缺真实viewer资料不造数据。`games.md` 使用空entries。
- [ ] 再运行Python测试和 `go test ./internal/knowledge -count=1`；核对500条激活ID、原词/接法往返、缺接法清单、原始总行数与冲突处理统计。

源数据归档可能含不适合入仓的资料，原始归档默认不提交；公开审核后的条目与脱敏manifest才可纳入交付。原始归档不受prompt/日志打印，不通过日志证明“不丢数据”，使用哈希与数量对账。

### K0-5：Brain最终预算与动态资料注入

- [ ] 在 `internal/brain/knowledge_test.go` 先覆盖以下用例，全部使用fake Provider/Usage，无真实模型。

| 用例 | 必须断言 |
|---|---|
| K0关闭 | Knowledge=nil时消息与P0-A正式基线相同，不读知识文件 |
| K0空命中 | 空Items合法，Scope/Revision仍正确；完整人格保留 |
| 静态边界 | 知识不进入system/instructions；相同Pack的system SHA一致 |
| 预算百分比 | 实际序列化增量不超过floor(B*r)，测试默认10%及硬上限10% |
| 转义开销 | 引号、换行、HTML结束标签经JSON转义后仍计入预算 |
| 共享余量 | 记忆占满剩余预算时知识为零，不反过来裁记忆或人格 |
| 整条舍弃 | 长条目放不下时不截正文，后续可容纳的短条目可保留 |
| 人格不够 | K0启用且完整人格+事件超过B时返回budget，Provider零调用 |
| 范围/资源 | Scope错配、越界私密、4096项或4MiB超限均零调用 |
| 只读性 | 裁剪不修改传入Pack/Memory/Shared/Knowledge与Reader快照 |

- [ ] 运行 `go test ./internal/brain -run 'TestKnowledge|TestReply' -count=1`，记录新增测试失败，再完成序列化与整条选择；最终完整请求重新estimate确认不超过B。
- [ ] 使用整数百分比；Brain Options新增 `KnowledgeBudgetPercent`，默认10，合法0–10。字段只控制启用的知识段，不改变全局重试/并发/时限。
- [ ] 开启知识时同时记录安全的预算审计：Revision、候选/实际注入ID、裁掉ID及估算开销；不记录正文或完整prompt。Reader候选不能冒充实际注入列表。
- [ ] `go test ./internal/brain ./internal/knowledge -count=1`，随后执行第8节门禁及真实PG组合测试。

### K0-6：配置、装配与回归证据

目标配置片段如下；新增 `config.Knowledge` 与 `Config.Knowledge`。关闭时不访问dir，启用后缺目录/坏文件为明确错误。

```toml
[knowledge]
enabled = false
dir = "personas/dafeiyu/knowledge"
budget_percent = 10
```

- [ ] 配置测试先覆盖默认关闭、未知字段、负数/11%拒绝、0%合法、坏目录关闭不读/启用报错，再实现加载校验；不增加凭据环境变量。
- [ ] 装配方构造并注入 `knowledge.Reader`；Brain不自行查文件，scheduler/handler不访问SQL。接入届时已真实实现的worker；若尚无业务消费路径，只完成明确诊断/评估装配，不声称上线可用。
- [ ] `fishd check-brain` 增加启用状态、Revision及加载条数等安全诊断；保持Provider调用0、不写虚假用量。完整当前事件的预算仍须逐请求验证，诊断成功不代表所有事件都能容纳。
- [ ] `evalGenerator` 启用时先用可信Scope和Input.Text召回，再传入Brain；关闭时不加载。Fixture冻结实际使用知识文件的相对路径、原始字节hash和Revision；记录加载器/召回器/预算代码hash及配置。
- [ ] 每条新评估保存候选及实际注入ID、知识预算增量、最终prompt估算；运行前冻结整个快照，期间源文件变动不能替换该run的内容。与旧Fixture不同的知识审计为契约加法，旧run不补写字段。
- [ ] 测试确保生成器只收到输入正文和Scope，不收到expect/forbid/审计注释；关闭模式与空命中不同，读取失败不能静默降级成关闭。
- [ ] `go test ./internal/config ./cmd/fishd ./cmd/fishctl ./internal/personaeval -count=1`，另跑真实PG组合路径，交DSH独立复核。

### K0-7：质量、对照与扩量

- [ ] 在 `plans/reports/knowledge/<eval-id>/` 冻结100条检索查询：60条有已审核预期知识、20条无命中、20条权限/通道反例；独立于35条人格集，输入不带评分答案。
- [ ] 人工相关率按实际返回条目计算，至少80%；60条正例至少48条返回一项预期相关知识；20条负例最多2条误召回；20条权限反例必须零越界。权限过滤掉的正确行为不算召回失败。
- [ ] 返回零条不能以“无错误结果”宣称相关率100%；无返回时相关率记不可计算，正例覆盖门槛仍必须通过。重复条目不重复计数。
- [ ] 固定同一人格候选、代码、知识快照、原35条、模型与参数，分别K0关/开各一次35条；两个新run分开，不把历史57.1%当同期严格A/B。未明确进入真实对照步骤前仅跑离线检索测试。
- [ ] 报告原始/投递分数、每场景结果、硬违规、检索相关率/覆盖、实际输入输出usage、未知费用NULL；单批差异不宣称统计显著，不承诺补到90%。
- [ ] DSH独立重算评分、核对知识引用/实际注入/预算、检查真实PG账本和源快照。原始输出发生硬违规不能靠投递截断或人工旁评抹去。
- [ ] 退出标准逐条通过后，再按固定清单生成后续批次；每批复用解析/权限/预算测试及检索质量抽查。每次改变知识影响模型上下文时跑对应人格回归并保留新run，不把首次500条通过当全部源库通过。

## 7. K1/K2只记录后续边界

K1在P2实现 `knowledge_entities/knowledge_facts/knowledge_links`，参数化repository查询、来源权重、有效版本及审计。无pgvector时，标签/实体/关键词查询仍须独立通过验收。复用记忆来源权重语义，不把知识事实写进memory_records，也不直接挪用聊天事实作为公开知识。

K2在P2按需增加可配置embedding、pgvector混合检索及可选rerank；启用前先按通道/私密会话过滤候选，再对允许内容评分或发送给外部reranker。需有去重、文档切分、版本、过期与重建策略。默认关闭，故障和效果不达标都不能悄悄改为全库注入。

本轮不建上述表、向量索引、embedding调用、rerank客户端或空worker；P2重新制定独立实施计划。

## 8. 开发与独立验收门禁

每个代码任务按新增失败测试 → 最小实现 → 对应测试 → 全仓验证 → 确定快照 → DSH独立验收推进；任务状态与Phase退出分开。纯文档任务只做链接、规则一致性和差异检查，不制造功能测试。

代码交付必须完成：

```powershell
gofmt -l .
go vet ./...
go test ./... -count=1
go test -tags=integration ./... -count=1
go build ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
go test -race ./... -count=1
git diff --check
```

gofmt预期无输出、其余命令成功退出；lint固定项目版本。race在有可信64位C工具链的环境跑；如果改用WSL/CI，明确环境和实际结果，不能用普通test代替。真实PG测试继续使用随机隔离schema，不能清空共享业务库。

K0无业务数据库表，但仍需在 `internal/database/usage_integration_test.go` 验证假HTTP → 加载公开知识 → Brain → 真实PG的成功、无知识、预算拒绝/权限拒绝零调用及零虚假账本。实际调用后取消的有界记账、重试次数和费用NULL语义继续保持。

交接写明实际修改路径、基线/文件hash、契约影响、命令/结果、未执行项及原因、写入者是否暂停。DSH自己写验收，不复制开发自测充数。仅Codex做Git写操作，验收覆盖的明确路径单独暂存；个人项目直接push当前主分支，不建PR，不夹带其他人的未验收文件。

## 9. 证据保护、成本与执行检查表

永久保留两轮真实目录及所有离线重评分目录：

- `plans/reports/persona-eval/20261031T105248Z-501d0282/`：原v1 15/35=42.9%。
- `plans/reports/persona-eval/20261031T121041Z-bcd11c0e/`：v2主分20/35=57.1%，同输出v1为19/35。
- `plans/reports/persona-eval-rescore/` 的候选与-final目录、v1对照目录，以及两轮manifest：不重写、不补字段、不复用输出目录。
- 已有0.7c设计、A0审计及DSH/WorkBuddy报告不因本计划被覆盖或代填。

新run开始前保存旧证据树哈希清单，结束后对比不变。当前已有说明里“17个失败标签”是历史汇总误记；正确为15条失败、16个标签，其中“喊主人”2条公开、1条QQ私聊。新文档如实说明，不回写旧证据修分。

预计投入：契约/规范约0.5日，P0场景或人格子实验各约0.5–1日，K0加载/召回/首批迁移约1–2日，装配与独立验收约1–2日；属于工作量估算，不是交付时限。内容审核、DSH复核和新发现问题可能增加投入。

K0运行时不增加模型调用；知识会增加prompt输入，不能称免费。P0-A/B/H各自一次35条，K0正式关/开对照为70条；只执行实际进入的步骤，不预先承诺全部批次。Provider失败沿用最多3次总尝试，不因分数低重试。使用已有单批实测227871输入/393输出tokens仅作量级参考，不把新知识输入、缓存或不同模型费用按旧数据伪造；未配置可靠报价时费用为NULL。

| 序号 | 任务 | 状态 | 完成记录要求 |
|---|---|---|---|
| P0-A0 | 意图/素材/标准审计确认 | [ ] | 引用现有审计与DSH独立结论；零调用 |
| P0-A | 场景装配单因素实验 | [ ] 待核对并行产物 | 先核对20261031T124251Z-43a917e1及源码，不重复调用；再登记验收 |
| P0-B/H | 按A结果整理候选及独立新用例 | [ ] | 精确编辑清单、候选版本、原集/新集分别验收 |
| G2 | P0.5放行 | [ ] | 全部P0退出证据，或主人明确接受未满足项的记录 |
| K0-1 | 契约/类型/范围 | [ ] | 正式契约版本及零调用反例 |
| K0-2 | 文件快照/解析 | [ ] | 严格解析、限额、取消及Revision结果 |
| K0-3 | 确定性召回 | [ ] | 排序、空命中、ACL与Top3结果 |
| K0-4 | 首批500条迁移 | [ ] | 原始归档、缺接法清单、500条ID、可重复输出 |
| K0-5 | Brain预算/注入 | [ ] | 完整人格、知识<=10%、总预算与实际注入审计；开发结果见K0-5执行单，DSH维护验收状态 |
| K0-6 | 配置/装配/评估 | [ ] | 默认关闭、无调用诊断、快照和真实PG结果 |
| K0-7 | 质量/对照/独立验收 | [ ] | 100条检索指标、正式对照及DSH结论 |
| G3 | 分批扩量 | [ ] | 每批来源/数量/质量及回归，不虚报全量完成 |

待定项只收录执行时新发现的产品/契约冲突，并注明影响任务、负责人和放行依据；当前已裁决的分层、显式Knowledge、<=10%预算及K1/K2阶段边界不得重新当作自由选项。P0-A0/A历史产物不重复制造；当前推进以最新放行单为准。

## DSH 同步记录（2026-10-31）

- 正式契约已建：`contracts/knowledge.md`（P05-KNOWLEDGE-1），依据 `plans/phase-0.5-contract-decision.md`。
- 已写入契约：keywords 同等级规则、Revision 算法、必需根文件规则、Scope/私密隔离、Top3 与排序、10%/0–15% 预算、审计增量。
- 场景装配实验（`20261031T124251Z-43a917e1`，13/35=37.1%）归**主对话**负责；DSH 已独立复核并建议回滚，本计划不重复执行。
- `contracts/brain.md` 的 Knowledge 字段增补**暂缓**：按裁决需先与主对话确认可写窗口（主对话正在处理场景回滚），回滚完成后由 DSH 同步。
- `contracts/persona-eval.md` 的知识审计加法待 brain.md 同步后一并落地。
