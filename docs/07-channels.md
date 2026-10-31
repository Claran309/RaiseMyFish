# 07 · 通道设计（Collector / Sender）

> 每个通道 = 一个 Collector（收）+ 一个 Sender（发）。
> 平台差异只存在这一层，核心逻辑不感知平台。

## 1. 通道清单

| 通道 | Phase | 采集方式 | 发送方式 | 关键风险 |
|---|---|---|---|---|
| `live` 直播弹幕 | P0 | 长连 WebSocket | HTTP API | 频控（10s/条）、风控 |
| `comment` 评论 | P1 | 收件箱轮询 | HTTP API | 状态核验（state 0/17） |
| `dm` 私信 | P1 | 等待回复轮询 | HTTP API | 隐私、频率 |
| `qq` QQ | P3 | OneBot 客户端 / AstrBot | 同左 | 协议端掉线、封号 |

## 2. 直播弹幕（live）

### 采集
- 长连 WebSocket 或复用 `danmaku-await` 子进程
- 过滤自己发的消息（self id）
- 标准化：`live:{room}:{msg_id}`

### 发送
- 频率：10 秒/条（冷却 + 令牌桶双保险）
- 长度：≤ 40 字
- 核验：发送后回读/回执，失败进 retry

### 特殊
- 被 @ / 叫名字 → 优先级 P1
- 主播本人发言 → 单独标记（可触发主动互动）
- 礼物事件 → 可触发感谢（P1 起）

## 3. 评论（comment）

### 采集
- 轮询收件箱（回复我的 / @我的 / 动态评论）
- 幂等键：`comment:{oid}:{rpid}`
- 记录 `root` / `parent` 用于楼中楼回复

### 发送
- 顶层 / 楼中楼两种（`--root` 参数）
- 发送后核验：回读该评论，确认 `state == 0`
- `state 17`（仅自己可见）→ 记录、不重发、告警
- 评论区关闭（12002）→ skipped

### 特殊
- 视频 @ 必须先「看懂视频」再回（可看画面/字幕，P1）
- 长度 ≤ 200 字

## 4. 私信（dm）

### 采集
- 轮询「等回话」的会话（复用 `dm-await` 逻辑）
- 幂等键：`dm:{uid}:{msg_id}`
- 只处理主人白名单，其余进待审

### 发送
- 文本，≤ 500 字
- 频率：10 秒/条
- 回复后回读确认

### 隐私
- `private=true`，任何公开通道不可见（查询层强制）

## 5. QQ（qq）

### 方案选择（P3 决策点）

| 方案 | 优点 | 缺点 |
|---|---|---|
| 自写 OneBot 客户端（Go） | 架构统一、收发可控 | 需处理 WS 重连/协议细节 |
| AstrBot HTTP API 转发 | 省事、有 WebUI | Python 异构、需收消息转发插件 |
| **参考 lanmei-dream 的 gateway** | 已有 OneBot 11/12 反向 WS 实现（`internal/gateway/`，`lxzan/gws`） | 需按本项目契约适配 |

**推荐**：自写 OneBot 客户端放 `pkg/onebot` / `internal/gateway`，协议端（NapCat/LLBot）现成。
WS 库到 P3 再选型（可参考 lanmei 的 `internal/gateway/` 与 `lxzan/gws`），现在不锁。

### 设计要点
- **协议端可替换**：客户端只依赖 OneBot v11 JSON 协议
- **断线重连**：指数退避，重连后补拉离线消息（协议端支持时）
- **降级**：协议端掉线不影响其他通道；QQ 通道挂起并告警
- **私聊 = private**；群聊只在被 @ 时处理

## 6. 通用要求（所有通道）

| 要求 | 说明 |
|---|---|
| 幂等 | 消息 ID 稳定 |
| 超时 | 所有 HTTP/WS 调用设超时 |
| 重试 | 指数退避，上限 3 次，进死信 |
| 核验 | 发送后必须确认（回读/回执） |
| 记账 | 发送成功/失败都写 `out` 事件 |
| 日志 | 结构化：event_id / ch / action / result / cost_ms |
| 限流 | 每通道令牌桶 + 每日配额 |
| 凭据 | cookie/secret 从环境变量或本地加密文件读，不落日志 |

## 7. 采集器接口

```go
type Collector interface {
    Name() string
    // Start 启动采集循环，阻塞直到 ctx 取消；错误经 errc 上报后继续重连。
    Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error
    Health() Health
}
```

- 每个 collector 独立 goroutine
- 断线重连由 collector 内部负责
- `Health` 供可观测层探活（「多久没收到事件」= 静默失败信号）

## 8. 发送器接口

```go
type Sender interface {
    Name() string
    Send(ctx context.Context, reply Reply, ev Event) (SendResult, error)
}

type SendResult struct {
    OK        bool
    MsgID     string
    Verified  bool   // 核验通过
    Error     string
}
```

- sender 不重试（重试由上层任务队列负责），只返回结果
- 核验失败必须如实返回 `Verified=false`

## 9. 直播通道实现参考（dsh-bili-whale）

现有大肥鱼在 DSH 里的直播链路实现位于
`C:\Users\Claran\.dsh\profiles\desktop\node_modules\dsh-bili-whale\`（v0.4.5，Node/ESM）。
本项目的 live 采集/发送可以据此搬运或重写，不必从零验证协议。

### 四段链路

| 环节 | 文件 | 实现要点 | 是否依赖 DSH |
|---|---|---|---|
| 监听 | `tools/commands/danmaku-await.mjs`（418 行） | **WS 长连接**（心跳 30s、收帧计数）→ 命中写 pending-hits | ❌ 只用 Node 标准库 + config |
| 发送 | `tools/commands/danmaku.mjs`（312 行） | `POST api.live.bilibili.com/msg/send`（zlib/crypto 签名）+ 防重 10s + outbox | ❌ 只用 Node 标准库 |
| 调度 | `lib/scheduler.js`（1088 行） | 5 秒扫队列 → 拼 prompt → 分给 worker | ✅ 依赖 DSH agent 会话 |
| 宿主壳 | `lib/index.js` | 设置页/配置/依赖体检 | ✅ 依赖 DSH，**不搬** |

### 配套件（按需）

| 功能 | 文件 |
|---|---|
| 送礼（小花花 1 电池） | `tools/commands/gift.mjs` |
| 常驻 Edge（截图/点赞） | `tools/commands/presence.mjs` |
| 跨链路活动流 | `tools/commands/activity.mjs` |
| 梗白学习 | `tools/commands/meme-learn.mjs` |
| 插件挂载入口 | `lib/index.js` |

### 搬运方案

| 方案 | 做法 | 工作量 |
|---|---|---|
| **A. 复制 Node 脚本**（推荐） | `danmaku-await.mjs` + `danmaku.mjs` 复制进 `tools/bili-whale/`，去掉 DSH 引用，作子进程 | 小 |
| B. Go 重写 | WS 监听 + `POST /msg/send`（参考 bililive-go） | 中（500~800 行） |
| C. 整插件搬 | 连宿主壳一起搬 | 大，且带 DSH 依赖 |

### 结论

1. WS 长连接 + `POST /msg/send` 是现成的，已验证能长时间稳定发弹幕
2. **调度 1088 行不用搬**——RaiseMyFish 的 Scheduler/Brain 替代
3. 采集/发送优先 A 方案（子进程），Go 重写作为备选
4. 不把 DSH 宿主壳搬进项目

## 9. 测试要求

- 每个通道：mock 平台响应，测试成功/失败/核验失败三条路径
- 幂等：同一消息重复投递只处理一次
- 断线：模拟长连断开，验证重连与补拉
- 降级：协议端不可用时其他通道不受影响
