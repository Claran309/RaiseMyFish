# 13 · 运行与部署

> 同一份代码支持三种运行方式；人格、知识与行为语义完全一致。
> PostgreSQL **必须 17+ 且带 pgvector**，fishd 不内置数据库。

## 1. 支持矩阵与依赖

| 运行方式 | 状态 | 入口 |
|---|---|---|
| Windows 本机 | **现有，保持不变** | `scripts/dev.ps1` + 根目录 `.env` |
| Linux 本机 | **新增** | 静态二进制 + systemd + 外部 PostgreSQL |
| Linux / Docker Compose | **新增** | `deployment/docker-compose.yml`（fishd + pgvector PG17） |

| 依赖 | 用途 | 说明 |
|---|---|---|
| Go 1.25+ | 仅构建 | 运行阶段不需要 Go 工具链 |
| PostgreSQL 17+ | 事件、记忆、账号、向量 | 必须能 `CREATE EXTENSION vector`（迁移 0006） |
| pgvector | 语义召回 | 官方 `postgres:17` 不含 pgvector，用 `pgvector/pgvector:pg17` 或自行安装扩展 |
| Node.js 24 | `tools/bili-whale` 的直播/发送/礼物脚本 | 脚本使用全局 `WebSocket`/`fetch`，建议 24（勿低于 22） |
| ffmpeg / ffprobe | `[watch]` 看直播、`[proactive]` 主动弹幕 | 不启用这两个能力时可不装 |
| ASR 服务 | 看直播音频转写 | 外部 HTTP 服务，默认 `http://127.0.0.1:8077/v1/audio/transcriptions` |
| LLM / Embedding API | Brain、提炼、向量 | 外部 HTTPS 服务，只从环境变量读密钥 |
| B站 Cookie + 本账号 UID | 采集与发送 | 可放 DB 活跃账号，环境变量兜底（见 §5） |

容器镜像已内置 Node、ffmpeg/ffprobe、CA 证书与时区；**不包含 PostgreSQL**。

CI 的集成 job 同样使用 `pgvector/pgvector:pg17`，保证干净环境能执行迁移 0006；不要换回不带扩展的官方 `postgres:17`。

## 2. Windows 本机（现有方式）

```
Windows 本机：
  ├─ Go 服务：scripts/dev.ps1 start（或 go run ./cmd/fishd）
  ├─ PostgreSQL：本机安装，注册为服务自启
  ├─ Node.js：跑 tools/bili-whale 脚本
  └─ B站：Cookie 放根目录 .env
```

**开发期不建议常开 Docker Desktop**：WSL2 常驻 1.5~2.5GB，在内存紧张的机器上是负担；Docker 留给 Linux/服务器交付。

### 资源预算

| 组件 | 内存 |
|---|---|
| fishd（Go） | 50~150MB |
| PostgreSQL | 200~400MB |
| QQ 协议端（未装配） | 150~300MB |
| **合计（不含 Docker）** | **< 1GB** |

### 一键启停（`scripts/dev.ps1`）

```powershell
.\scripts\dev.ps1 status   # PG / 日志目录 / fishd 进程
.\scripts\dev.ps1 build    # 编译 bin\fishd.exe
.\scripts\dev.ps1 start    # 前台运行，Ctrl+C 优雅退出
.\scripts\dev.ps1 stop     # 跨终端停止（超时强杀进程树）
```

## 3. Linux 本机（新增）

### 3.1 安装依赖与建库

```bash
# 示例为 Debian/Ubuntu 口径：
#   - PostgreSQL 17 + pgvector 来自 PGDG 仓库或发行版仓库；
#   - Node.js 24 用 NodeSource / nvm 安装（脚本需要 ≥22，发行版自带 nodejs 往往过旧）；
#   - ffmpeg 仅在启用 [watch]/[proactive] 时需要。
sudo apt install -y postgresql-17 postgresql-17-pgvector ffmpeg
# 另装 Node.js 24：NodeSource setup_24.x 或 nvm install 24

sudo -u postgres psql -c "CREATE USER fish PASSWORD 'CHANGE_ME';"
sudo -u postgres createdb -O fish raisemyfish
```

确认扩展可用（迁移会自动执行）：

```bash
sudo -u postgres psql -d raisemyfish -c "CREATE EXTENSION IF NOT EXISTS vector;"
```

### 3.2 构建、配置、迁移、启动

```bash
make build                 # 或 go build -trimpath -o bin/fishd ./cmd/fishd
cp .env.example .env       # 填 FISH_DB_DSN / FISH_LLM_KEY / B站凭据
$EDITOR config/fishd.toml  # 房间、self_id、通道开关

# 必须在仓库根目录执行：配置里的 logs/、data/、state/、personas/ 都是相对路径
./bin/fishd --config config/fishd.toml --env-file .env migrate up
./bin/fishd --config config/fishd.toml --env-file .env
```

运行期口径：`config/fishd.toml` 的 `[db].dsn_env` 默认指向 `FISH_DB_DSN`；凭据只从环境变量或 DB 活跃账号读取。

### 3.3 systemd（可选）

```ini
[Unit]
Description=RaiseMyFish fishd
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=raisemyfish
WorkingDirectory=/opt/raisemyfish
# .env 由 fishd 自己读取；确保该文件仅服务用户可读
ExecStartPre=/opt/raisemyfish/bin/fishd --config config/fishd.toml --env-file .env migrate up
ExecStart=/opt/raisemyfish/bin/fishd --config config/fishd.toml --env-file .env
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
```

- `logs/`、`data/`、`state/`、`personas/` 必须归 `raisemyfish` 用户可写（梗白学习与灵魂记忆会回写 persona）。
- systemd 与 `fishwatchdog` 二选一，不要同时托管同一个 fishd。用 systemd 时保持 `[watchdog].restart_command` 不指向自身。
- CLI 运维：`./bin/fishctl --config config/fishd.toml --env-file .env status`。

## 4. Docker Compose（新增）

交付物：

- `deployment/Dockerfile`：多阶段构建 `fishd`/`fishctl`，运行阶段基于 `node:24-bookworm-slim` 并安装 ffmpeg；**PostgreSQL 依赖外部**。
- `deployment/docker-compose.yml`：`fishd` + 一次性 `migrate` + `pgvector/pgvector:pg17`。
- 镜像**不烤入** `config/fishd.toml` 与 `.env`；配置只读挂载，凭据从 `deployment/.env` 注入。

### 4.1 首次部署

```bash
cp .env.example deployment/.env
$EDITOR deployment/.env
```

`deployment/.env` 至少设置：

```dotenv
POSTGRES_PASSWORD=CHANGE_ME
# 容器内数据库主机名是 postgres；密码与上一行一致
FISH_DB_DSN=postgres://fish:CHANGE_ME@postgres:5432/raisemyfish?sslmode=disable
FISH_LLM_KEY=sk-...
FISH_BILI_COOKIE=...
FISH_EMBEDDING_KEY=...
```

再按需改 `config/fishd.toml`（直播房间、`self_id`、通道与礼物开关）。容器内路径仍是 `logs/`、`data/`、`state/`、`personas/`，无需改相对路径。

> 安全提醒：仓库里的 `config/fishd.toml` 是当前本机运行配置，可能已开启真实送礼/主动弹幕并绑定特定房间。首次部署务必按目标账号核对，尤其 `[gift].real`、`[proactive].enabled` 与各通道开关。

```bash
# 仓库根目录执行：构建镜像 → 起 PG → 跑迁移 → 起 fishd
make docker-up
# 等价：
docker compose -f deployment/docker-compose.yml up -d --build
```

构建默认走 `goproxy.cn`；海外网络可在 `deployment/.env` 设置 `GOPROXY=https://proxy.golang.org,direct`。

迁移服务使用同一份配置，但 `migrate` 只校验数据库连接、日志级别与目录、状态文件路径：B站 Cookie、LLM/Embedding 密钥等运行期凭据不参与迁移。首次部署可以先建表、再补凭据；`fishd` 启动并在 DB 活跃账号回填之后才会核验各启用能力的凭据。

### 4.2 查看与运维

```bash
make docker-logs
docker compose -f deployment/docker-compose.yml ps
docker compose -f deployment/docker-compose.yml exec fishd ./fishctl --config config/fishd.toml status
# 面板只监听容器回环，不映射到宿主机；在容器内读取：
docker compose -f deployment/docker-compose.yml exec fishd curl -s http://127.0.0.1:8090/api/status
```

面板按安全约束只允许回环监听，因此 Compose 默认**不发布 8090**。需要浏览器面板时，优先在宿主机上跑 Windows/Linux 本机方式；不要为了映射端口把面板改成监听全部网卡。

### 4.3 数据卷

| 卷/挂载 | 容器路径 | 内容 |
|---|---|---|
| `pgdata` | `/var/lib/postgresql/data` | PostgreSQL 数据 |
| `fishd-logs` | `/app/logs` | 应用日志、日报 |
| `fishd-data` | `/app/data` | 状态文件、心跳、提炼水位 |
| `fishd-state` | `/app/state` | 私信游标、冻结标记、watchdog 心跳 |
| `../personas`（bind） | `/app/personas` | 人格、知识、梗白学习与灵魂记忆（**需可写**） |
| `../config/fishd.toml`（bind，只读） | `/app/config/fishd.toml` | 运行配置 |

- 容器以 uid 1000（`node`）运行。Linux 宿主机需保证 `personas/` 对该 uid 可写：`sudo chown -R 1000:1000 personas`。
- ASR/本机搜索服务跑在宿主机时，容器内用 `host.docker.internal`；宿主监听地址、防火墙与替代方案见 4.4。
- 宿主机 5432 已被本机 PostgreSQL 占用时，改 `postgres.ports` 左侧端口即可；容器内 `FISH_DB_DSN` 仍用 `postgres:5432`。

### 4.4 容器访问宿主机 ASR/搜索

容器里的 `127.0.0.1` 指向容器自身，不是宿主机。Compose 已给 `fishd` 加：

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

因此 `config/fishd.toml` 可以写：

```toml
[watch]
asr_url = "http://host.docker.internal:8077/v1/audio/transcriptions"

[web]
search_url = "http://host.docker.internal:9200/search"
```

配置校验对明文 HTTP 只额外放行**精确主机名 `host.docker.internal`**（大小写不敏感）。以下地址仍会被拒绝：

- `host.containers.internal`、`gateway.docker.internal` 等其它别名；
- `host.docker.internal.evil.com`、`host.docker.internal.` 等相似或带尾点写法；
- Docker 网关 IP 或其它私网地址（`192.168.*`、`10.*` 等）；
- 带用户信息、查询串或片段的地址。

这个窄授权只覆盖 ASR 与搜索。LLM、Embedding、视觉模型仍只接受 HTTPS 或回环 HTTP（`contracts/brain.md` 对 Provider 的约束），不能填 `http://host.docker.internal`；本地模型服务请在宿主机或 Compose 内加一层 HTTPS 反向代理，让容器用 HTTPS 域名访问。Podman 的 `host.containers.internal` 不在本例外内，需要改用 HTTPS 端点或 Compose 内的受控代理。

`host-gateway` 只解决「容器 → 宿主机」的名字解析，**不会让宿主机只监听 `127.0.0.1` 的服务自动接受网桥连接**。宿主机服务必须满足：

1. 监听容器可达的地址：`0.0.0.0:<port>` 或具体的网桥/网关地址；只绑 `127.0.0.1` 时通常连不上（宿主机回环与容器网桥不是同一地址空间）。
2. 允许 Docker 网段访问：Linux 下用防火墙只放行 Docker 网段（如 `172.16.0.0/12` 或 `docker0` 网段），不要直接暴露到公网；Windows/macOS 的 Docker Desktop 由虚拟机转发，通常监听 `0.0.0.0` 并放行防火墙即可。

明文 HTTP 会经过 Docker 虚拟网桥：只用于可信的本机辅助服务。搜索若配置了 `search_key`，密钥同样明文上网桥；对密钥敏感时，把 `search_url`/`asr_url` 指向 HTTPS 反向代理（宿主机或 Compose 内的受控代理），而不是直接明文。

验证（返回任意 HTTP 状态码都说明网络可达）：

```bash
docker compose -f deployment/docker-compose.yml exec fishd \
  curl -sS -m 3 -o /dev/null -w '%{http_code}\n' http://host.docker.internal:8077/
```

连接失败时先查宿主机监听地址与防火墙，不要把地址改回 `127.0.0.1`（那会指向容器自身）。

### 4.5 使用外部 PostgreSQL

Dockerfile 本身不包含数据库。若已有外部 PG 17 + pgvector：

```bash
docker build -f deployment/Dockerfile -t raisemyfish/fishd:local .

docker run --rm --env-file deployment/.env \
  -v "$PWD/config/fishd.toml:/app/config/fishd.toml:ro" \
  raisemyfish/fishd:local --config config/fishd.toml migrate up

docker run -d --name fishd --restart unless-stopped --env-file deployment/.env \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD/config/fishd.toml:/app/config/fishd.toml:ro" \
  -v "$PWD/personas:/app/personas" \
  -v fishd-logs:/app/logs -v fishd-data:/app/data -v fishd-state:/app/state \
  raisemyfish/fishd:local --config config/fishd.toml
```

> `docker run` 不读取 Compose 的 `extra_hosts`；需要访问宿主机 ASR/搜索时必须显式加 `--add-host=host.docker.internal:host-gateway`。

或在 `deployment/docker-compose.yml` 中删除 `postgres` 服务与 `fishd.depends_on.postgres`，把 `FISH_DB_DSN` 指向外部实例。

## 5. 配置与凭据

- 配置优先级：进程环境 > dotenv > TOML > 默认值。
- 敏感项只写 `*_env`（如 `api_key_env`、`dsn_env`），真实值放 `.env` 或 `deployment/.env`，均不入仓。
- 账号解析：**DB 活跃账号优先，环境变量兜底**。只把 Cookie 存 DB 也可以启动（不再要求环境里同时有 Cookie）。
- 窗口支持：Windows 本地读根目录 `.env`；Compose 读 `deployment/.env`；两者互不影响。
- 当前面板固定回环地址；`[web].addr` 填非回环会被配置校验拒绝。

## 6. 进程守护

| 环境 | 方式 |
|---|---|
| Windows 开发 | `nssm` 注册服务，或 `fishwatchdog` |
| Linux 交付 | systemd `Restart=on-failure`，或 `fishwatchdog` |
| 容器 | `restart: unless-stopped` |

不要叠加多个守护者。`fishwatchdog` 依赖 `state/fishd.heartbeat` 与 `[watchdog].restart_command`，与 systemd/Compose 二选一。

`[watchdog].restart_mode` 必须显式选择（启用时必填）；观察窗口默认 60 秒：

| 模式 | 适用命令 | 成功判据 | 超时处理 |
|---|---|---|---|
| `service-command` | `systemctl restart fishd`、启动脚本等短命管理命令 | 命令在看门狗观察窗口内退出，退出码为 0 | 视为失败：终止该命令并 `Wait` 回收，进入冷却 |
| `managed-child` | 直接启动常驻进程，如 `bin/fishd --env-file .env` | 观察窗口内出现**该子进程 pid** 写出的新心跳（不能沿用启动前的旧心跳） | 视为失败：终止该子进程并 `Wait` 回收，进入冷却 |

- 不要把「后台包装脚本」配成 `managed-child`：包装脚本退出后，看门狗拿不到子进程 pid，心跳不会被认可。启动脚本请用 `service-command`。
- 父上下文已取消时看门狗不会再启动新命令；`managed-child` 在观察期内被取消时不会杀死可能已经健康的服务。
- 两种模式都不再把「到观察超时仍在运行」当作成功，避免挂死的 `systemctl`/启动脚本在冷却后反复累积进程与 `Wait` goroutine。

## 7. 日志与备份

| 数据 | 位置（本机） | 位置（Compose） | 保留 |
|---|---|---|---|
| 应用日志 | `logs/fishd-YYYY-MM-DD.log` | `fishd-logs` 卷 | 7 天 |
| 日报 | `logs/daily-YYYY-MM-DD.md` | `fishd-logs` 卷 | 90 天 |
| PG 数据 | 系统 PG 目录 | `pgdata` 卷 | 按表策略 |
| 备份 | `data/backup/` | 宿主机目录 | 7 天 |

```bash
# Compose 备份 / 恢复
docker compose -f deployment/docker-compose.yml exec -T postgres \
  pg_dump -U fish raisemyfish > backup.sql
cat backup.sql | docker compose -f deployment/docker-compose.yml exec -T postgres \
  psql -U fish -d raisemyfish
```

## 8. 升级与回滚

- 本机：`pg_dump` → 替换二进制 → `fishd migrate up` → 重启；回滚二进制并按需恢复备份。
- Compose：`pg_dump` → `git pull` → `make docker-up`（自动重建并重跑幂等迁移）。
- 迁移只前进，不支持 `migrate down`；回滚数据库请用备份。

## 9. 端口

| 服务 | 端口 | 暴露方式 |
|---|---|---|
| PostgreSQL | 5432 | Compose 只绑 `127.0.0.1:5432` |
| fishd 面板 | 8090 | 仅回环；Compose 不发布到宿主机 |
| ASR（外部） | 8077 | 按需，默认只在本机 |
| QQ 协议端 | 6099 / 3001 | 未装配，不启动 |

## 10. 验收

- [ ] 空机器按 README（本机或 Docker）能起
- [ ] `fishd migrate up` 可重复执行
- [ ] 崩溃后由 systemd / Compose 自动拉起
- [ ] 日志、日报可读，凭据不出现在日志
- [ ] PG 备份可恢复
- [ ] 内存占用 < 1GB（不含协议端与 Docker 开销）
