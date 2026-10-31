# K0-5开发交接：Brain知识资料注入

本轮实现ContextInput.Knowledge、范围/资源校验、预算和资料段注入。实际模型调用0，没有平台发送、没有生产开关启用；新功能待DSH独立验收。

## 本轮结果

- Knowledge=nil为关闭：不读取目录、不调用Reader，不增加knowledge消息字段或Reply.Meta字段，原P0消息字节/场景/instructions保持。
- 非nil为显式启用，即使空Items或比例0也校验Scope/Revision且必须保留完整人格。P>B直接budget，Provider0调用、Usage0行。
- 比例整数默认10，合法0–10，覆盖旧文档0–15。K0-6后续配置也应遵守10%上限，本轮没有新增配置/Reader装配/CLI评测功能。
- 完整人格/场景/事件固定说明先保留，按原顺序裁记忆后，知识只能用余量。知识不反过来驱逐记忆，不裁人格。
- Item只投影ID/Source/Text到user.context的knowledge数组；JSON转义防止资料改外层段结构，不进system/instructions。

## 预算口径

```text
B = 保守UTF-8消息字节 + 原64框架余量的上限
P = 完整人格/场景/事件/说明 + 与最终记忆同形的空容器
M = 最终保留记忆/共享事实相对该空容器的序列化增量
K_cap = min(floor(B * p / 100), max(0, B-P-M))
```

同形空容器保留nil与[]差异，所以P+M精确等于空知识基线估计。知识按每条JSON真实编码计算ID/Source/Text、HTML/引号/换行转义、knowledge键/数组及逗号开销。候选按Reader给定顺序整条选择，长条放不下继续试后续较短条目，不截接法。最后实际render再次核对总B、实际增量及cap，不一致就拒绝调用。

每条只编码一次，避免为每次试条重新渲染整段大prompt。裁剪/排序只操作副本，不修改Persona/Memory/Shared/Knowledge或Reader快照。

## 校验与安全

Task/Event/Memory/Shared原校验保持。Knowledge.Scope必须与Event同Channel/Session/Private，Context.Validate继续检查私密Origin、场景、Revision、重复ID及合法文本；失败在Provider前返回input。

知识条目与记忆/共享事实共用4096项及4MiB输入防护，加入ID/Source/Text、场景和Origin/目标会话等被处理文本。所有准备/选择循环检查取消，Provider重试/用量记账/清理超时和防御截断未改。

Reply.Meta的knowledge仅含启用/Revision/比例、已通过范围校验的候选/实际注入/丢弃ID和字节统计，不记录知识正文/完整prompt。P2 repository查询隔离仍须在查询层实现，本轮不靠prompt代替权限。

## 证据与验证

red.txt记录缺类型接缝的失败，behavior-red.txt记录旧行为缺审计、越界仍调用、未计资源等实际失败；green.txt记录8项Knowledge顶层测试全通过。Brain覆盖率84.3%。

真实PG组合测试使用独立随机schema及本机假HTTP：知识正文保留/转义、位于context；一次成功写1行model=fake:knowledge-model、输入123/输出7、费用NULL。把Knowledge.Scope改错后不再HTTP调用，PG行数仍1。真实模型请求0，不能把这个测试当知识自然相关率或人格效果评估。

全仓单元、真实PG integration、vet/build、lint v2.15.0（0 issues）、格式与WSL race通过。内部只读审查未发现Critical/Important，另独立跑Brain/Knowledge单测和Brain vet，不替代DSH验收。

protected_files按启动前哈希核对全部人格/旧评估166文件不变，internal/knowledge实现不改；默认config/fishd.toml未改。before/保存原Brain与Knowledge源码，source-snapshot/保存本交付源码，manifest给出精确范围。

## 未实施范围与接续

K0-6的配置开关、Reader生产装配、fishd诊断及persona eval知识文件快照/每条评估审计未接入；K0-7的100条自然检索质量和知识开关真实对照也未执行。默认关闭由Knowledge=nil保证，本轮Brain不读任何知识文件。

DSH请按contracts/knowledge.md独立核查比例上限、实际序列化增量、完整人格/记忆优先、私密范围、错误零调用、输入不可变与真实PG链路，更新验收状态。K0-5开发完成不等于全部K0上线或相关率达标，后续步骤仍需放行。
