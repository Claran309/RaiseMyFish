# 接真实 B站客户端派发单（执行器落地）

> 目标：把 ReAct 工具的 nil 执行器换成真实 B站客户端（带 cookie），护栏不变。
> 凭据：现有 `FISH_BILI_COOKIE`（大肥鱼账号，主人授权）；风控底线见第四节。

## 一、执行器接口实现

### 只读

| 接口 | 实现 | API |
|---|---|---|
| `LiveStatusSource` | 直播间状态 | `room/v1/Room/get_info`（room_id）；返回 live_status/title/online |
| `WebSearcher` | 查证 | 未配置搜索源时返回「未配置」（可先 stub，config 加 `[web] search_url` 可选） |
| `vision` 帧源 | 复用 watch.Snapshot + VisionDescriber | 已有（cmd 侧装配） |

### 副作用（全部已有护栏）

| 接口 | 实现 | API | 备注 |
|---|---|---|---|
| `GiftExecutor` | 送礼 | 已有 `internal/gift` Sender（gift.mjs） | dry-run/real 由 `[gift]` 决定 |
| `SocialExecutor` | follow/unfollow | `relation/modify`（fid + act + csrf + re_src） | 带 cookie/csrf（bili_jct） |
| `LikeExecutor` | 点赞 | `room/v1/Room/like` 或 `xlive/web-ucenter/v1/roomAdmin/like` | 带 cookie |
| `DMExecutor` | 私信 | 已有 bili-cli 或 `msg/send` | 只发主人/白名单（护栏已有） |
| `DynamicExecutor` | 发动态 | `dynamic/create` | **默认关**（`post_dynamic` 独立开关） |

## 二、装配（cmd/fishd）

- `serve.go`/`proactive.go`：把 nil 换成真实执行器（env 有 cookie 才装配；无 cookie 保持 nil + 日志「未配置凭据」）
- 日志：`action="tool_executor_setup"`（装配了哪些/缺哪些）
- vision：cmd 侧接 watch recorder（复用主动弹幕的 Snapshot 源）

## 三、风控底线（必须遵守）

1. **限频**：follow/like/dm/post_dynamic 已有护栏（1 分钟/10 分钟限频），不得放宽
2. **首次运行 dry-run 优先**：`[gift] real=false`、`post_dynamic` 默认关——先观察日志
3. **失败不重试**（避免触发风控）
4. **凭据缺失**：不装配执行器（返回「未配置」），不报错
5. **日志不打凭据/正文**（只打工具名/参数摘要/结果）

## 四、测试

- mock 平台客户端（httptest）：断言 URL/method/参数/csrf
- 保持既有护栏测试全过（额度/限频/黑名单/不取关主人）
- 真实平台调用留验收时（不写进测试）

## 五、禁止

- 不改 contracts、不碰调度/发送语义、不执行 git
- 不真实调用平台（测试 mock）

## 六、交付格式

- 文件列表、执行器清单（已接/未接）、测试结果、配置说明、未做项、是否暂停写入
