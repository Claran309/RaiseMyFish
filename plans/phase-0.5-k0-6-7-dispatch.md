# K0-6/7装配诊断与离线检索执行单

主人放行K0-6配置/Reader装配/无模型check，以及K0-7的100条离线检索；不放行真实模型开/关35条对照，不宣称生产链路已消费知识。

## 精确范围

主Codex：internal/config新增Knowledge配置及测试；cmd/fishd Reader装配、check诊断及测试；config/fishd.toml显式关闭；internal/knowledge新增只读SnapshotInfo诊断元数据（不改Recall匹配/排序）。K0-7内部助手独占新internal/knowledgeeval、cmd/knowledgeeval和新报告目录，不改原库或旧评测。内部助手不是DSH，后续另独立验收。

## 门禁

- [x] 默认关闭/比例0–10/未知字段/坏配置先RED再实现；关闭不读坏目录。
- [x] NewFileReader装配、Revision/Count只读诊断、错误不降级、取消保留；check无模型/无用量/无服务状态写。
- [x] 真PG + 假模型端点的check路径证明模型0调用/用量0，默认配置保持false。
- [x] 100条查询与标注先冻结、发布库不改；首轮测量相关46/56=82.14%，覆盖43/60，误命中0/20，越界0。
- [x] Top3稳定顺序10次及Brain内存fake预算100/100通过，不当自然质量指标。
- [x] 全仓/PG/lint/race与内部只读审查通过；确定快照见K0-6报告目录manifest。
- [ ] K0-7覆盖质量门槛：43/60<48，未通过；不据此宣称全部质量过关或扩量。
- [ ] DSH独立验收实现及首次质量证据；不抄开发标注和结论。

K0-7即便质量门槛未过，也须保留测量结果；不转向收费模型对照或更改发布库。后续修复方向交主人/DSH基于真实证据决策。

实际默认/临时启用`fishd check`均通过：默认知识关闭，单独启用诊断可读500条，生产Revision=3c6e9e32d543f2bd62004a2f37910d1a72d8ff539883f00a4988e153298c4c43；两次provider_called=false、usage_written=false、pipeline_connected=false。100条正例分层20/20、20/20、3/20；17个场景遗漏已静态复核，无目标title/keywords/有效tag触发，未观察到Reader契约bug。
