# Phase 0 · 弹幕单线闭环

> 目标：**在真实直播间稳定回弹幕**，同时把人格、记忆、观测的最小版立起来。
> 这是唯一可开工的阶段。三线顺序：先弹幕，评论/私信留到 Phase 1。

## 依赖

无（从零开始）。

## 开工前置（环境检查）

- [ ] Go 1.25+（本机 1.27 可用）
- [ ] PostgreSQL 17+ 已安装并运行在 5432（**当前未检测到，需先装/启动**）
- [ ] `golangci-lint` 已安装（CI 用；本地可先用 `go vet` 代替）
- [ ] B站凭据可用：`bili` 在 `~/.dsh/bili-cli/.venv/Scripts/bili.exe`（配置路径，不必进 PATH）；弹幕监听用 `node <dsh-bili-whale>/tools/commands/danmaku.mjs`
- [ ] `.env` 从 `.env.example` 复制，填 `FISH_DB_DSN` / `FISH_LLM_KEY`
- [ ] 分支：远端默认是 `master`，CI 已监听 `main` / `master` / `dev` 任一

## 任务清单

### 0.1 仓库骨架与基础设施
- [x] Go module 初始化，目录结构按 `README.md`
- [x] 配置加载（TOML + 环境变量覆盖 + 校验）
- [x] 结构化日志（zap，JSON，落文件 + stderr）
- [x] `cmd/fishd` 入口：装配依赖、优雅退出
- [x] `cmd/fishctl` 入口：至少 `status` / `tail`
- [x] `migrations/0001_init.sql` 建表（events/tasks/memory_sources/memory_records/llm_usage/send_log/dlq）

0.1结果：364dcb3已推送；DSH COMM-004确认manifest31/31及单测/真实PG/迁移/运行路径通过，Codex另有Linux race和lint证据。强杀后的即时存活检测留到0.11；当前默认20秒心跳判定不变。

### 0.2 store 层
- [ ] repository 接口（EventRepo / TaskRepo / MemoryRepo / UsageRepo）
- [ ] PG 实现（GORM + pgx 驱动；幂等/状态机用 Raw SQL）
- [ ] 幂等插入测试
- [ ] 状态迁移测试

### 0.3 事件总线
- [ ] `Publish` 幂等入库
- [ ] `ClaimPending`（单进程版；预留 FOR UPDATE SKIP LOCKED）
- [ ] `Mark` 带条件状态迁移
- [ ] `Notify` 进程内 channel
- [ ] 崩溃恢复：replying 超时 → queued

### 0.4 调度器
- [ ] 五道闸：ACL / 噪音 / 幂等并发 / 节流 / 优先级
- [ ] 优先队列（container/heap）
- [ ] 冷却（time.AfterFunc）
- [ ] 令牌桶（x/time/rate，按通道配置）
- [ ] 会话锁（同 session 串行）
- [ ] 每日配额
- [ ] 单测覆盖每一闸

### 0.5 弹幕采集器（live）
- [ ] **采用 dsh-bili-whale 实现**（`tools/commands/danmaku-await.mjs`，WS 长连接）→ 复制进 `tools/bili-whale/`，去掉 DSH 依赖，子进程跑
- [ ] 标准化事件 + 幂等键
- [ ] 自消息过滤（self id）
- [ ] 断线重连（指数退避）
- [ ] Health 上报

### 0.6 Brain（OpenAI 兼容）

已按主人授权放行开发；Memory最小类型P0-MEMORY-1、范围校验与允许文件见contracts/memory.md和plans/phase-0.6-dispatch.md。0.6可补自身UsageRepo最小依赖切片并真实PG验证，不把0.2其余repository或0.9召回标为完成。
- [x] `Brain` 接口实现
- [x] 上下文组装：system(persona) + user(context/event/instructions)
- [x] 预算截断（先裁记忆、后裁 persona，soul 保留）
- [x] 超时/重试/失败分类
- [x] `llm_usage` 记账

0.6结果：DSH独立验收32/32文件与真实PG/关键行为通过；已提交并推送b51f444。

### 0.7 人格包与回归
- [x] `personas/dafeiyu/` 包结构（meta/soul/rules/samples/context）
- [x] 从 `~/.bili-ai/persona.md` + `persona-shared.md` + `persona-live.md` 迁移
- [x] `eval/cases.jsonl` ≥ 30 条（直播场景为主）
- [ ] `fishctl persona eval` 命令（已实现并真实实测，评估器代码待DSH复核）
- [ ] 回归通过率 ≥ 90%（本轮真实35条15/35=42.9%，未满足）

0.7a素材/35条用例已接收32651ba；0.7b静态加载器已提交5ece97f（DSH原交付验收，Codex三项裁决/增量见COMM-006）。0.7c真实运行见下文，CLI新实现仍待DSH复核，不以baseline回放替代。

0.7c本轮真实运行已完成：run 20261031T105248Z-501d0282，dafeiyu@v1，请求deepseek:deepseek-chat、服务端标识deepseek-flash；原始/投递文本均42.9%，35次调用全部成功并有PG核账。不修改旧结论凑门槛，整体0.7/人格退出标准保持未完成，证据见plans/reports/persona-eval对应目录。

### 0.8 弹幕发送器
- [ ] **采用 dsh-bili-whale 实现**（`tools/commands/danmaku.mjs`，`POST api.live.bilibili.com/msg/send`）→ 复制进 `tools/bili-whale/`，子进程跑
- [ ] 核验（回执/回读）
- [ ] 限流（令牌桶）
- [ ] `send_log` + `out` 事件
- [ ] 失败分类（retry / dead / skipped）

### 0.12 看直播（截图 / 听声）— MVP 范围（主人选 C）
- [ ] 直播流/录屏 → ffmpeg 抽帧（参考 `rec_desktop`/`cf_every` 的参数与输出）
- [ ] 多模态模型读帧（需配支持视觉的模型）
- [ ] ffmpeg 抽音频 → ASR（faster-whisper 本地服务或 SenseVoice）
- [ ] 文件式流水线：录 10 秒 → 抽帧 → 读图 → 转写（延迟 20~40 秒是已知边界）
- [ ] 失败降级：黑屏/无音轨告警，不阻塞弹幕闭环
- [ ] 参考实现与能力边界：`docs/07-channels.md` §9 + `D:\B站直播能力清单_给群友agent.txt`

### 0.13 配套能力（按 dsh-bili-whale 对齐）
- [ ] 送礼（`gift.mjs` 思路：小花花 1 电池）——按需搬
- [ ] 常驻 Edge（`presence.mjs`：截图/点赞）——按需搬
- [ ] 跨链路活动流（`activity.mjs`）——记录「在看/在回/在送」状态，接 Presence
- [ ] 梗白学习（`meme-learn.mjs`）——可选，P2 知识库联动

### 0.9 记忆最小版
- [ ] 发送后写 `out` 事件（必写）
- [ ] 简单提炼 worker（先规则版：主人相关/承诺/首次出现）
- [ ] 写入 `memory_records`（importance/confidence）
- [ ] 召回：最近会话滑窗 + 跨通道事件（P0 不做语义）

### 0.10 可观测
- [ ] 结构化日志（event_id/ch/action/result/cost_ms）
- [ ] 心跳：单通道 30 分钟无事件 → warn
- [ ] 日报：收/发/跳过/失败/费用
- [ ] `fishctl status` 显示各组件健康

### 0.11 运行与收尾
- [ ] `scripts/dev.ps1` 一键启停
- [ ] 内存占用 < 300MB（不含 PG/协议端）
- [ ] README 补充「如何跑起来」
- [ ] 本阶段任务状态更新

## 退出标准（必须全部满足）

1. 真实直播间连续运行 **2 小时**：不回重、不漏、不崩
2. 重启进程：待处理事件不丢、已发消息不重发
3. 人格回归通过率 **≥ 90%**
4. 日报可读，能看出「回了多少、跳过多少、花了多少」
5. 静默失败检测生效（人为断开采集 30 分钟 → warn）
6. `go test ./...` 全绿
7. **看直播（0.12）**：录 10 秒 → 抽帧读图 → ASR 转写，黑屏/无音轨有明确告警
8. **配套（0.13）**：送礼/常驻 Edge/活动流按需可用，活动流接 Presence

## 风险与应对

| 风险 | 应对 |
|---|---|
| bili-cli 子进程不稳定 | 优先子进程复用；失败则评估自写 WS |
| B站弹幕频控 | 令牌桶 + 冷却 + 每日配额 |
| prompt 缓存失效导致成本高 | 严格按「静态 system / 动态 user」组装 |
| 人格迁移后语气漂移 | 先原样迁移，再按回归集微调 |

## 待定（实现中记录）

- P0-G01（0.2/0.3/0.4 前，Codex 裁决）：`ClaimPending` 已把事件改为 queued，但调度文档仍要求 pending 上过滤/延期，且合法迁移无 queued→skipped；需统一抢占、过滤、retry_at 和任务原子创建的语义后再冻结接口。
- P0-G02（0.2/0.3 前，Codex 裁决）：恢复要求重新领取 queued/retry，而 Bus 只有 ClaimPending；超时恢复也缺少明确的状态起始时间/持有权依据。需确定领取、超时字段与补偿阶段标识，不得仅按事件平台时间判断 worker 已失效。
- P0-G03（0.5/0.8 前，Codex 设计，WorkBuddy 验证平台）：稳定平台消息 ID、失败 out 事件幂等键、发送回执/核验信息的持久化，以及「平台已收但本地尚未标 sent」的崩溃窗口仍未定义；只补 sent 超时规则不足以证明重启不重发。out 唯一写入责任按 coordination，memory 不重复写。
- P0-G04（类型已裁决）：P0-PACK-1加载器已接收，Memory最小上下文冻结为contracts/memory.md的P0-MEMORY-1；回归执行接口仍须0.7c前确认。0.6不等待完整回归，真实人格≥90%仍等0.6，mock仅测试执行器；Reader实际查询/隐私SQL仍待0.9。
- P0-G05（0.1 验收前，Codex 负责，DSH 验证）：CI 的 job 级 hashFiles 检查在 checkout 前执行，需核实表达式支持与空仓保护位置；lint action/latest 与现有配置版本需对齐；真实 PG 集成 job 仍被注释，功能验收前启用并实际执行。
- P0-G06（0.11 前，Codex 拆任务、WorkBuddy 修脚本、DSH 验收）：dev.ps1 已修 PID 命名，但仍需创建日志目录、避免 go run 父子进程生命周期遗漏、落实优雅退出/重复启动检查；补充验证命令并记录未通过项。
- P0-G07（0.11增强，Codex裁决，DSH复验）：0.1暂不追加单独PID存活校验，强杀后在线判断按默认20秒心跳时效；service.state是最后记录。0.11增强需同时考虑PID复用/进程创建身份与心跳/PG锁信息，设计方案另行明确，不能仅用os.FindProcess成功认定为同一实例。
- 弹幕采集用子进程还是自写 WS：0.5 先验证子进程，不行再自写
- 提炼 worker 的触发频率：先每 5 分钟扫一次

## 完成记录

> 每完成一项，勾选并写一句结果。格式：`[x] 任务 — 结果（验证方式）`

- [x] 0.1 — 已实现基础设施并推送364dcb3（DSH COMM-004独立验收；单测、真实PG、迁移幂等和运行检查）。
- [x] 0.7a/0.7b — 素材与用例32651ba、静态加载器5ece97f；DSH独立验收与正文边界增量复核均通过。
- [x] 0.6 — 已验收并推送b51f444（DSH 32/32快照与关键路径独立验收）。
- [ ] 0.7c退出门槛 — 真实运行35/35完成，但本轮词法通过率42.9%未达到90%；评估器新代码/产物待DSH复核。
