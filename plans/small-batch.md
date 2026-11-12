# 小单批次（2026-11-12）

## 批 P1：#2 点赞路径确认

**问题**：点赞用 `room/v1/Room/like`——B站还有 `xlive/web-ucenter/v1/roomAdmin/like`——**不确定哪个有效**。

**改法**：验收时看 `code != 0` 就改常量（无需重构）。

## 批 P2：#4 私信会话翻页

**问题**：`get_sessions` 只取首屏 20 会话——消息多的会话漏。

**改法**：`get_sessions` 翻页（end_ts 游标）——拉更多会话——消息多的会话用 `get_session_msgs` 拉详情。

**验收**：会话数 >20 的场景能拉到全部。

## 批 P3：#5 评论多级回复

**问题**：采集的 root/parent 只存 Meta；回复统一 root=parent=rpid（单级）。

**改法**：回复用真实 root/parent（多级：回复楼中楼时 parent=被回复评论的 rpid，root=根评论 rpid）。

**验收**：回复楼中楼时用真实 parent（不是一个 rpid 走天下）。

## 批 P4：#8 多 persona 支持

**问题**：soul_path 固定 dafeiyu——多 persona 时按 pack 派生。

**改法**：soul_path 按 persona pack 派生（`personas/<name>/soul.md`）。

**验收**：配置多个 persona 时各自 soul 正确。

## 不派

- #59 发送异步化（架构—主人没让派）
- #13/#14 web_search（主人还没配 API）
- #24 Edge 截图（主人取消）

## 交付状态（2026-11-12）

- 批 P1：已交付。真实探针：`room/v1/Room/like` 返回 code 1000003（方法 like 未找到）、`xlive/web-ucenter/v1/roomAdmin/like` HTTP 404；改用 `xlive/app-ucenter/v1/like_info_v3/like/likeReportV3`，参数 `click_time/room_id/anchor_id/uid/csrf`（主播 UID 由 `room/v1/Room/get_info` 取）。项目客户端真实调用返回 code=0；补请求参数、缺本账号 UID、主播 UID 拉取失败/缺失的回归。
- 批 P2：已交付（前序已实现，本批补验收）。`get_sessions` 按 `end_ts` 微秒游标翻页，默认 5×100；逐会话用 `fetch_session_msgs` 拉详情。解禁 25 会话跨页测试（10+10+5，每个会话各拉一次详情，重启不重复）；生产 `state/dm-cursors.json` 已跟踪 77 个会话，证明 >20 场景可用。
- 批 P3：已交付（前序已实现，本批核对）。采集把真实 `root/parent` 写入 Meta；发送时 `root=Meta.root`（顶楼/0 回落到本条）、`parent=被回复评论的 rpid`，不再 root=parent=rpid。既有 `TestSendUsesCollectedRoot` / `TestSendZeroRootFallsBack` 覆盖。
- 批 P4：已交付（前序已实现，本批补验收）。`soul_path` 留空时按 `[llm].persona_dir` 派生 `<persona_dir>/knowledge/soul-memories.md`；显式 `soul_path` 优先。新增 `personas/dafeiyu` 与 `personas/dafeiyu-v2` 各自派生的回归。
- 门禁：`go build ./...`、`go test ./...` 全绿；`internal/bili`、`internal/config`、`internal/collector/dm`、`internal/sender/comment`、`cmd/fishd` 相关用例通过。

## 批 Q/R/S 交付状态（2026-11-12）

### 批 Q：#87 四感与取流诊断（已交付，待 DSH 开播验收）

- 声音：`HTTPTranscriber` 错误带 ASR 端点主机与底层原因；新增 `watch.CheckASR`（`/health`，2s 超时）并在 fishd 启动时探测（`action=asr_health`）。`start-fishd.bat` 与稳定版 `start.bat` 都会先探测 8077，未启动则调用 `~/.dsh/skills/asr-server/start-asr.bat`，最多等 60 秒；失败只告警不阻塞。
- 取流：`StreamRecorder` 取流失败时查询 `room/v1/Room/get_info` 的 `live_status`，区分「直播未开播（live_status=0）」「直播进行中但没有可用流（live_status=%d）」「开播状态查询失败」，错误不泄露 SESSDATA。
- 验收点（开播后）：①启动日志 `action=asr_health` 且「ASR服务可用」；②`logs/` 无 `action=proactive_audio` 告警，ASR 服务日志有 `/v1/audio/transcriptions` 2xx；③主动弹幕 `action=proactive_react` 的 `tools_used` 含 `watch_live`，返回内容含「画面/主播刚说/最近弹幕」；④被动弹幕 `action=react` 的 `steps` 与 `llm_usage` 次数包含反思步（无 `react_reflect_skip` 时）。

### 批 R：#88 性能与延迟（已交付）

- 回复延迟统计：`Stats.Latency` 与 Web 面板 `/api/status` 的 `latency`、`fishctl status` 的 `stats.latency` 同口径（近 24h，入向事件首次成功发送，p50/p90/avg/max）。真实 PG 集成测试覆盖窗口、最早一次成功、channel 空回落、out/负延迟/qq 干扰。
- 连接池：`[db] max_open/max_idle/conn_max_lifetime_seconds/slow_query_ms` 可配（默认 30/10/1800/200），面板 `db_pool` 暴露 open/in_use/idle/max_open/wait_count/wait_duration。
- 慢查询：GORM logger 转 zap `action=db_slow`，`ParameterizedQueries=true`，不打印参数值。
- 预算调优（先测后调）：近 24h 实测 LLM 单次调用 p90 live 2.33s / comment 1.69s / dm 1.70s；`[reasoning.comment]`、`[reasoning.dm]` 从 45s 收到配置下限 30s（5 步约 8.5s，仍 3.5 倍余量），直播 3 步/8s 维持。DM 延迟主因是 120s 轮询、评论是 60s 轮询，模型预算不是瓶颈。

### 批 S：#89 稳定版（已交付）

- 版本号：`cmd/fishd` 的 `version` 由 ldflags 注入（`-X main.version`），新增 `fishd version`，状态文件与 `fishctl status` 的 `service.version` 同源；默认 `dev`。Web 面板暂未展示版本（未做项）。
- 打包：`make release VERSION=0.1.0` → `scripts/build-dist.sh` 构建 `dist/fishd.exe`、同步配置/人格/README，生成 `fishd-stable-0.1.0.zip`；稳定版 `start.bat` 为 exe 一键启动（ASR 探测 + 面板自动打开）。
- 文档：README 快速启动与 `docs/13-deployment.md` 增补一键启动、稳定版打包、连接池/慢查询/预算口径。

### 门禁与顺手修复

- `go build ./...`、`go vet ./...`、`go test ./... -count=1`、`-race`（改动包）、`go test -tags=integration ./internal/database` 全部通过。
- 顺手修复：`internal/memory/fact_test.go` 的不可达死代码导致 `go vet` 失败（删除）；`database` 事实集成测试改用测试注入的 fact 提炼器，匹配「规则版不再产 fact、事实交 LLM」的现状，断言未放宽。
