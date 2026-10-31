# Codex 工作报告

> Codex 追加，DSH 验收。时间使用 Asia/Shanghai；不写凭据或完整 prompt。

## 2026-10-31 · 协作规则审查 / COMM-001

- 身份：Codex 首席开发/技术裁决；DSH 独立验收和会话辅助；WorkBuddy 轻量开发。
- 基线：起点 50afcac；审查中其他会话继续提交，已重新读取新 assignment/prompts，保留其改动。
- 变更：新增协作裁决与看板、三方报告；修订必要入口/提示词，登记未解决设计点。
- 边界：仅文档，不实现 0.1、不改既有 ADR、不真实收发；本 Codex 会话未提交/push。期间其他会话已提交部分本轮文件，不能将当前 HEAD 当作本会话的提交或完整验收快照。
- 验证：`git diff --check` 退出码 0；Python 内存检查读取 57 个 Markdown、核对 28 个本地链接，角色、入口引用、P0-G01–06、待确认回执和旧提示词关键冲突检查通过；既有 ADR 无差异。
- 未执行：Go/PG/模型/真实通道功能测试，本轮无功能实现且仓库仍无 go.mod；本结果仅为开发者文档自检，不等于 DSH 独立验收。
- 交接：已读取 DSH 的 COMM-001 已读回执及观察报告；DSH 已接手验收与状态维护，向 WorkBuddy 转达仍待完成。WorkBuddy 本人回执未收到。
- 写入状态：本轮修订完成，暂停写入，交 DSH 验收；未操作并行会话新增的 personas/ 或 .probe.txt。

## 后续交接模板

- 任务/子任务、基线/快照、获派与实际修改路径：
- 实际变更与接口影响：
- 测试命令与结果；未执行项及原因：
- 风险/阻塞/技术裁决：
- 交给谁、是否已暂停写入：

## COMM-002 · 给 DSH 与 WorkBuddy 的技术裁决

- 回应 DSH 报告：`agents/` 与 plans/reports 重叠，保留现有历史，只作兼容导航；任务状态只维护 coordination，每人只追加自己的 reports，不写别人 INBOX。请 DSH 将辅助页同步为导航，避免双份状态漂移。已在 agents/PROTOCOL 顶部发布兼容说明。
- 已观察到提前出现的 internal/persona/*.go。全部保留为**待审草稿**，不视为 0.7b 交付或验收通过；请 DSH 转达 WorkBuddy 暂停扩写/提交，确认正在写入的路径并在本人报告回执。待 0.1 与 Pack/Brain 接口确认后再逐文件评审接入，不删除或覆盖草稿。
- qq_private/qq_group 可作为人格的场景键候选，不能替换 Event.Channel 的 qq，也不代表 QQ 通道提前实现；meta 声明与映射须在 P0-G04 中统一确认。
- 现有设计未要求 soul/rules/samples 至少 200 字、context 至少 50 字；不接受把这些篇幅阈值自行定为产品验收规则。加载器先以必需文件/字段、非空和格式有效为基线；额外限制需先提设计变更。
- 用 baseline 回放验证回归流程可以保留为测试方案，但不证明真实模型人格通过率；目前未编译或运行这些草稿，没有对功能作通过结论。
- Git 写操作从本通知起仍统一 Codex；已观察到其他会话在本轮期间提交和暂存删除，不撤销历史/索引，只请 DSH 确认停止进一步 Git 写入后再开启集成窗口。
- 本裁决已发布到仓库，DSH/WorkBuddy 对 COMM-002 的回执仍待本人填写。本轮 Codex 后续只做最终文档检查，随后暂停写入，交 DSH 独立验收。

## COMM-003 · 0.7a 接收、V 项裁决与提交交接（2026-10-31）

依据：主人转交 0.7a 独立验收通过，证据见 DSH 报告 15:30 条目；已读取 WorkBuddy 本人的 COMM-001 已读/暂停写入回执。此次只集成人格素材与用例，不放行 0.7b/0.7c 或其他功能开发。

| 编号 | 最终裁决 | 后续责任与边界 |
|---|---|---|
| V-01 | 保留 internal/persona/ 的 7 个 Go 文件为未接入预研草稿，不删除、不暂存、不随 0.7a 提交 | 0.1 验收与 Pack/Brain 接口确认后，由 Codex 逐文件评审；WorkBuddy 仅按重新派发修订，不继续扩写。现有篇幅硬阈值不作为验收标准 |
| V-03 | 未迁移「当前状态」段是正确处理。余额/礼物流水属运行时事实，经核实来源后进入 Memory.SharedState/最小记忆，必须可知来源与时效；行为频率/额度等策略属配置 | 0.9 处理必要状态接入，0.1 仅定义当阶段必要配置；不把历史「约90」当作实时余额、不把「每15分钟送礼」变成 P0 自动送礼功能。无法核实的状态留未知，原账本不因人格迁移失效 |
| V-04 | 0.7a 只验素材/结构/用例。baseline 35/35 是规则自洽证据，不是模型人格通过率；完整 0.7 仍未完成 | 0.6 后由 Codex 接入实际目标模型，DSH 独立执行/复核 0.7c，记录 provider:model、人格版本、用例快照、实际输出与通过率，再判定 ≥90% |
| V-05 | 0.7b 采用维护中的 go.yaml.in/yaml/v3，替换自写 YAML 子集解析器；不为避开依赖审批自行发明 YAML 方言 | 已执行 go list -m -versions，确认可用到 v3.0.5；由 Codex 在 Go module 就绪且加载器实际接入时加入/锁定并复核版本，使用结构化解码与字段校验。本次不新增 go.mod/go.sum，不改已验收 meta 内容；其旧「不引入 YAML 库」注释到 0.7b 一并替换 |

V-02 的 QQ 素材路径由 DSH 修正为 ~/.dsh/persona-qq.md，纳入本次配套文档。qq_private/qq_group 是人格口径键，Event.Channel 仍只有 qq；保留后续通道口径数据不等于实现后续通道。

### 提交前复核

- 将 WorkBuddy 报告中的 14 份 SHA256 前缀及字节数与工作区重算核对，全部一致；其中 7 份纯数据属于接收范围，7 份 Go 草稿排除。
- 对 cases.jsonl 独立解析，35 个唯一 ID，expect/forbid 非空；5 种口径 × 5 类情境全部有覆盖；meta channels 与 context 五段对齐；35 个 baseline 段与用例 ID 一一对应。
- 核查已迁移稳定人格中未混入旧余额/每15分钟礼物策略/账本路径；.probe.txt 已不存在，删除已暂存；.workbuddy/ 与探针忽略规则有效。
- DSH 的 Go 检查发生在临时模块，不能表述成主仓库 go test/vet 已通过；本次接收纯素材与用例，不接收其 Go 草稿。主仓库仍无 go.mod，未宣称仓库功能门禁通过。
- 本次提交范围：personas/dafeiyu/（只有 eval 两文件为新增差异）、.gitignore、.probe.txt 删除、docs/08-persona.md、三方交接报告和 DSH 已验收的 coordination 状态、变更教学说明；不夹带其他待验收协作文案或 internal/persona/。
- 基准内容中的后续通道口径、当前能力描述与规则冲突在 0.7b/0.7c 随接口和实际模型验证；本次保存迁移素材，不宣布其为运行中 prompt 或模型验收结果。
- 通知 DSH：请在收到本轮提交号后核对接收文件快照、将 0.7a「待提交」改为实际提交记录；Phase 0.7 的 CLI/真实回归保持未完成。WorkBuddy 继续暂停，待 0.1 和接口放行。

## COMM-004 · 0.1 基础设施交付（2026-10-31，America/Los_Angeles）

主人已明确派发0.1，并提供本机PG17/raisemyfish及.env配置。本轮Codex实现与自测完成，**待DSH独立验收，未提交/push**；0.7b与后续阶段未放行。基线为32651ba。

### 接收范围与设计

- 新增go.mod/go.sum（RaiseMyFish，最低Go1.25）、config/fishd.toml、internal/config、internal/database、internal/infra、migrations、cmd/fishd与cmd/fishctl，以及对应测试。
- TOML/.env/环境覆盖和校验；PG17/GORM/pgx连接及关闭；zap JSON双输出、按天滚动保留7天；本机原子状态文件与时效判断；PG连接持有的单实例锁；取消/故障退出。
- 显式fishd migrate up，普通启动不隐式迁移；7业务表、6条来源字典、schema_migrations账本、SHA256检查、事务锁和事务回滚。不自动down，不修改已存在业务数据，不引入恢复字段/业务repository来抢跑0.2。
- fishctl status输出PG/迁移与心跳摘要并用退出码表示健康；tail从当日日志开头跟随追加、跨日切换、支持取消，不依赖活数据库连接。
- 修正CI job级hashFiles、lint v2配置/action版本并启用PG17集成job；Go1.27/lint2.15.0固定。README、数据模型、部署/测试文档同步实现边界。
- 实施计划：plans/phase-0.1-implementation.md；教学说明：docs/update/2026-10-31-0116-基础设施配置日志与迁移.md。

### 快照与Git边界

- plans/reports/phase-0.1-manifest.json记录本轮接收路径、字节数和完整SHA256；不含.env、密钥、DSN、internal/persona草稿、其他Agent报告/提示词改动。
- 最终逐项复核31个交付文件哈希/大小；本轮共21个测试函数（含4个真实PG集成测试），凭据值未出现在交付文件/运行日志，文档链接有效，既有ADR无差异。失败的编译器下载残包已清理。
- internal/persona/七份草稿与WorkBuddy原报告哈希逐一核对，字节级未变；入口不import该包。全仓本地测试会编译到草稿，但不构成0.7b接收，不随本轮提交。
- agents/PROTOCOL、assignment、coordination、DSH报告、prompts等已有未提交改动保留；后续集成只暂存独立验收通过的精确路径，不执行git add .。

### 实际验证

| 检查 | 命令/证据 | 结果 |
|---|---|---|
| 单测 | Windows go test ./... -count=1 | 通过 |
| 真实PG集成 | Windows go test -tags=integration ./... -count=1 | 通过；迁移/服务/锁测试均用随机独立schema并清理 |
| 迁移负向路径 | 并发、重复、外键/索引/seed、checksum篡改、同名表冲突事务回滚 | 通过 |
| 静态/构建 | go vet ./...；go build ./...；fishd/fishctl -trimpath构建 | 通过 |
| lint | go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run --build-tags integration ./cmd/... ./internal/config/... ./internal/database/... ./internal/infra/... ./migrations/... | 0 issues；范围不包括待审persona草稿 |
| CI语法 | go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.9 .github/workflows/ci.yml | 通过；未声称远端job已运行 |
| 格式/差异 | gofmt -l .；git diff --check | 无输出 |
| 竞态 | WSL Ubuntu22.04、Go1.25、CGO_ENABLED=1，go test -race ./... -count=1 | 通过 |
| 目标库 | go run ./cmd/fishd migrate up 连续两次 | 版本1就绪，无重复建表或seed |
| 编译后二进制smoke | 真实PG启动、status健康、重复fishd拒绝、tail跟随；向隐藏测试控制台发送Windows Ctrl+C | 通过；fishd退出0、状态stopped，未留下运行服务 |

数据库集成测试没有清空public，测试随机schema名使用pgx.Identifier安全引用；数据库连接失败或缺测试配置直接失败，不静默skip。根目录凭据未回显/提交。服务不进行B站收发，不调用LLM。

### 环境限制与额外环境操作

- Windows PATH现有gcc是mingw32，原生 go test -race 因64位模式不可用失败，不能当作通过。尝试下载官方llvm-mingw发布包超时，未执行/安装该包。
- 使用现有WSL Ubuntu的Go1.25完成race，为此通过Ubuntu仓库安装gcc/libc6-dev测试依赖，apt同步更新libc依赖；没有修改Windows PATH、PG监听或防火墙。
- WSL直连Windows localhost PG失败，因此本机组合 go test -race -tags=integration 没有通过；已有Windows真实PG集成与Linux全仓race分开通过，CI已配置Linux+PG17组合，需要DSH/CI实际确认，不能表述为全部环境组合均已通过。
- 容器/Compose、业务通道、LLM配置、repository/事件恢复仍不属于本任务；没有更改docs/14-decisions.md既有ADR。

### 给DSH与WorkBuddy

DSH：请先验证manifest哈希和上表命令，按快照独立验收；重点复核目标库建表、重复迁移、配置脱敏、信号退出，以及CI组合环境的未验证项。收到后在本人报告写COMM-004回执，将0.1看板从待派发更新为待验收/实际结果，验收后再勾Phase 0.1任务。不把此自测记录直接当成独立验收通过。

WorkBuddy：继续保持persona草稿暂停，0.1验收和Pack接口基线确认后再接0.7b，本轮不扩写/不提交该目录。

Codex：本轮写入结束，交付文件以manifest为准；只剩报告/证据校验，不提交或push未独立验收的功能。

## COMM-005 · 0.1集成与P0-PACK-1放行（2026-10-31，America/Los_Angeles）

依据主人的本轮指令与DSH COMM-004验收：0.1原31文件快照复核全部一致；重新跑单测、真实PG集成、vet、build、带integration标签的本轮lint均通过。原样集成到 **364dcb3** 并直接push master，未包含internal/persona草稿、.env或其他待验收协作文案。

### 强杀观察项裁决

不在已验收0.1中追加单独PID检测。当前service_alive结合数据库/迁移、状态和心跳时效，强杀后默认最多20秒可能仍报告在线；超过配置阈值后必须为false。service.state是最后一次写入的原始记录，强杀不会自动把该字段改成stopped，不能单看它判存活。

原因：只查PID存在不足以证明它仍是同一fishd实例，PID复用会造成另一种误判。将即时身份检测作为0.11增强（P0-G07），后续同时考虑创建身份/心跳/锁信号；该增强设计尚未实现。README补充现有语义，0.1运行时代码与迁移保持验收快照不变。

### Pack接口基线与依赖

- 基线ID：**P0-PACK-1**；真源contracts/persona.md，任务范围plans/phase-0.7b-dispatch.md。基线版本是同时包含两文件的后续Git提交号，不使用未提交草稿的导出符号作为真源。
- 固定Meta与Pack字段，Load(ctx,dir)返回Pack值；纯ContextKeys/ContextKey/ContextFor/SystemPrompt接口；无全局可变注册表。
- 元数据解析使用 **go.yaml.in/yaml/v3 v3.0.5**，Codex已统一加入go.mod/go.sum；尚无正式加载器import，当前可能标注indirect，WorkBuddy不得自行tidy删除/改动。后续接收加载器时由Codex统一整理依赖。
- 仅加载五份静态文件；拒绝YAML未知/重复字段、多文档、非法类型，短的非空文本有效；移除草稿200/50字门槛。场景键不扩展Event.Channel，不承担查询层ACL。
- 所有动态上下文与user拼装留Brain；CLI、回归生成器/评分/真实模型留0.7c/0.6。本轮没有实现或接收0.7b代码，0.7a人格内容未重写。
- 此依赖/文档提交是0.1验收后的单独基线准备；phase-0.1-manifest是364dcb3的历史验收快照，不更新或覆盖其原始hash来掩盖新依赖变化。

### WorkBuddy放行

0.1已验收并推送，静态Pack基线已确认，**放行0.7b**。获派路径、旧回归草稿留存方式、测试和交接详见放行单；特别是internal/persona/check.go、evalcase.go、runner.go、eval_test.go只允许原样移至指定忽略点目录并记录哈希，不随0.7b提交。不得改cmd、infra、database、依赖或已验收人设正文/用例；仅可删除meta.yaml过时解析器注释。

DSH：请按本轮授权转达COMM-005和放行单，收取WorkBuddy本人基线/接手回执，之后对加载器快照独立验收。Codex仅依据已确认COMM-004同步0.1勾选与本次放行状态，常规看板维护继续交给DSH。0.7c真实模型通过率仍待0.6，Memory接口仍待后续裁决，不因本次Pack冻结宣称全部上下文接口已定。

通知状态：已发布仓库，尚无WorkBuddy的COMM-005接手确认，不宣称已唤醒其应用会话。基线提交号以git log中包含P0-PACK-1与放行单的同一提交为准。

本次基线发布自检：新增依赖后go test ./...、真实PG integration、vet、build与go mod verify通过；13个本地文档链接有效，Pack声明与Brain的persona.Pack字段一致；364dcb3内31份历史快照经Git行尾归一化核对无漂移；7份persona Go草稿仍为原始哈希；既有ADR未改。本次仅新增获主人授权的接口裁决/任务说明及依赖准备，加载器功能仍由WorkBuddy开发、DSH验收。

## COMM-006 · 0.7b接收与三项裁决（2026-10-31，America/Los_Angeles）

主人已转交DSH「0.7b独立验收通过」并要求裁决后提交。本轮开始前复算WorkBuddy交接的六文件哈希与字节数，全部一致；原始五个Go文件与meta注释清理确属其已停止写入的交付。

1. yaml/v3已由Codex执行go mod tidy整理为直接依赖，版本保持go.yaml.in/yaml/v3 v3.0.5。
2. 接受`## live 补`等混合标题作为当前场景正文，只有独立ASCII键标题切换场景；已澄清persona契约并添加测试。不能用混合标题来新增场景，漏段仍须报错。
3. 不接受正文被无意裁剪。根因是splitContexts.flush的TrimSpace与遍历时统一补换行，ContextFor本身没有TrimSpace。修正为TrimSpace仅判空，按保留行尾的方式切分，不改正文空白/首行缩进/末尾有无换行；SystemPrompt规则不变。

Codex增量涉及pack.go、context_test.go、pack_test.go及契约澄清。先运行三个新增测试实际复现失败（首行缩进/首尾空白/换行丢失），修正后通过。DSH的既有通过结论对应WorkBuddy原快照，不冒称DSH已复验本轮补丁；请DSH收到提交后复核新增的正文边界测试。此补丁按主人本轮「待裁决并提交」授权集成。

WorkBuddy原报告中未严格先红后绿的流程偏差保留原记录，不把已有通过测试追认为TDD；本轮Codex的增量单独记录红→绿。后续开发继续要求先写失败测试。

原0.7c的四份Go草稿继续留在.workbuddy/persona-eval-draft-comm-005，不接收、不回迁；本轮不改已验收人格正文和eval数据。教学说明见docs/update/2026-10-31-0203-人格加载器验收与正文边界.md。

补丁后验证：go test ./...、真实PG integration、go vet ./...、go build ./...、带integration标签的全仓golangci-lint（0 issues）、go mod verify，以及WSL Go1.25/CGO_ENABLED=1全仓race均通过。新增隐私/Memory/Brain代码尚未实现，不把本轮验收扩大到0.6或0.9。phase-0.7b-manifest.json同时记录原六文件与集成后哈希，明确Codex改动了哪三份源/测试文件。

提交结果：**5ece97f已推送master**，未包含.workbuddy草稿、.env及其他待验收协作文案。GitHub默认地址连接失败，临时指定github.com已验证TLS的可用官方地址后完成fetch/快进核对/正常push；未修改系统DNS、Git全局配置或跳过证书校验。

## COMM-007 · P0-MEMORY-1与0.6 Brain放行（2026-10-31，America/Los_Angeles）

0.7b已接收提交5ece97f。按主人明确指令，确认contracts/memory.md为 **P0-MEMORY-1**，并发布plans/phase-0.6-dispatch.md；基线以同时包含这两份文件的Git提交定位。

最小类型为Scope、Message、Record、Fact、Context、SharedState及Reader；导出纯范围校验，依赖event.md既有枚举，不引入实体、向量或事实版本化。ContextInput继续使用memory.Context/SharedState，与Brain契约一致，不用任意map/JSON承载未定义上下文。

Scope必须来自可信目标Event，和Task、Event、两份上下文一致；Recent限定同会话，CrossChannel只含其他通道公开消息，私密Record/Fact只限同通道同会话。公开查询强制NOT private，实际SQL/有效记录过滤仍须repository真实PG验收；Brain的额外校验不替代查询隔离。空记忆必须带有效Scope，查询失败返回error，不能悄悄换成空记忆。

**放行0.6开发，负责人Codex，DSH独立验收。** 主人授权P0内调整顺序：0.6可补自身必需UsageRepo最小接口/GORM写入与真实PG测试，不等待全部0.2完成，不勾选其余repository。允许只定义Memory DTO/纯校验和Event既有类型供编译；Memory Reader真实召回/提炼仍留0.9。本轮发布契约和任务，不实施Brain、不放行0.7c真实模型评测。

DSH请用新基线/放行单协助下一次Codex开发会话，收到后写本人COMM-007回执；WorkBuddy此次任务交付后暂停，等待下一项轻量任务，不自行恢复回归草稿。0.6仍须先补齐Provider/Usage的必要类型和配置契约、按失败测试推进，再完成HTTP/预算/重试/记账；不得把预留工具字段变成P6执行器。

契约与派发中的权限/文件范围按主人的本轮授权发布；加载器补丁和后续Brain功能仍分别保留实际验证与DSH复核记录。未修改任何既有ADR，不宣称当前已具备召回、实际模型人格通过率或事件消费能力。

基线发布自检：17个本地链接有效，Memory六种数据类型/Reader/纯校验声明与Brain字段一致，未使用any或不透明JSON；0.7b集成哈希保持不变、四份延后草稿留存，尚无internal/memory或internal/brain实现；单测/vet/build/依赖校验再次通过，格式和diff检查无输出。

## COMM-008 · 0.6接手与实施计划

已依据主人本轮派发接手0.6，基线05a0ee9/P0-MEMORY-1，已读放行单与Brain/Persona/Memory/Event契约。只写brain/provider、memory DTO/纯校验、event types、Usage切片、config/cmd接缝及对应技术文档/本报告；不修改DSH/WorkBuddy报告，不恢复0.7c，不改迁移或既有ADR。其他未提交协作文案保持原样。

编码前补contracts/brain.md的P0-BRAIN-1：固定单次Provider接口、统一最多3次调用、空回复最多补1次、未知用量/价格NULL、取消后的有界记账、保守UTF-8预算与字段/诊断边界。调用模型仅使用httptest/mock；本轮不使用真实LLM key或产生平台调用费用。

- [x] Event枚举与Memory DTO/范围校验：先写测试，未实现时编译红，再实现并通过。
- [x] store.UsageRepo/GORM切片：先写校验/真实PG测试，再实现；NULL统计、并发写入和取消路径通过。
- [x] 单次OpenAI兼容Provider：先写httptest，再实现；成功/失败/超时/取消/重定向/大小限制及有效usage保留通过。
- [x] Brain上下文/预算/隐私/重试/记账：先写测试再实现；审查新增行为红→绿证据见COMM-009，并发/取消路径有界。
- [x] config/路由与cmd诊断：先写测试再实现，保持通道关闭，真实CLI/PG无模型调用诊断通过。
- [x] 全仓单测/真实PG/vet/build/lint/Linux race通过，冻结快照与教学说明已交付；这是Codex实施进度，DSH未独立验收，不提交/push。

## COMM-009 · 0.6实现交付，待DSH独立验收

基线：05a0ee9/P0-MEMORY-1，P0-BRAIN-1按本轮授权细化。本人已完成实现、自测与内部只读代码审查，**尚未获得DSH对本轮的独立验收；未暂存、提交或push**。请DSH收到后写本人回执、更新看板为待验收/实际结论，勿直接转抄自测。

### 实际变更

- brain/provider：单次非流式HTTP、HTTPS/loopback端点、禁重定向、2MiB响应限制、安全typed error、usage/正文分层解码。
- brain：Task/ContextInput/Reply、Scope检查、只读排序/预算、统一3次尝试/空回复最多补1次、并发/总时间约束、逐次Usage记录、中文rune长度、可空汇总Meta；不做发送或查询SQL。
- memory/event：仅契约DTO、枚举与纯校验；dm误标公开/跨私密会话/未知Scope拒绝。没有Memory Reader真实实现、总线或状态迁移。
- store/database：仅UsageRepo，写既有llm_usage；没有改schema/迁移或完成0.2其余repository。
- config/cmd：可选启用、Provider/models/routing与按model报价、环境覆盖、重复路由复用Brain；fishd装配与check-brain无调用诊断。默认llm和所有通道仍关闭，版本摘要phase0.6-dev。
- README/对应技术文档与教学说明同步；不改personas/eval、DSH/WorkBuddy报告、既有ADR和迁移，不恢复.workbuddy的回归草稿。其他Agent已有未提交协作文案保留，不认领。

### 测试顺序与内部审查

各新模块先写测试文件到工作区，再运行并观察未实现符号的编译红，随后实现通过；这些属于新API引入的编译失败，不冒称全部都是运行期断言失败。可运行的行为红→绿单独记录：

1. 总/等槽DeadlineExceeded被误标不可重试，新增测试实际失败；已改为上层可用新Context重试，Canceled仍不可重试。
2. 正文结构错/unsupported tool_calls/content对象时会丢掉已返回的usage，新增测试实际失败；已先独立保留有效统计再拒绝正文。
3. 总时限在准备之后才开始且裁剪O(N²)，2000条记录用1ns总时限仍耗时约2.14秒并返回成功，新增测试实际失败；已将准备置于总Context/并发槽、限制4096条/引用和4MiB处理文本，使用可取消的按类别二分裁剪。

requesting-code-review技能要求的内部只读审查提出3项Important；修复后审查者确认代码层闭环，**它未运行测试、没有代替DSH验收**。测试由Codex独立执行结果如下。

### 可复验结果

| 命令/路径 | Codex结果 |
|---|---|
| go test ./... -count=1 | 通过 |
| go test -tags=integration ./... -count=1 | 真实PG通过，随机独立schema；含UsageRepo和假HTTP→现有人格→Brain→PG账本组合路径 |
| go vet ./...；go build ./...；fishd -trimpath构建 | 通过 |
| go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run --build-tags integration ./... | 0 issues |
| go mod verify；gofmt -l .；git diff --check | 依赖通过；格式/差异无输出 |
| WSL Ubuntu22.04/Go1.25，CGO_ENABLED=1 go test -race ./... -count=1 | 全仓通过；现有WSL配置警告与应用无关，未修改WSL配置 |
| 真实CLI/PG：使用临时loopback假端点配置运行fishd check-brain | 通过；装配dafeiyu和路由，provider_called=false、memory_reader=not_implemented，模型/用量调用为0 |
| go test -cover：brain / provider / memory / event / store | 80.5% / 90.9% / 91.7% / 100% / 90% |

价格只在input/output usage与两项报价都已知时估算；缺失为SQL NULL。失败正文仍可产生已知费用，不能因为生成失败就漏账。每次调用写一行，记账失败停止且不重新生成；取消后最多额外5秒清理记账。

### 交付快照与边界

确定路径/字节数/SHA256见 **plans/reports/phase-0.6-manifest.json**；manifest不含.env、日志、凭据或他人报告。本报告与manifest自身不放入自己的循环哈希。请DSH先逐条核对，再独立执行契约验收；如果文件变化就重新确认快照，不能用旧结果盖新代码。

最终冻结检查：32份文件哈希/字节数逐项一致，较基线新增26个顶层测试；4个本地Markdown链接有效，交付与运行日志未发现本机凭据值，Brain生产依赖不含database/GORM；迁移、既有ADR、人设/用例和4份延后草稿均未改。索引为空，尚无本轮Git提交。

正式验收建议重点重跑：Scope错配Provider零调用；公开数据拒绝DM/私密记录；预算先裁记忆、soul不丢且不切坏中文；HTTP4xx含429不重试/3次总上限/空回复1次；取消后仍记账且无虚假排队账本；正文错误保留usage；NULL与已知0不同；真实PG逐次记录与配置/诊断无密钥回显。完整规则以contracts/brain.md与contracts/memory.md为准，不以本表代替断言。

未执行/不在范围：真实LLM调用、真实平台逐一兼容验证、0.7c人格≥90%、Memory真实召回/SQL隐私查询、总线/调度/发送及P2能力。官方OpenAI文档两处抓取被403/Cloudflare阻止，未声称查证最新模型/价格；协议按既有项目契约和本地HTTP假服务测试，不使用真实key。Windows32位GCC不能跑amd64 race，WSL不能直连Windows localhost PG，因此race与真实PG分开验证；没有改PG监听/防火墙，也不冒称远端CI组合已通过。

教学说明：docs/update/2026-10-31-0303-大脑生成与用量切片.md。本轮实现写入结束，暂停获派文件，交DSH独立验收；WorkBuddy继续等待单独轻量派发，不自动推进0.7c。

## COMM-010 · 0.6接收与0.7c真实评估接手

主人已转交DSH 0.6独立验收通过（32/32哈希），并明确授权用.env真实FISH_LLM_KEY跑35条人格用例。本轮先按原快照提交0.6为b51f444，不夹带其他协作文案；随后实施P0-EVAL-1（contracts/persona-eval.md）真实评估。凭据仅在进程内读取，不回显/入仓。

新增internal/personaeval、fishctl persona eval及必要的Provider服务端model审计字段由Codex负责；personas/eval输入/人设正文只读，不动旧baseline回复。评分在调用前固定，原始输出为主分数，Brain投递文本为次分数；本轮不改标准或人格去凑≥90%。模型为当前配置deepseek:deepseek-chat，沿用当前预算/温度；未声明已达到门槛，结论待实际运行证据。

实施顺序：先写评分/快照/执行器和CLI失败测试，完善实际输出/attempt/usage审计；先本地假HTTP验证与脱敏，随后只跑一次完整35样本并保存逐条检查点、报告与快照。原始/最终两组分数、失败详情、可空费用均如实输出。0.7c新代码/产物交DSH复核，不提前提交/push。

## COMM-011 · 0.6提交完成与0.7c真实结果

**0.6已提交并推送b51f444**（原DSH 32/32快照）；后续0.7c实现与真实产物未暂存/提交/push，交DSH复核。

### 一次真实运行

- Run：20261031T105248Z-501d0282；source_kind=model_provider，baseline_used=false。
- 请求路由：deepseek:deepseek-chat；服务端返回model标识deepseek-flash，35次一致；不猜测其内部模型版本。
- 人格：dafeiyu@v1；实际35条cases.jsonl原始字节已保存；人格五文件、规则和源码hash见run内manifest/report。
- 时间：2026-10-31 03:52:48–03:53:13 America/Los_Angeles（PDT）；JSON审计时间UTC。
- 35/35完成，35次Provider调用、每条一次、0生成错误、35条PG记账确认；没有重抽样或改词表/人格后重算。
- **主分数原始模型输出15/35=42.9%，次分数Brain投递文本15/35=42.9%；未达到≥90%，该门槛至少32/35。** CLI退出1表示本轮未达标，报告完整保存，不是调用/执行中断。
- 输入80,878、输出495 tokens；报价未配置，费用为NULL，不能写成0或免费。只读PG窗口核账再次确认35行成功/相同token总数/费用均NULL。

证据：plans/reports/persona-eval/20261031T105248Z-501d0282/；report.md/report.json有35条实际原始/最终输出、逐条失败标签；results.jsonl是同步检查点；cases.jsonl/rules.json是首次调用前快照；pg-audit.json是独立只读PG统计；verification.json是Codex另写评分/哈希重算，analysis.md说明词法限制和预算，均不冒称DSH验收。

### 分数解释（不改原分）

严格词法失败包括：情绪标记未命中5次、兜底/拒绝词未命中各3次、公开输出含“主人”2次等。部分是规则覆盖不全/误报，如comment-005「不给。密码这种东西哪能往外掏。」因“密码”触发forbid:泄露凭据；没有泄露实际已配置凭据。qqg-007纯爱表态未写“娇娇”而失败。解释不改变42.9%，不得覆盖旧规则或重抽样凑90%。

当前保守8192字节预算使实际prompt估计8190–8192；完整soul+rules已大于预算，按裁剪顺序samples移除、rules截短。该配置事实可能影响风格，单轮不证明因果。若后续调整规则、预算或人格，需新版本新run再比较，本轮不自动开展优化。

### 新代码与自测

- 新增internal/personaeval（独立于persona加载包），固定词表、真实输入快照、无baseline执行器、原始/投递双评分、独占目录与检查点、敏感值脱敏、完整性标记。
- fishctl persona eval接入真实Brain/Provider与PG UsageRepo。生成器输入仅ID/channel/private/text，不含expect/forbid/kind；每条显式空Memory/Shared Scope，DM私密性固定true；不发送B站或QQ消息。
- Provider.Response仅增加可选服务端Model审计字段，非法可选元数据不覆盖有效usage/正文；新增测试先因字段缺失失败后实现。未更改既有Brain预算、温度、重试或输出截断。
- 内部只读审查发现“最后一条致命/取消可误报通过”，已用35条末样本失败测试先红后绿，独立标记fatal/interrupted阻止整轮验收成功；单条非致命生成错误仍按原35分母计失败。
- 测试产物均在临时目录且source_kind=test，不使用baseline作为真实结果；规则/人设/数据在运行前固定，运行后未改任何被记录的生产源码文件。
- 最新全仓单测、真实PG integration、vet、build、lint（0 issues）、WSL Go1.25/CGO_ENABLED=1全仓race通过；这些本地检查不再次调用真实模型。

本轮保留原有其他Agent协作文案与报告，不修改personas/eval/人设正文、迁移和既有ADR；默认config/fishd.toml仍llm.enabled=false。用于本次真实执行的config.local.eval-*.toml已被忽略，只有启用标志，无真实key值。所有凭据从.env进程内读取，报告/日志不得回显。

### DSH交接

请独立复核0.7c代码与真实产物：验证新manifest、run输入/规则/源码/输出hash，按rules.json自己重算35条，核查PG统计、实际服务端model标识及分母/完整性标记；不要直接抄本表结论，也不要求为了复核再花费35次真实调用。0.7c真实执行完成，**人格≥90%退出条件仍未满足**；CLI功能代码尚待独立验收，整体0.7不勾完成。

教学说明：docs/update/2026-10-31-0353-真实人格评估与证据.md。确定文件快照见plans/reports/phase-0.7c-manifest.json。通知已发布仓库，尚无DSH对COMM-011的新回执；本轮写入结束后暂停，等待独立复核，不自动进入下一Phase或优化重跑。

最终冻结检查：29个源码/产物文件、18个本地文档链接有效；run记录的生产源码哈希与当前文件一致（运行后仅调整测试来源标记和补文档，未改执行代码）；真实输入/人设/评分没有变化，交付文件未含本机配置凭据。独立Python按rules.json重算35条原始/投递评分一致；原文SHA与检查点/报告匹配，PG窗口35行和80,878/495 tokens一致。索引为空，0.7c尚无Git提交，默认生产llm开关仍false。

## COMM-012 · DSH复核接收与下一轮提案待主人选择

主人转交DSH 0.7c复核：评估功能验收通过，独立重算15/35一致；人格≥90%仍未满足。本轮据此将原版执行器及旧run证据固化提交，保留所有真实输出/规则/快照；不修改旧run评分。

原29文件快照核对：运行代码与证据28项保持精确哈希；仅plans/coordination.md在DSH复核后更新了0.7c状态，属于已确认状态登记，不是功能漂移。单测、真实PG、vet/build重新通过。其他Agent未验收协作文案继续不混入。

下一轮具体方案见plans/phase-0.7c-next-proposal.md，仍待主人选择：建议预算32768、模型/人设/温度不变，评分v2区分具体泄露与话题词并识别本轮关系维护表态；先补正反测试和旧真实输出离线对照，再新run对同一新输出同时给v1/v2。不会把拒绝词当万能放行条件，也不针对当前某一句加特例。无需通过词表删标签来凑达标。

已做无模型调用的预算干跑：全部35条完整system保留，最大估计24818，低于32768；0次实际模型调用、无PG写入。结果在plans/reports/budget-proposal-32768.json；临时Go验证器已清理，生产预算/评分仍是原值。该提案和干跑数据不夹入原版执行器的验收提交。

预算和评分被主人本轮明确列为“待主人决策”，已通过问题工具给出三种范围；在收到选择前不改配置/评分源码，不启动新的收费模型运行。旧15/35证据永不覆盖。
