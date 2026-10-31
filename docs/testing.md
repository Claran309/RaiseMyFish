# 测试策略

> 原则：**没有测试的功能视为未完成。** 核心链路必须有集成测试。

## 1. 测试金字塔

| 层 | 范围 | 工具 | 要求 |
|---|---|---|---|
| 单元 | 纯逻辑（评分/规则/解析） | `go test` | 覆盖核心分支 |
| 集成 | repository / 事件总线 / 调度 | 真实 PG（临时库或 testcontainers） | 关键路径全过 |
| 回归 | 人格语气 | `fishctl persona eval` | 通过率 ≥ 90% |
| E2E | 真实通道（可选，手动） | 手动/脚本 | 上线前跑 |

## 2. 各模块测试要求

| 模块 | 必测 |
|---|---|
| event | 幂等、状态迁移、并发 Claim |
| scheduler | 五道闸、会话锁、令牌桶、恢复 |
| brain | 上下文组装顺序、预算截断、LLM 失败分类 |
| memory | 写入路径、版本化、召回排序、隐私过滤 |
| collector | mock 平台、断线重连、自消息过滤 |
| sender | 成功/失败/核验失败、记账 |

## 3. Mock 策略

- 外部 HTTP（B站/LLM）：`httptest.Server` 或接口 mock
- 协议端（QQ）：本地 fake OneBot server
- 时间：可注入 clock（避免 sleep 测试）
- 数据库：集成测试用真实 PG，不用 sqlite 替代

0.1：`go test -tags=integration ./... -count=1` 使用 `FISH_TEST_DSN`，本机未设置时仅从根目录 `.env` 取DB连接串。数据库迁移与服务启动测试创建独立随机schema，不清空业务public表；测试结束只清理自己的schema。缺PG配置/连接失败将失败，不静默skip。CI的PG17服务已启用。

0.6：Provider/Brain使用httptest与fake UsageRepo验证HTTP、重试、隐私、预算、并发/取消；database的integration测试另覆盖真实llm_usage写入，以及本地假HTTP→现有人格→Brain→真实PG账本的组合路径。测试不调用真实模型，不把mock通过当作0.7c人格回归通过。check-brain诊断也必须保持模型调用数为0。

Windows运行race需要可用的64位C编译器；PATH中32位MinGW会报「64-bit mode not compiled in」，不能把这个结果当通过或以普通test替代race。可显式将CC设为可信64位clang/gcc路径，避免修改全局PATH。CI使用Go1.27，模块最低Go1.25；golangci-lint固定v2.15.0及v2配置。

## 4. 覆盖率

- 核心包（event/scheduler/brain/memory）行覆盖 ≥ 70%
- 不追求数字，追求关键路径覆盖

## 5. CI 门禁（必须全绿）

```
gofmt -l .            无输出
golangci-lint run     无 error
go vet ./...
go test -race ./...
go build ./...
```

## 6. 回归测试（人格）

- 用例在 `personas/<pack>/eval/cases.jsonl`
- 每次改 prompt/模型必须跑
- 失败用例打印实际输出

## 7. 手动验证清单（上线前）

- [ ] 真实直播间连续 2 小时
- [ ] 重启进程：不漏不回重
- [ ] 静默失败：断采集 30 分钟 → 告警
- [ ] 私信不外泄
