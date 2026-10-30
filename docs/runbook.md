# 运行手册（Runbook）

> 出问题时按这里排查。先看 `fishctl status`，再看日志。

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
