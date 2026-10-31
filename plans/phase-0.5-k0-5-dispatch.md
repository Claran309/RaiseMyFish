# K0-5 Brain知识注入执行单

主人已放行internal/brain写入窗口；本轮只实现Knowledge DTO接缝、校验/资源、预算/注入和安全审计，先写失败测试。知识≤10%，覆盖旧计划0–15；契约已先修订。K0-6配置/Reader生产装配/CLI评测及K0-7质量验证不在本轮实施。

## 可写范围

Codex：internal/brain/types.go、brain.go、context.go、新knowledge.go及所属knowledge_test.go；internal/database新增真实PG组合测试；contracts/knowledge.md和brain.md范围澄清、本人报告/交接/教学说明。其他会话/人格/旧评估不改，默认生产配置不启用知识，不跑真实模型。

## 执行与检查

- [x] 默认Knowledge=nil消息/审计与P0相同，不读目录；百分比默认10，0合法，负值/>10拒绝。
- [x] Scope/Revision/场景/跨私密/重复或坏条目校验，失败Provider0调用/Usage0行；共4096项/4MiB覆盖知识。
- [x] 非nil时完整P优先，P>B即budget（即便0%或Items空）；只裁记忆，不裁人格。
- [x] K_cap=min(floor(B*p/100),max(0,B-P-M))，计JSON转义/键/分隔增量；整条选择，长条放不下继续较短条目，不改入参。
- [x] 知识仅user.context，system及instructions不变；Reply.Meta只审计合法ID/Revision/开销，不存正文。
- [x] 新测试先RED后GREEN，全仓测试/PG/vet/build/lint/race及内部只读审查。
- [x] 固定交付hash、结果及教学说明；本人交接见COMM-021及plans/reports/k0-5/20261101-brain-knowledge/。
- [ ] DSH按契约独立验收，不抄开发自测；验收前不提交本轮新功能。

K0-1~4已提供Reader/DTO，不在Brain内主动读取/召回。后续可信装配方传非nil Context才启用，当前fishd/fishctl仍不会自动打开知识功能。

开发结果：8项Knowledge顶层单测及既有Brain回归通过；Brain覆盖率84.3%。真实PG组合测试确认1次假HTTP成功产生1行123/7 token、费用NULL；后续无效Scope不调用也不新增行。全仓/PG/vet/build/lint/race通过，默认配置未改、实际模型调用0。合同/摘要/实施计划的活动预算上限统一收紧为10%，历史DSH同步记录原样保留。
