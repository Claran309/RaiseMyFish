# K0-6/7统一交接：装配诊断完成，检索覆盖未过门禁

K0-6实现及工程验证完成，待DSH独立验收；K0-7独立100条首轮已运行，相关率达标，但正例覆盖43/60低于48/60，**不能标记全部质量通过**。真实模型开/关各35条明确未跑，模型调用0；不宣称知识进入生产消费链路。

## 1. K0-6装配与诊断

config新增Knowledge.Enabled=false、Dir=personas/dafeiyu/knowledge、BudgetPercent=10，整数合法0–10。默认config/fishd.toml只增加显式关闭段，LLM与通道开关保持。关闭时不访问目录；启用缺/坏目录明确错误，不伪装零命中，错误隐藏路径并保留取消链。

fishd通过NewFileReader构造knowledge.Reader接口；SnapshotInfo只读已构造快照的Revision/Entries，不重新读文件。加载空库是assembled=true/entries=0，有合法Revision，与关闭或加载失败不同。Brain比例取配置；没有事件worker将Reader召回结果交给真实业务，K0-5按请求校验/预算仍独立执行。

新fishd check先确认PG ready，再装配Brain/Reader、打印状态，不调用Brain.Reply、不写LLM用量、不获取服务单例锁或写status。原check-brain保留。

实际命令结果另存check-default.json和check-enabled.json：默认关闭；单独被忽略的config.local.knowledge-diagnostic.toml启用诊断加载500条，Revision=3c6e9e32d543f2bd62004a2f37910d1a72d8ff539883f00a4988e153298c4c43。两者provider_called=false、usage_written=false、pipeline_connected=false。

真实PG隔离集成测试将LLM指向本机假端点，证明诊断HTTP调用0、llm_usage行0、status不存在；启用500条、关闭坏目录不读、启用坏目录失败。没有实际生产消息或模型生成。

## 2. K0-7首轮离线质量

数据和源字节/源码在首次Reader调用前冻结，cases SHA256=c1111d04da4bd27ff2843f5dbb9373f5238db65a12d68645c7e9761b3b1551a1。新评测与人格35条及旧H完全分开。

| 项目 | 首轮结果 | 门槛/结论 |
|---|---:|---|
| 返回相关率 | 46/56=82.14% | ≥80%，通过 |
| 正例覆盖 | 43/60=71.7% | ≥48/60，**未通过** |
| 无命中负例误召回 | 0/20 | ≤2，通过 |
| 权限/通道反例 | 0越界 | 0，通过 |
| 相同查询合法Scope镜像 | 20/20 | 排除全公开空结果假证据 |
| 固定Top3顺序 | 10次相同 | 结构检查通过 |
| Brain实际JSON预算 | 100/100 | ≤10%，通过；内存fake非真实模型 |

正例分层20精确标题20/20、20含标题自然句20/20、20无标题场景3/20。17个漏召回都没有目标title/keywords/有效tag词面，keywords发布字段均空，未发现Scope/数据标签错误或Reader契约bug。返回的部分bug/“乐”等宽标签/短词带来歧义，保守按冻结相关ID计，不事后补标。

仅这个子集相关率达80不能证明自然场景泛化，覆盖门禁仍失败。不改题、标注、发布500源或Reader追分，也不自动扩量或跑收费对照。

## 3. 私密与预算证据

发布500全公开，不能用无私密内容证明权限。评测在临时副本里加独立合成fixture：3个private Origin覆盖15条跨会话/平台/公开目标反例，另1个仅QQ私聊场景源覆盖5条场景反例；每条有同输入合法Scope镜像。所有夹具明确合成，无真实观众私信，公开库原字节不改。

100条质量分母不包含额外镜像/排序控制。预算验证用真实Brain配内存fake Provider/Usage，每条候选与空知识各组装一次，只检查序列化增量/总预算/固定system与注入ID，合成人格，不等于真实人格开关A/B。没有HTTP、PG或生产Provider调用。

预期相关ID由开发助手事前审读公开title/usage标注，非DSH独立标注。DSH还需抽查标注语义与相关性。100条一旦使用即已见测试集，后续变更不能把其重复调优结果称为全新独立未见验证。

## 4. 工程检查与保护

K0-6、K0-7新增测试均先失败再实现；主线程全仓单元、真实PG integration、vet/build、lint v2.15.0（0 issues）、格式及WSL race通过。内部k067_review独立纯单测并从results重算全部指标一致、freeze代码/文件哈希一致，无Critical/Important；没有代替DSH的PG或质量验收。

助手本机race因32位编译器失败的原日志保留，不能当通过；主线程WSL全仓race补齐，日志在本目录。人格、发布知识库、现有人格评测及K0-5/旧报告等223份保护文件保持起始hash，brain源码本轮未改。默认配置唯一行为是明确知识关闭。

## 5. 复核入口与未做项

- K0-7原始首次证据：plans/reports/k0-7/20261101-retrieval-eval/run/freeze.json、results.json、report.md；coverage-review.md为17漏召回的静态复核。
- 本目录verification.json为主线程只读重算，manifest.json为统一源码/数据/报告快照；source-snapshot保留最终配置/装配/诊断源码。
- 未执行真实模型开/关各35条、persona eval知识装配/旧评测改动、生产事件worker接入、数据扩量、关键词补标或RAG。

DSH请按契约独立验收K0-6实现和K0-7首次测量，不将coverage失败改成通过。补审核触发词/别名或调整资料元数据属于后续知识版本及新验证决策，本轮不自动执行；实际生产知识仍关闭、链路未连接。
