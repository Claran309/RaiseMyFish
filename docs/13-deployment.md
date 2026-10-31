# 13 · 运行与部署

> 开发期本机直跑，交付期 Docker Compose。
> 目标机器：主人个人电脑（16GB 内存），边用边跑。

## 1. 开发期（推荐，低开销）

```
Windows 本机：
  ├─ Go 服务：go run ./cmd/fishd（或 air 热重载）
  ├─ PostgreSQL：本机安装，注册为服务自启
  └─ QQ 协议端：NapCat / LLBot（保持登录）
  └─ B站：bili-cli（Python venv，按需）
```

**不推荐开发期开 Docker Desktop**：WSL2 VM 常驻 1.5~2.5GB，
在只剩 4GB 空闲的机器上是负担。Docker 留给「交付给用户」。

### 资源预算

| 组件 | 内存 |
|---|---|
| fishd（Go） | 50~150MB |
| PostgreSQL | 200~400MB |
| QQ 协议端 | 150~300MB |
| **合计** | **< 1GB** |

### 一键启停（`scripts/dev.ps1`）

```powershell
# 启动：检查 PG → 启动协议端 → 启动 fishd
# 停止：优雅关闭 fishd → 可选停协议端
# 状态：各组件健康
```

### Makefile 命令

```bash
make dev-status   # 各组件状态
make docker-up    # 构建并启动全部服务（交付/全栈）
make docker-down  # 停止
make docker-logs  # 跟随日志
```

## 2. 交付期（Docker Compose）

```yaml
# deployment/docker-compose.yml（实现期编写）
services:
  postgres:   # 17+，数据卷挂载
  fishd:      # 构建自本仓库
  # 协议端按需（用户自备 cookie/账号）
```

- 数据卷：`./data/pg`、`./data/logs`
- 环境变量文件：`.env`（不入仓）
- 端口：PG 5432、fishd 8080、协议端按需
- 不把凭据写进 compose，用 env_file

## 3. 配置

```toml
# config/fishd.toml（示例）
[server]
addr = "127.0.0.1:8080"

[db]
dsn_env = "FISH_DB_DSN"

[llm.providers.deepseek]
type        = "openai"
base_url    = "https://api.deepseek.com/v1"
api_key_env = "FISH_LLM_KEY"
models      = ["deepseek-chat"]

[llm.routing]
default = "deepseek:deepseek-chat"
live    = "deepseek:deepseek-chat"
dm      = "deepseek:deepseek-chat"

[channels.live]
enabled = true
room = "1992099976"

[channels.comment]
enabled = true

[channels.dm]
enabled = true

[channels.qq]
enabled = false
```

- 配置优先级：环境变量 > 配置文件 > 默认值
- 敏感项只写 `*_env`，从环境变量取
- 配置校验失败直接退出并打印原因

## 4. 进程守护

| 环境 | 方式 |
|---|---|
| Windows 开发 | `nssm` 注册为服务，或 watchdog 脚本 |
| Linux 交付 | systemd unit |
| 容器 | restart: unless-stopped |

崩溃自动拉起；连续失败 N 次停止并告警（防止无限重启风暴）。

## 5. 日志与数据

| 数据 | 位置 | 保留 |
|---|---|---|
| 应用日志 | `logs/fishd-YYYY-MM-DD.log` | 7 天 |
| 日报 | `logs/daily-YYYY-MM-DD.md` | 90 天 |
| PG 数据 | `data/pg`（或系统 PG 目录） | 按表策略 |
| 备份 | 每日 pg_dump → `data/backup/` | 7 天 |

## 6. 环境变量清单

```
FISH_DB_DSN=postgres://user:pass@127.0.0.1:5432/raisemyfish?sslmode=disable
FISH_LLM_KEY=sk-...
FISH_BILI_COOKIE=...        # 或 FISH_BILI_COOKIE_FILE=路径
FISH_QQ_TOKEN=...
FISH_LOG_LEVEL=info
```

**全部不入仓**；提供 `.env.example`（占位符，不含真实值）。

## 7. 端口规划

| 服务 | 端口 |
|---|---|
| PostgreSQL | 5432 |
| fishd | 8080 |
| NapCat/LLBot WebUI | 6099 / 3001 |
| 避免与 DSH（64958 等）冲突 | — |

## 8. 升级与回滚

- 迁移前自动 pg_dump
- 二进制替换 + 重启
- 版本号写入 `fishctl status`
- 回滚：回滚二进制 + 回滚迁移（或恢复备份）

## 9. 验收

- [ ] 空机器按 README 能起
- [ ] 崩溃后自动拉起
- [ ] 日志/日报可读
- [ ] 备份可恢复
- [ ] 内存占用 < 1GB（不含协议端与 Docker）
