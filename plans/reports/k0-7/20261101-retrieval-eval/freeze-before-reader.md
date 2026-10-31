# 运行前数据冻结（开发侧）

在首次调用本评测的真实 FileReader 之前固定：

- cases.jsonl SHA256：c1111d04da4bd27ff2843f5dbb9373f5238db65a12d68645c7e9761b3b1551a1。
- batch-synthetic-acl.md SHA256：1153fec3708f0d0b4e4412c4eb86dfc0fa51cd69dd3fb3b634fc6c5a02b28f4e。
- batch-synthetic-order.md SHA256：4f192a3e8e4dc30072a513304f27efcd2140ebe39a98481e26123f9a12e12b06。
- 60正例：20精确标题、20含标题自然句、20不含目标标题的语义场景；0显式别名（当前500条keywords均为空）。20无命中、15合成私密权限反例、5合成通道限制反例。
- 标注来源为开发助手审读当前公开库title/usage；不是DSH独立标注，不读取原35条或H题。标题与语义场景分别计数，不把含标题自然句冒称语义泛化。
- 预期ID是运行前按接法语义挑选的相关条目，不按实际输出补标；未列入预期的返回计不相关，可能保守。仅有抽卡、总结等明确多个接法的输入列多个预期，不将同tag自动当相关。
- 20个无标题语义场景可能超出K0词面Reader能力；保留并如实报告，不换题、改标注或Reader追分。
- 20个权限镜像控制与合成Top3排序是结构校验，不计入100条相关率的分子分母。
- 发布库全public；私密fixture明确无真实用户信息。临时快照逐字节复制完整公开库后仅增加白名单memes/batch-synthetic-acl.md；镜像用同一输入的合法源scope验证可召回。
- 仅调用内存fake Provider/Usage；不访问网络、模型、PG或.env。预算使用固定合成人格，不能代表真实人格的35条开关对照。

首次真实Reader之前，runner将写freeze.json（case/fixture/代码及公开库每文件原字节hash、生产Revision、合成Revision），再从同一内存字节快照创建Reader。已有输出目录拒绝覆盖。
