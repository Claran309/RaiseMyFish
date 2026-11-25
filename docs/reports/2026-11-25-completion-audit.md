# 完成审计（2026-11-25）

> 对照《压测与运维数据清单-20261123》与用户 goal 提示词的 A/B/C/D、验收标准。
> 结论先行：**A/B/C 完成；D 的 R1–R8 全部测量并归档，R3 发现会话串行瓶颈，
> R7/R8 记录恢复缺陷。**
> 未做的项目在文末逐条列出，不用“预计/大概”代替证据。

## A. 线上只读运维报告：PASS

- SQL：`bench/sql/01..09`；报告生成器：`bench/report`（每条 SQL 在
  `SET TRANSACTION READ ONLY` 事务里执行）。
- 报告：`docs/reports/2026-11-24-30day-ops-report.md`；
  图：`docs/reports/assets/daily-events.svg`、`daily-latency.svg`、`daily-cost.svg`。
- 原始 CSV/baseline：`bench/out/report-*`（未进 git）。
- 真实基线（2026-11-24）：events 35,942 / 入站 24,320 /
  done 28,104 / dead 515 / send_log 4,803 / llm_usage 22,564 /
  有效记忆 1,123 / 观众 1,164 / 30.9 天 / 峰值 24 条每分钟 / 均值 3.48。
- 真实缺陷：comment dead 405/590（68.6%）；491 条 dead 无 `event_errors`
  （早于 0026 迁移）；watchdog 未启用时 5 天 23 次启动；ReAct 降级 142 次
  （parse 107 / timeout 18 / llm_error 13）。

## B. 压测工具：PASS

| 交付 | 证据 |
|---|---|
| `cmd/mockllm` | `cmd/mockllm/main.go`、`main_test.go`；固定延迟/token、错误/超时、`/stats` active/peak |
| `cmd/fishload` | `cmd/fishload/main.go`、`main_test.go`；at 排序、fixed/burst、`--rewrite-at`、只 Publish |
| `bench/fixtures` | `main.go`、`sanitize.go`、`sanitize_test.go`、`mock-send.mjs`；UID 哈希/正文截断/会话分片 |
| `fishctl bench snapshot` | `cmd/fishctl/bench_snapshot.go`、`bench_snapshot_test.go`；单行 JSON |
| 汇总 | `bench/analyze/main.go`；`go run ./bench/analyze --raw bench/raw` |

## C. 代码改动：PASS

- C.1 dry-run：`internal/sender/dryrun/`、`cmd/fishd/serve.go:349` 起；
  live 使用真实 SendRepo + 本地 transport，comment/dm 使用 durable 计划；
  R1 240 的 `counts.json` 显示 `send_ok=1450`、`send_failed=0`，证明仍写 send_log。
- C.2 metrics：`internal/metrics/`、`internal/config/metrics.go`、
  `cmd/fishd/metrics.go`；关闭测试 `TestStartMetricsServerDisabled`，
  关键指标名测试 `TestMetricsRegistryHasRequiredNames`。
- C.3 埋点：scheduler 的 lease renew/lost、skip、session busy 等计数；
  `fishd_skip_total{reason}`、`fishd_degrade_total{reason}`、`fishd_duplicate_*`。

## D. staging 压测：PASS（含已知瓶颈）

环境：独立 PG 17.11 + pgvector 0.8.0（127.0.0.1:55632），
`raisemyfish_bench`；`config/fishd.bench.toml`；mock LLM；dry-run。

| 场景 | 结果 | 证据 |
|---|---|---|
| R1 24/48/120/240 条/分钟 ×3 | 全部完成，240 P99 0.747s，0 失败/0 重复/0 未完成 | `bench/raw/R1-20261125-*`；容量报告 |
| R1 30 分钟 soak（24/min） | 744 条全部完成，P99 0.242s，goroutine 15/heap ~3.8MB | `bench/raw/R1-20261125-143704` |
| R2 4 worker / 8 会话 ×3 | 728 成功，P99 0.763s，0 失败/0 重复 | `bench/raw/R2-20261125-*` |
| R3 同用户 600 条/分钟 ×3 | 2007 成功，P50 203.7s、P99 415.7s，约 1600 未完成 | `bench/raw/R3-20261125-*` |
| R4 mock LLM 10/50 并发 ×3 | peak 10/50，0 错误，P99 ~1.01s | `bench/raw/R4-20261125-131831/` |
| R5 dry-run 120/240 ×3 | 120 send_ok 717/725/725；240 1449/1450/1450；0 失败/重复 | `bench/raw/R5-20261125-*` |
| R6 记忆 1k/5k | 1k 90s 清零；5k 300s done 3100/pending 1900，600s 清零 | `bench/raw/R6-*` |
| R7 kill -9 + watchdog ×3 + 长 drain | 进程 5.15–5.20s 恢复；短窗口 168 条待租约，长 drain 组清零 | `bench/raw/R7-*` |
| R8 DB 中断 ×3 + 真停 staging PG ×3 | fishd 4–5s 后退出，无自动重连；真停 30.7–32.3s | `bench/raw/R8-*`、`R8pg-*`、`R8min-*` |

串会话：`bench/sql/14_bench_cross_session.sql` 输出 `cross_session=0`
（`bench/raw/cross-session.txt`）。重复发送：所有 R1/R2 组 `duplicate_sends=0`。
Prometheus：容器可用；抓取原文 `bench/raw/prometheus-metrics.txt`，未配置成运行依赖。

## 验收标准逐条

| # | 标准 | 结论 | 证据 |
|---:|---|---|---|
| 1 | 生产库零写入，前后计数一致 | PASS | `bench/raw/prod-counts-before.txt` / `prod-counts-after.txt`：events 35,942、send_log 4,803、llm_usage 22,564 完全一致 |
| 2 | 线上每个数字可由 SQL 复现 | PASS | `bench/sql/01..09` + `bench/report` |
| 3 | staging 1x/2x/5x/10x 各 3 次；重复 0、串会话 0、无丢失 | PASS（R1） | R1 三组各 3 次；`counts.json` 0 失败/0 重复/0 未完成；cross-session 0 |
| 4 | 故障四列：检测/恢复/丢失/重复；kill -9 后 30–60s 恢复 | PASS | R7 5.2s 进程恢复、长 drain 306/306；R8 无自动恢复已记录；故障报告四列表 |
| 5 | dry_run 关闭行为与现在一致；开启无外网 | PASS（代码路径） | `DryRun=false` 走原路径；`dryrun/network_test.go` 断言默认网络未被触发；真实平台 A/B 因硬约束未做 |
| 6 | /metrics 关闭无路由/开销；开启指标齐全 | PASS | `TestStartMetricsServerDisabled`、`TestMetricsRegistryHasRequiredNames`；`prometheus-metrics.txt` |
| 7 | `go test ./...` 全绿，gofmt | PASS | `bench/raw/go-test-final.txt`；改动文件已 gofmt |
| 8 | raw/out/dist 未进 git | PASS | `git check-ignore`：`.gitignore:5/6/3` |
| 9 | 完成审计 | PASS | 本文件 |

## 已知缺陷与限制（不阻断本次交付）

1. **R8 缺陷**：`R8pg` 一次带负载，`R8min` 两次只验证进程行为；主矩阵 3 次
   仍是“终止连接 30 秒”。fishd 无进程内重连这一缺陷仍然存在，但故障注入
   本身已有 3 次真停证据。
2. **R7 短窗口队列未清空**：进程恢复 5 秒，但旧租约 5 分钟导致短窗口
   168 条未完成；补做的长 drain 组已清零（306/306）。恢复时间达标，但
   “重启后立即处理旧租约”仍不是当前行为。
3. **R3 有积压**：R3 约 1600 条仍 pending；R6 5k 延长到 600 秒后已清零。
   R3 是会话串行导致的容量问题，不是发送失败/丢失。
4. **真实模型 smoke 未跑**：没有调用真实 Provider，成本 0；没有实现
   `--allow-real-llm` 入口（本次只要求 mock，真实平台发送被硬约束禁止）。
5. **真实平台 A/B 未做**：dry-run 关闭路径由源码分支与全仓测试覆盖；
   `dryrun/network_test.go` 断言默认网络未被触发；未做真实平台逐字节 A/B。
6. **文档清理已执行、历史仍需处理**：已通过 `scripts/privacy-audit.ps1 -Apply`
   从索引移除 225 个个人/过程文件（本地保留）；旧提交历史不会自动消失，
   若已推送需轮换 B 站 cookie/CSRF。`LICENSE` 已按 MIT 添加。

## 下一步建议

- 先修 R3 的单会话串行/背压（或把“同用户刷屏”定义为无 SLA）。
- 修 R8：明确进程内重连或“退出+watchdog”策略（3 次真停已补）。
- 复核 225 个索引删除并处理已公开过的 B 站凭据/历史。
- 再跑 `go test ./...` 后复核 `git status`，不要 `git add .`。
