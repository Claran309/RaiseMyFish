# P0 接线收尾派发单（把包接进 fishd/CLI）

> 背景：watch/gift/Presence 都是包级完成、没接主循环（0.12/0.13 只交付包）。
> 本批把剩下三件接上；extractworker + fishctl memory 已在 `phase-0.5-memory-wiring.md`（另一批，不重复）。

## 任务 1：watch 接入 fishd

1. `cmd/fishd/main.go`：直播采集运行中时，按 `[watch] interval_seconds`（默认 600s）定期触发一次看直播
   - 每次：Record(10s) → Frames(4) → Describe → Transcribe → 日志 `action="watch"`（延迟/结果）
   - 失败降级：不阻塞采信/调度（watch 是旁路）
   - `[watch] enabled=false` 时不启动（默认关）
2. `[watch]` 配置补 `interval_seconds`
3. 测试：mock 流水线 + 定时触发断言（fake clock）

## 任务 2：gift 接入 fishctl（手动触发）

1. `cmd/fishctl/gift.go`：`fishctl gift send --room <房间> --gift-id <ID> --num <n>`（读取 `[gift]` 配置：real/enabled/max_price/max_num/allowed_rooms/daily_limit）
   - 默认 dry-run（`[gift] real=false` 时只打印）
   - 白名单/价格/数量校验（已有）
2. 测试：mock 仓储/命令输出；dry 与实际区分的断言

## 任务 3：Presence Summary 暴露

1. `cmd/fishctl/status.go`：输出加 `presence` 字段（`Summary(now)`：如「在看直播 30308220 / 在回弹幕」）
2. 测试：mock Table 注入、空表、多值

## 禁止

- 不改 contracts、不碰调度/发送/人格/知识库
- 不真实送礼/看直播（真跑留最终冒烟）
- 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、未做项、是否暂停写入
