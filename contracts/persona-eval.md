# 人格真实模型评估 · P0-EVAL-1

本轮由主人明确授权：提交已验收0.6后，以.env真实密钥运行既有35条用例，判定≥90%。不回放baseline，不为达到门槛修改人格/用例/评分后重跑挑选结果。

## 输入和评分冻结

CLI由Codex实现，新增internal/personaeval为本轮评估包（Codex负责），不使persona加载包反向依赖Brain。旧.workbuddy草稿仅参考既有词法评分规则，不能恢复baseline生成器。生成入口只接真实Provider/Brain，单测可注入假生成器但产物必须标test。

运行前校验全部Case ID唯一、通道/场景合法、expect/forbid非空且标签已定义。保存用例原始JSONL快照、五个人格文件哈希/版本、确定的评分规则JSON与哈希、执行器源码哈希/代码基线和生效配置（不含key、DSN）。加载与快照前后校验，避免哈希描述的数据不是实际使用的Pack。

规则使用既有persona词法标签：forbid一票否决，expect全部满足；tag具体词表/长度/正则在首次真实调用前固定为persona-lexical-v1，并落盘rules.json。长句采用既有人格场景限制live40/comment60/dm200/qq_private200/qq_group60，短句20；不在看到输出后放宽或换词。规则只是可重现的词法回归，不冒充完整语义评估；误报可另注，不能改自动分数。

## 生成与分数

35条均使用同一Pack/配置，每条新建独立合成Task/Event，显式空Memory/Shared Scope，不向模型透露expect/forbid、评分词表或baseline。dm的Private固定true，qq采用Case.private，live/comment为false。只调用模型，不发送B站/QQ消息。

主通过率对Provider返回的原始模型文本打分；同时记录Brain实际返回的截断文本与次通过率，防止后处理把模型超长掩盖成通过。生成错误计失败，不从分母删除；全部35条完成才判定本轮≥90%，至少32/35。主动取消/鉴权失败可停止并标incomplete，保留已花费与已完成结果，不宣称完成35条。

每条记录provider:model（请求路由）、服务端model若有、人格版本/快照、输入Case、原始/最终输出、各项失败原因、逐次尝试统计/安全错误、prompt哈希（不存完整prompt）、时间和可空token/费用。每次真实调用仍写PG llm_usage。基准回复文件不作为运行输入；baseline参数或生成器禁止进入真实CLI。

整轮取消/总时限或致命鉴权/记账错误独立标记interrupted/fatal，即使发生在第35条也不允许报告complete/reached90成功；已得到的数学分数仍保留。单条非致命生成失败按原分母计失败，不自动删掉该样本。

## 证据与费用

顺序运行，沿用Brain的单次/总超时及最多3次实际调用，不反复抽样挑最佳；35样本最多105次尝试。整轮默认20分钟上限。每条完成立即落JSONL检查点，最终输出report.json/report.md，主分数raw与次分数delivered明确区分；任何中断都保留证据。

产物在plans/reports/persona-eval/<run-id>/，目录独占创建不覆盖。必须记录source_kind=model_provider、baseline_used=false；测试产物标test。报告/进度不包含.env、key、DSN、完整prompt或HTTP头；配置中的敏感值若意外出现在模型文本，打失败并脱敏保存，保留原文哈希/脱敏标记，不回显凭据。

本轮明确授权真实调用费用；未知报价不伪造0，token使用实际统计、费用保持NULL。完成后按真实结果报告是否达到90%，失败不偷改人设或评分标准。DSH独立复核执行器与真实产物；0.7c新代码未验收前不提交/push。
