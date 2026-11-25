# RaiseMyFish staging 容量报告（2026-11-25）

> 环境：独立 PostgreSQL 17.11 + pgvector 0.8.0（127.0.0.1:55632），
> `raisemyfish_bench`；fishd 指标 19091；mockllm 18081；发送 dry-run。
> 基线 commit：`edf31c3` + 本工作区改动。所有原始数据在 `bench/raw/`（未进 git）。
> 成本：mock LLM，**0 成本**。

## 方法

- 预热 1 分钟，测量 5 分钟；每档 3 次取中位数。
- `cmd/fishload` 按 `at` 排序回放，`--rewrite-at` 把 `events.at` 写成实际注入时间；
  这样延迟口径仍是 **events.at → 首次 send_log.ok**（未改口径）。
- 压测库每次重置为 1,000 观众 / 1,000 会话 / 10,000 事件。
- 测量结束后停止注入，按终态（pending/queued/retry/replying）最多 drain 240 秒。
- 原始 JSONL：`snapshots.jsonl`、`inject.jsonl`、`counts.json`、`latency.json`、
  `send-summary.txt`、`drain.json`。

## R1 直播弹幕入口（1x/2x/5x/10x）

| 档位 | 速率/分钟 | 新增入向 | 成功发送 | 失败 | 重复发送 | Live P50 | Live P90 | Live P99 | 最大 pending | 最大最老事件 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1x | 24 | 145 | 145 | 0 | 0 | 0.214s | 0.232s | 0.269s | 1 | 0.0s |
| 2x | 48 | 292 | 292 | 0 | 0 | 0.213s | 0.242s | 0.258s | 1 | 0.0s |
| 5x | 120 | 722 | 722 | 0 | 0 | 0.221s | 0.250s | 0.277s | 1 | 0.1s |
| 10x | 240 | 1,450 | 1,450 | 0 | 0 | 0.210s | 0.231s | 0.747s | 1 | 0.0s |

三档低速率和 10x 均在三组内完成终态结清；10x 三组的 `drain.json` 都是
`completed=true`。原始目录：

- 24：`bench/raw/R1-20261125-005354`、`R1-20261125-010003`、`R1-20261125-020211`
- 48：`bench/raw/R1-20261125-010618`、`R1-20261125-011231`、`R1-20261125-011837`
- 120：`bench/raw/R1-20261125-012447`、`R1-20261125-013057`、`R1-20261125-013704`
- 240：`bench/raw/R1-20261125-023403`、`R1-20261125-024012`、`R1-20261125-024623`

### 30 分钟 soak（1x，24 条/分钟）

`bench/raw/R1-20261125-143704`：预热 1 分钟、测量 1,800 秒，共注入 744 条；
结束 `send_ok=744`、`send_failed=0`、`unfinished=0`、`duplicate_sends=0`；
live P50 `0.209s`、P90 `0.230s`、P99 `0.242s`；goroutine 15、
heap 约 3.8 MB，DB 池使用 1。说明 1x 长稳没有泄漏或积压。

## R2 多 worker 竞争

4 个 fishload 进程、8 个直播会话分片、总计 120 条/分钟，3 次中位数：

| 新增入向 | 成功发送 | 失败 | 重复 | Live P50 | Live P90 | Live P99 | 最大 pending | LLM 峰值 |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 728 | 728 | 0 | 0 | 0.215s | 0.743s | 0.763s | 3 | 4 |

原始目录：`bench/raw/R2-20261125-025514`、`R2-20261125-123737`、`R2-20261125-124351`。

## R3 同用户 10 条/秒刷屏（非 1x–10x 容量档）

600 条/分钟、同一用户、同一直播间，3 次中位数：

| 新增入向 | 成功发送 | 未完成 | 失败 | 重复 | Live P50 | Live P90 | Live P99 | 最大 pending |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 3,610 | 2,007 | 1,602 | 0 | 0 | 203.7s | 375.4s | 415.7s | 1,730 |

这是当前最明确的瓶颈：同一 `session` 在调度器内串行，10 条/秒时 3 组都积压；
事件没有丢失，但 180 秒 drain 后仍有约 1,600 条未完成。原始目录：
`bench/raw/R3-20261125-125010`、`R3-20261125-125925`、`R3-20261125-130835`。

## R4 mock LLM 直接并发

不经过 fishd/调度器；mock 固定延迟 1 秒。每档 3 次：

| 并发 | 请求数/次 | 成功 | 错误 | P50 | P99 | mock peak |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 20 | 20×3 | 0 | 1.000–1.001s | 1.004–1.006s | 10 |
| 50 | 100 | 100×3 | 0 | 1.002–1.005s | 1.010–1.013s | 50 |

原始目录：`bench/raw/R4-20261125-131831/`（`c10-r*.json`、`c50-r*.json`）。
结论：mock LLM 本身在 10/50 并发下无错误、延迟稳定；R3 的积压不是模型吞吐。

## R5 dry-run 发送 120/240 条/分钟

R5 使用独立 run（`[sender] dry_run=true`），120/240 各 3 次：

| 速率/分钟 | 成功发送 | 失败 | 重复 | Live P99 | 说明 |
|---:|---:|---:|---:|---:|---|
| 120 | 725 | 0 | 0 | ~0.24s | `send_log` 全部落账；3 次 717/725/725 |
| 240 | 1,450 | 0 | 0 | ~0.24s | `send_log` 全部落账；3 次 1449/1450/1450 |

原始目录：`bench/raw/R5-20261125-153020`、`153626`、`154236`、
`154850`、`155505`、`160115`。

## R6 记忆提炼积压

| 初始事件 | 测量 | 结束 done | 结束 pending | 每秒完成（约） | 证据 |
|---:|---:|---:|---:|---:|---|
| 1,000 | 90s | 1,000 | 0（快照滞后显示 100） | ~11/s | `bench/raw/R6-20261125-132015` 等 |
| 5,000 | 300s | 3,100 | 1,900 | ~10/s | `bench/raw/R6-20261125-132337` 等 |
| 5,000 | 600s | 5,000 | 0 | ~8–10/s | `bench/raw/R6-20261125-151657`（长跑清零） |

`done_events_without_memory=0`（`bench/sql/12_memory_backlog.sql`），说明没有事件被默默跳过；
5k 在 300 秒时仍有 1,900 pending，延长到 600 秒后清零，证明是吞吐延迟而非数据丢失。
1k 三组：`132015`、`132152`、`140926`；
5k 三组：`132337`、`132844`、`133349`。

## 串会话与终态对账

- `bench/sql/14_bench_cross_session.sql`：`cross_session=0`，见
  `bench/raw/cross-session.txt`。
- R1 10x 三组 `counts.json` 的 `send_ok=1450`、`unfinished=0`、`send_failed=0`、
  `duplicate_sends=0`；R2 同样为 0 未完成。
- R3/R6 的未完成数是队列积压，不是发送失败；对应 `drain.json`/`memory-backlog.txt` 已保留。

## Prometheus

本机 `claran-prometheus` 容器可用（8084），但未把它配置成 fishd 的必需依赖；
已直接抓取一次 fishd `/metrics` 原文：`bench/raw/prometheus-metrics.txt`。
其中包含 `fishd_events_pending`、`fishd_oldest_event_age_seconds`、
`fishd_scheduler_active`、`fishd_lease_lost_total`、`fishd_duplicate_send_total`、
`fishd_reply_latency_seconds{channel,quantile}`、`fishd_db_pool_in_use`。

## 结论

- **R1 1x/2x/5x/10x 全部通过**（10x P99 0.747s，0 丢失/0 重复）。
- **同用户 10 条/秒是已知超容量场景**；瓶颈是会话串行，不是 mock LLM。
- **记忆提炼约 10 条/秒**，5k 积压需要更长窗口。
- 细节故障恢复见 `docs/reports/2026-11-25-fault-recovery-report.md`；
  瓶颈排序见 `docs/reports/2026-11-25-bottleneck-ranking.md`。
