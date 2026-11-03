# 私信通道（P1）派发单

> 目标：私信采集 + 回复，接入现有调度/Brain 管线（channel=dm），与直播/评论并列。
> 背景：DSH dm.mjs 已有私信发送（一天 1 收信人/只发主人/互关），本批做完整通道。

## 前置

- 参考 `internal/channel/channel.go`（Channel 接口）与 `internal/channel/comment/comment.go`（通道适配范例）
- B站私信 API 需要 cookie + csrf（`FISH_BILI_COOKIE`）
- 私信风控极严（判断营销/骚扰）——**默认关**，护栏优先

## 一、采集（收到私信）

`internal/collector/dm/dm.go`：
- 轮询私信会话：`GET /msg/sessions`（或 bili-cli 等价接口）→ 解析最近会话/消息
- 只取「未读/新消息」→ 按 msg_id 去重 → 写事件（channel=dm、Session=对话 mid、Text=内容、FromID/FromName）
- 配置 `[collector.dm]`：enabled（默认 false）、interval_seconds（默认 120，10-3600）
- 采集失败：auth 错误停采集，其余等下一轮

## 二、发送（回复私信）

`internal/sender/dm/dm.go`：
- 发送：复用 bili-cli dm.mjs 或 `POST /msg/send`（需 cookie/csrf）
- **护栏**：
  - 只允许发给「主人 + 白名单」（`[dm] allowed_mids`，默认主人 mid）
  - 一天最多 1 个收信人（防刷量，DSH 教训）
  - 限频：10 分钟 1 条
  - 互关检查（relation.attribute === 6，非互关拒发）
- 配置 `[sender.dm]`：enabled（默认 false）
- 错误分类：风控/权限 → auth/retry；未知 → uncertain（不自动重投）

## 三、通道适配

`internal/channel/dm/dm.go`：实现 Channel（Collect/Send），注册到 Registry。
`sendRouter` 按 `claim.Event.Channel == dm` 分发。

## 四、测试

- 采集：mock 会话 JSON → 解析/去重/未读过滤/配置校验
- 发送：mock 鉴权/白名单拦截/收信人当日锁定/限频/互关检查/错误分类
- 通道：转发与停止
- 不用真实平台（mock）

## 五、禁止

- 不改 contracts、不碰调度/Brain 语义、不执行 git
- 私信默认关，所有护栏不得放宽

## 六、交付格式

- 文件列表、测试结果、配置说明、未做项、是否暂停写入
