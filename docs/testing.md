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
