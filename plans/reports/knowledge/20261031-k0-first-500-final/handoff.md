# K0-1～K0-4 开发交接（待 DSH 独立验收）

日期：2026-10-31，America/Los_Angeles。开发者：Codex。基线HEAD：`008beb6`；源契约：[P05-KNOWLEDGE-1](../../../../contracts/knowledge.md)。本次主人明确接受当前P0人格57.1%未达标的现状，放行G2并允许K0与主对话P0-B并行。

## 1. 实际范围与任务状态

| 子任务 | 开发交付 | 验收边界 |
|---|---|---|
| K0-1 | Scope/Query/Item/Context/Reader及纯校验完成 | `ContextInput.Knowledge`及Provider零调用断言随K0-5暂缓；本轮不碰Brain |
| K0-2 | 四必需根文件、批次白名单、严格YAML、原始字节Revision、限额/取消/链接边界完成 | 不做文件监听、全局缓存或数据库表 |
| K0-3 | 先权限/场景过滤、确定匹配与排序、Top3和深拷贝完成 | 100条自然检索质量验收留K0-7 |
| K0-4 | 全量源快照、审核清单、首批500条发布、原词/接法往返及幂等完成 | 不编造设定/观众资料，不宣称全量约7000条已迁移 |

开发自测完成，DSH独立验收未代填。正式Phase任务勾选由DSH根据实际范围维护；不能把本报告作为P0.5全部退出。

## 2. 获派及实际改动路径

- `internal/knowledge/types.go`、`validate.go`、`file.go`、`recall.go`及四个测试文件：独立知识模块。
- `scripts/export_memes.py`、`scripts/test_export_memes.py`：离线只读迁移和六项临时源库测试。
- `personas/dafeiyu/knowledge/{memes,lore,viewers,games}.md`：500条梗；其余三个文件为合法空entries。
- 本目录：正式发布manifest、selection、审核决策、命令结果、独立数据核对、保护文件hash及交付快照。
- `plans/reports/knowledge/20261031-k0-first-500/`：审查修复前的发布草稿，原样保留；正式验收入口使用本-final目录。
- `docs/update/2026-10-31-0648-K0知识快照与只读迁移.md`：按实际代码编写的教学变更说明。
- 被忽略的 `data/knowledge-import/`：原始归档、未审核/已审核候选清单和临时审核工作资料，不入仓。

本次未修改 `internal/brain/`、人格五文件、人格评测源码、旧run/规则/manifest、现有正式契约、依赖、配置或数据库迁移；没有调用真实模型。未读取P0-H目录中的input全文。共享区主对话新增候选人格、v3评分/离线重评等文件不是本任务交付，不混入本次暂存范围。

## 3. 接口和实现

导出类型严格对应知识契约；没有把知识塞进Memory或Persona。FileReader构造时整体校验，Recall不再读磁盘。公开知识满足场景即可；私密知识只进相同私密Channel/Session，不靠prompt约束。

错误区分无命中与失败；取消可经errors.Is判断。YAML除KnownFields外检查节点类型，防止数字转字符串；拒绝未知/重复字段、锚点/别名/合并键、多文档和超限。Revision根据实际读取原始字节，目录移动不改变，BOM/CRLF改变则版本改变。

匹配优先级为标题>keywords>有效标签，再按最长词、不同命中数、priority、ID排序。文字梗、游戏梗、网络用语、直播、自学习等泛分类不独立触发。返回最多3条独立副本，修改Scenes/Origin不影响其他请求。

首批文件340205字节；独立核对最大条目资料为747字节。这些数字不代表prompt的<=15%预算已验证，最终序列化与预算分配仍归K0-5。

## 4. 源数据、审核与可重复迁移

快照：`data/knowledge-import/8f36ebfd124b604075676cbe2fd8763c2a1ddb51676415995b4fbf79f6422cce/`，读取源SQLite两层时使用单个只读事务。

| 项 | 数量 |
|---|---:|
| 原始memes/custom | 6660 / 172 |
| 原始合计 | 6832 |
| 同名custom覆盖内置 | 15 |
| 唯一标题 | 6817 |
| 原始缺接法 | 42 |
| 审核候选 | 775 |
| 排除 | 265 |
| 通过但未选入本批 | 10 |
| 本批启用 | 500（custom92 + memes408） |

审核集中在公开文本接法与人格边界：排除攻击/隐私/多角关系、括号动作、无独立接法及把人格规则当知识的素材。它是Codex开发侧审核，待DSH独立抽查；不是对全部百科事实作真实性认证。

原title和usage逐字保留；全部源字段，包括summary/origin/other/null值，都在原始JSONL归档。首批不注入未核实出处，公开summary为空，tags只作明确分隔。完整审核ID与排除原因见 `review-decisions.json`，正式500条见 `selection.json`。

以下命令在仓库根目录运行，只重放既有快照，不接触源库或模型；重复执行成功时不改已有文件：

```powershell
python -B scripts/export_memes.py publish --snapshot data/knowledge-import/8f36ebfd124b604075676cbe2fd8763c2a1ddb51676415995b4fbf79f6422cce --selection plans/reports/knowledge/20261031-k0-first-500-final/selection.json --output personas/dafeiyu/knowledge/memes.md --report plans/reports/knowledge/20261031-k0-first-500-final
python -B plans/reports/knowledge/20261031-k0-first-500-final/verify_migration.py
```

独立核对验证500个原词/接法与源快照一致、custom覆盖正确、审核覆盖、资料大小和output/selection/importer哈希一致。工具对目标人工修改报冲突，不覆盖；测试还确认超大标签导致完整条目超限时，既不产生知识文件，也不产生报告目录。

## 5. 测试和审查证据

测试先于实现：类型和FileReader/Recall分别先编译失败；严格标量类型、单条长匹配取消、来源空白绕过、发布端完整大小及泛分类误触发均曾复现失败，再修复通过。

| 命令 | 结果 |
|---|---|
| `go test ./... -count=1` | 通过 |
| `go test -tags=integration ./... -count=1` | 通过，真实PG隔离schema |
| `go vet ./...`、`go build ./...` | 通过 |
| `gofmt -l .` | 无输出 |
| 固定v2.15.0 golangci-lint全仓run | 0 issues |
| WSL CGO_ENABLED=1 Go全仓race | 通过 |
| `python -B -m unittest discover -s scripts -p test_export_memes.py` | 6项通过 |
| `go test ./internal/knowledge -cover -count=1` | 已观察90.2%语句覆盖 |
| 500条逐一以其原词查询 | 全部能召回自身ID，不等于自然查询相关率 |
| 独立数据verify及发布重放 | 通过，新增模型调用0 |
| `git diff --check` | 通过 |

全仓命令详情见 `checks.json`。WSL启动产生已有配置键警告，但Go race实际退出0，未修改WSL系统配置。真实PG测试是既有路径回归，不冒称知识→Brain→PG新增链路已接入。

内部只读代码审查发现两项Important：来源绝对路径可被前导空白绕过、迁移发布漏算tags等字段。两项均先补失败测试复现后修复，审查者复跑限定Go/Python测试并确认关闭；无Critical。审查的合成临时数据库曾因锁留下空清理问题，后已核验仅该合成source.db并逐项清理，没有遗留任务阻塞。这份内部审查不代替DSH独立验收。

## 6. 保护检查、未执行项与交接状态

开始时记录的65份Brain、原人格、历史评测/重评产物及知识契约原始hash见 `protected-files.json`；交付前逐项复算未变化。P0-H输入不在读取或hash范围内。

明确未执行：K0-5 Brain类型/预算/装配；K0-6生产配置及评估审计接入；K0-7的100条检索质量和真实模型开关对照；设定/观众真实素材补齐；P0-B/H；全量梗库扩量。前述事项按主人指定并行分工和后续门禁推进。

无新依赖、SQL或schema风险。当前只做字面召回；效果需自然查询评测，不能用title自命中或素材审核代替>=80%相关率，也不承诺提升57.1%。原始归档保留在本地被忽略目录，跨机器验收需保留该确定快照，而不是重新导出已经变化的源库。

**交付状态：开发/自测已完成，文件快照已冻结，暂停本任务写入，待DSH按本目录独立验收；未暂存、提交或push。** DSH确认后再由Codex仅集成本次明确路径；不混主对话的候选/评分改动，不建PR。
