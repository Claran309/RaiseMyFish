# P5 Web 面板全套功能设计（配置项目所有功能）

> 目标：项目所有功能都能在 Web 上看到/操作，不用敲命令。
> 现状：概览/记忆/活动三页（已上线）；本设计覆盖其余全部。

## 一、功能映射（项目功能 → Web 模块）

### 1. 直播/通道管理

| 功能 | Web 操作 | 后端 API |
|---|---|---|
| 看直播（watch） | 输入直播间链接/room_id → 开始/停止；显示监控房间/状态 | POST /api/watch/start、/api/watch/stop |
| 直播采集开关 | 显示状态 + 启停（热生效） | /api/channels/{live}/toggle |

## 1.5 消息管理（`/message`，评论/私信领域，与 /live 分开）

| 功能 | Web 操作 | 后端 API |
|---|---|---|
| 评论监听 | 开关 + 视频 oid 列表编辑 | /api/channels/comment/toggle、/api/comment/videos |
| 私信监听 | 开关 + 白名单编辑 | /api/channels/dm/toggle、/api/dm/whitelist |
| **最近评论收发** | 收到的评论 / 已回复的评论列表 | /api/comment/recent |
| **最近私信收发** | 收到的私信 / 已回复的私信列表 | /api/dm/recent |
| 通道健康 | 各通道最近事件时间/失败率 | /api/channels |

### 2. 互动管理（送礼/主动弹幕/点赞）

| 功能 | Web 操作 | API |
|---|---|---|
| 送礼记录 | 最近送礼列表（时间/房间/礼物/结果/额度剩余） | /api/gift/log |
| 送礼配置 | 开关/额度/白名单/礼物选择（热更新） | /api/gift/config (GET/PUT) |
| 送礼额度 | 今日已送/剩余 | /api/gift/quota |
| 主动弹幕 | 记录（内容/时间/是否发送） | /api/proactive/log |
| 主动弹幕配置 | 开关/间隔/触发模式 | /api/proactive/config |
| 点赞 | 记录 + 开关 + 间隔 | /api/like/log、/api/like/config |

### 3. 记忆管理

| 功能 | Web 操作 | API |
|---|---|---|
| 记忆列表/搜索/删除 | 已有 | /api/memory |
| **观众档案** | 列表（排行）、详情（说过什么/鱼怎么回） | /api/viewers、/api/viewers/{uid} |
| **灵魂记忆** | 列表/编辑/手动添加（Manual 标记） | /api/soul |
| **记忆冻结** | 一键冻结/解冻 | /api/memory/freeze |
| 记忆提炼状态 | 最近提炼记录/水位 | /api/memory/extract |

### 4. 知识库管理

| 功能 | Web 操作 | API |
|---|---|---|
| 梗列表/搜索 | 分页/搜索/筛选（标签/来源） | /api/knowledge |
| **梗编辑** | 改 title/usage/tags（写 learned.md 或 batch） | PUT /api/knowledge/{id} |
| **梗新增** | 手动加一条（Manual） | POST /api/knowledge |
| **梗删除** | 删除（软删或写 learned） | DELETE /api/knowledge/{id} |
| **重新 embedding** | 手动触发向量化 | POST /api/knowledge/embed |
| 梗白学习 | 学习记录（学了什么/为什么） | /api/knowledge/learned |
| 召回测试 | 输入一句话看召回什么（调试） | POST /api/knowledge/recall |

### 5. 事件与日志

| 功能 | Web 操作 | API |
|---|---|---|
| 事件列表 | 已有（方向筛选/分页） | /api/events |
| **事件详情** | 点开看完整（上下文/回复/元数据） | /api/events/{id} |
| **实时日志** | tail fishd 日志（SSE 推送） | /api/logs/stream (SSE) |
| 发送记录 | 已有 | /api/send_log |

### 6. 配置与健康

| 功能 | Web 操作 | API |
|---|---|---|
| **常用配置编辑** | 开关类（主动弹幕/送礼/记忆冻结/热重载…）热更新 | /api/config (GET/PUT) |
| 费用统计 | 已有（累计） | /api/cost |
| **Watchdog 状态** | 心跳/重启历史 | /api/watchdog |
| 健康检查 | 各依赖状态（DB/采集器/Brain/知识库） | /api/health |
| 人格查看 | 当前人格包摘要（soul/rules 版本） | /api/persona |

## 二、安全

- 只监听 127.0.0.1（已有）
- 写操作（配置/删除/送礼）要求 `confirm=yes` + 前端二次确认
- 配置编辑**只允许白名单字段**（不能改凭据/路径）
- 日志 tail 流式（SSE），不落盘

## 三、页面组织

```
/            概览（状态/统计/最近事件/错误/健康）
/live        直播管理（看直播/watch/直播采集/通道健康）
/message     消息管理（评论/私信监听 + 最近收发）
/memory      记忆（列表/观众档案/灵魂记忆/冻结）
/knowledge   知识库（列表/编辑/召回测试/embedding）
/activity    活动（事件/发送/送礼/主动弹幕/点赞）
/logs        实时日志
/config      配置编辑
```

## 四、实施建议

- 分批：先「只读」（礼物/主动弹幕/观众档案/知识库列表/事件详情/健康）→ 再「写操作」（配置编辑/知识编辑/冻结/embedding）
- 写操作全部走白名单字段 + confirm
- 遵循 AGENTS.md 规则：新 API 配 httptest、页面配关键字断言

## 五、暂不做

- 多实例管理
- 实时聊天（那是 B站的事）
