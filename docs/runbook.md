# 运行手册（Runbook）

> 出问题时按这里排查。先看 `fishctl status`，再看日志。

## 装配诊断（不调用模型）

```powershell
go run ./cmd/fishd check
go run ./cmd/fishd --config config.local.knowledge-diagnostic.toml check
```

`check`会连接PG确认ready，并按配置装配Brain/知识Reader；不调用Brain.Reply、不写LLM用量、不拿服务单例锁、不写运行状态。默认`[knowledge].enabled=false`；需要本地检查时另建被忽略的config.local.*配置启用，默认文件不要打开。

知识启用后输出`assembled`、Revision、条数与budget_percent（0–10）；加载失败明确报错，空库加载成功与缺/坏目录不是同一状态。`provider_called=false`、`pipeline_connected=false`表示只有装配/诊断，没有宣称事件worker已消费知识。Reader加载成功也不保证每个实际事件都能在总预算内生成，仍须K0-5逐请求预算。

离线检索开发验证入口为`go run ./cmd/knowledgeeval --output <不存在的新目录>`，不会加载.env或真实模型。现有100条首轮结果相关率82.14%，但正例43/60低于48/60，质量门禁尚未通过；不要据此启用真实模型对照或扩量。旧输出目录不可覆盖。

## 1. 服务起不来

| 检查 | 命令/动作 |
|---|---|
| 端口被占 | `netstat -ano | findstr 8080` |
| 数据库连不上 | 检查 `FISH_DB_DSN`；`psql` 手连 |
| 迁移未执行 | `make migrate-up` |
| 配置错误 | 看启动日志的校验错误 |
| 依赖缺失 | `go mod download` |

## 2. 鱼不回消息（静默失败）

按顺序查：

1. `fishctl status` — 各通道健康、最近事件时间
2. 采集是否在进事件：`fishctl events --ch live --n 5`
3. 调度是否在出任务：查 `tasks` 表 `queued` 数
4. brain 是否报错：日志搜 `brain` `error`
5. 发送是否失败：`fishctl dlq list`
6. 预算是否熔断：`fishctl cost --today`

## 3. 弹幕采集断线

- 看 collector 日志 `reconnect`
- 确认账号 cookie 未失效
- 重启 collector（或整个 fishd）
- 持续失败：切协议端/换方案

## 4. 发送失败

| 错误 | 处理 |
|---|---|
| 限流（code 类） | 等待，降低频率 |
| 内容违规（state 17 / 12006） | 不重发，记录 |
| 评论区关闭（12002） | 跳过 |
| 鉴权失效 | 刷新 cookie |

死信重放：`fishctl dlq retry <id>`

## 5. QQ 协议端掉线

- 检查协议端进程与登录态
- 重连协议端
- QQ 通道挂起期间，B站 通道应继续（确认降级生效）
- 恢复后确认补拉/补回

## 6. 数据库问题

| 现象 | 处理 |
|---|---|
| 连接池耗尽 | 调小并发 / 查长事务 |
| 磁盘满 | 清理归档 / 扩容 |
| 迁移失败 | 恢复备份，回滚迁移 |
| 数据损坏 | 从 `data/backup` 恢复 |

## 7. 成本失控

- `fishctl cost --today` 看分布
- 检查是否有循环重试
- 检查 prompt 是否缓存失效（system 是否被动态内容污染）
- 临时降级：切便宜模型 / 调低配额

## 8. 进程崩溃循环

- 看崩溃日志与退出码
- 连续失败 N 次后应停止自动重启（防风暴）
- 修复后再启动

## 9. 紧急停止

```bash
# 优雅停止
kill -TERM <pid>   # Windows: taskkill /PID <pid>
# 或停止服务
nssm stop fishd    # Windows
systemctl stop fishd  # Linux
```

## 10. 日常检查（每天）

- [ ] 日报是否生成
- [ ] 各通道最近事件时间是否正常
- [ ] 死信是否堆积
- [ ] 花费是否在预算内
