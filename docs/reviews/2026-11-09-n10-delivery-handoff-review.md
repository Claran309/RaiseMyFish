# N10 私信接收账号绑定交付复核

> 日期：2026-11-09。依据用户提供的两张交付截图及当前未提交工作区复核。  
> 已提交HEAD：e759da4；本次N10修改尚在工作区，不能以HEAD代表受审实现。  
> 本轮未修改业务源码、契约或配置，未执行Git写操作、真实平台/LLM调用，不扩展到A6/N11。

## 结论

N10主要修复方向正确：接收账号从采集SelfID进入事件Meta及复合Session；首次计划按接收账号取凭据；无主旧事件不回落当前活跃账号；复合Session在发送时能解析peer。当前仍有两项可复现缺口与一项契约同步未完成，暂不建议将N10记为验收通过。

## 1. P1：已有计划绕过显式Session/Meta冲突校验

位置：internal/sender/durable/durable.go的dmPlanAccount，约242—261行。

PlanAccount命中后直接返回，DMRecvActor只在没有计划时调用。于是“有旧计划”成为跳过归属一致性检查的分支。Resume也直接采用计划账号，没有先区分缺失旧归属与显式矛盾归属。

离线探针：计划账号111，Session=111:20002，Meta.recv_actor=222；假发送结果为prepare=1、arm=1、sends=1、error=nil。并未真实调用平台。该行为与交付声明“冲突归属冻结”不符。

建议：区分三种状态——缺失、合法一致、显式冲突。旧事件缺失归属且已有可信旧计划可以保留兼容；Session/Meta已有有效冲突时无论计划是否存在都拒绝新arm/POST。事件与计划账号不一致同样需要明确拒绝策略；已在途尝试保留只读核验/审计，不能因此重投或改号。

验收：覆盖Send与Resume、无计划/ready/armed/accepted、旧裸Session无Meta/有效一致/有效冲突。不能为阻止新投递而破坏已接受事实的只读恢复。

## 2. P2：内存去重没有跟随新的事件身份分域

位置：internal/collector/dm/dm.go约247、271、284、311—326行。

新持久事件ID是dm:{recvActor}:{peer}:{msgID}，但saw/markSeen仍只使用原始msgID。同一个Collector切号后，两账号同peer/msgID会被内存去重抑制，虽然其新事件ID和私密Session应不同。

离线探针用同一个Collector先SelfID=111后222，得到events=1，预期2。当前TestCollectorSeparatesAccountsForSamePeer为两个账号分别创建Collector，无法覆盖这个状态残留。

这是条件性边界：本轮未证明B站真实msg_key是否全平台全局唯一，也没有观察真实漏收；但新设计没有声明可依赖该全局唯一性，内存身份应与持久幂等身份一致。

建议：统一使用完整事件幂等键作为seen key，或者至少recvActor＋peer＋msgID；poll取得的确认仍匹配完整ev.ID。补同实例切号、同账号不同peer、同账号同peer重复、ACK失败重拉用例。

## 3. 契约未同步

contracts/event.md第63行仍定义dm:{uid}:{msg_id}。本次新导出event辅助API、复合Session、Meta.recv_actor、旧任务冻结和旧计划兼容都没有对应当前契约增量。

建议按项目流程明确契约新基线：recvActor指B站MID而非DB账号行ID，peer含义、格式约束、冲突/缺失语义、旧事件与旧记忆不可推断归属、已有计划恢复、人工处理方式。不能把“contracts未改”当作此项接口变更已经完成的证明；也不要求为了同步契约擅自迁移无证据旧数据。

## 测试事实

- go test ./internal/event ./internal/collector/dm ./internal/sender/dm ./internal/sender/durable ./cmd/fishd -count=1 -timeout=20m：通过。
- go test -tags=integration ./internal/database -count=1 -timeout=20m：通过，约69秒；比截图所列TestDeliveryPlan过滤集更广，但仍不是全仓integration。
- 定向go vet、go build ./...、定向gofmt只读检查与git diff --check：通过；存在既有codex报告CRLF提示。
- 新增overlay不变量探针2项FAIL，分别证明上述问题，不属于已有正式回归通过。

临时证据：tmp/review-20261109/n10_dedup_test.go.txt、n10_actor_conflict_test.go.txt、n10-overlay.json、n10-review-output.txt；只映射虚拟测试文件，不改包内源码。

复跑：

```powershell
go test -overlay tmp/review-20261109/n10-overlay.json ./internal/collector/dm ./internal/sender/durable -run '^TestN10Review' -count=1 -v
```

截图中during_brain场景当前是发送器级模拟：修改fake.current后调用Send，并没有实际运行Brain阻塞/真实Scheduler。这能证明首次Send选择旧actor的局部性质，不能宣称整个采集→排队→生成→发送都已E2E验证。补上述缺口后再考虑完整装配用例，后续N11仍按其独立规格执行。
