# P4 Watchdog 派发单（自愈看门狗）

> 目标：检测 fishd 冻结/崩溃 → 自动重启（P4 阶段能力）。
> 判断依据：心跳文件（fishd 定期写 `state/fishd.heartbeat`）+ 进程存活。

## 一、心跳

- `cmd/fishd` 每 30 秒写 `state/fishd.heartbeat`（内容：时间戳 + pid）
- 已存在的状态文件（`fishd.status.json`）可复用，但心跳要**独立文件**（避免与状态导出耦合）

## 二、看门狗（`internal/watchdog` + 独立进程）

- `cmd/fishwatchdog`：独立进程（更可靠，不跟 fishd 同生共死）
- 循环（每 60 秒）：
  1. 读心跳文件：mtime 超过 `stale_seconds`（默认 120）→ 判「冻结」
  2. 检查进程存活（pid 从心跳内容读；进程不存在 → 判「死亡」）
  3. 连续 `unhealthy_threshold`（默认 2）次异常 → 重启：
     - kill 旧进程（如果还在）
     - 执行启动命令（配置 `restart_command`，如 `fishd --env-file .env`）
     - 记录事件到日志（`action=watchdog_restart`）
  4. 重启后冷却 `cooldown_seconds`（默认 300），防重启风暴
- 配置 `[watchdog]`：enabled、interval_seconds、stale_seconds、unhealthy_threshold、cooldown_seconds、restart_command
- 日志：健康/冻结/死亡/重启/冷却，含时间与 pid，不含凭据

## 三、测试

- 心跳新鲜 → 健康
- 心跳过期 → 冻结 → 重启（mock restarter）
- PID 不存在 → 死亡 → 重启
- 冷却期内不重复重启
- 连续 1 次异常不重启（阈值 2）

## 四、禁止

- 不改 contracts、不碰调度/发送语义、不执行 git

## 五、交付格式

- 文件列表、测试结果、配置说明、未做项、是否暂停写入

## 六、交付结果（2026-11-04）

- 已完成：fishd 30s 心跳；watchdog 判定（冻结/死亡）、阈值、冷却、杀旧进程、重启日志；配置校验；独立 fishwatchdog 进程；单测覆盖全部派发场景。
- 验证：gofmt/vet 干净；`go test ./...` 全绿；race 全绿（knowledge 单独跑 455.161s）；`-tags integration -p 1 ./...` 全绿；arch-check 三项通过；`go build ./...` 通过。
- 未做：未写入 docker-compose/systemd 示例；未在真实 systemd 环境演练重启；Web 面板展示属 P5 范围。
- 状态：待 DSH 验收，写入暂停。
