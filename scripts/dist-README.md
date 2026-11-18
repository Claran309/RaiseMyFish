# RaiseMyFish 稳定版

一条会看直播、回弹幕/评论/私信、有记忆与知识库的 AI 陪伴体。本包是可直接运行的 Windows 稳定版。

## 包内容

| 路径 | 说明 |
|---|---|
| `fishd.exe` | 主服务（版本用 `fishd.exe version` 查看） |
| `start.bat` | 一键启动：拉起本机 ASR（若存在）→ 启动 fishd → 打开面板 |
| `config/fishd.toml` | 运行配置（房间/账号/通道开关/预算/连接池） |
| `personas/` | 人格包与知识库 |
| `scripts/cookie.py` | 从本机 `.bili-ai/credentials.json` 构造 B站 cookie（可选） |
| `.env.example` | 环境变量模板（复制为 `.env` 后填真实值） |

## 依赖

- PostgreSQL 17 + pgvector（必须）
- Node 24（直播弹幕采集/送礼脚本需要；不启用可忽略）
- ffmpeg / ffprobe（看直播、主动弹幕的声音/画面采样需要）
- 本机 ASR 服务 `127.0.0.1:8077`（可选；未启动时声音感知自动降级，不影响回复）

## 首次启动

1. 复制 `.env.example` 为 `.env`，至少填 `FISH_DB_DSN`、`FISH_LLM_KEY`；知识向量化还需 `FISH_EMBEDDING_KEY`。
2. 初始化数据库：`fishd.exe --env-file .env migrate up`
3. 双击 `start.bat`。脚本会等待面板端口后自动打开 <http://127.0.0.1:8090>。
4. 关闭：关掉 fishd 窗口，或使用面板右上角的一键关闭。

> `start.bat` 会优先读取 `scripts/cookie.py` 生成的 B站 cookie；也可以在 `.env` 里直接设置 `FISH_BILI_COOKIE` / `FISH_BILI_COOKIE_FILE`，或使用面板的账号管理。

## 常见问题

- **端口 8090 没起来**：看 fishd 窗口报错；多数是 `.env` 的 DSN 不通或数据库未迁移。
- **声音感知降级**：启动日志出现 `action=asr_health` 且 `ASR服务不可用`；启动本机 ASR 后重启 fishd。
- **直播取流失败**：日志会区分「直播未开播」与「直播进行中但没有可用流」；后者检查 cookie 是否过期。
- **想调整性能**：`config/fishd.toml` 的 `[db]` 连接池、`[reasoning]` 各通道预算均可在重启后生效。
