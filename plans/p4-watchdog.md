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
