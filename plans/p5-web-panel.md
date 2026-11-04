# P5 Web 面板派发单（可视化控制台）

> 目标：一个本地 Web 面板，能看状态/记忆/活动，不用 fishctl 敲命令。
> 技术：Go net/http + embed 静态页（无前端构建链）；只监听 127.0.0.1。

## 一、路由与 API

### 页面（GET，返回 HTML）

| 路径 | 内容 |
|---|---|
| `/` | 概览：fishd 状态/心跳、events 统计（今日收/发/跳过/失败）、费用、presence、最近错误 |
| `/memory` | 记忆列表（kind/title/summary/importance/时间），支持搜索/筛选 |
| `/activity` | 活动流水（最近事件 + 发送记录，分页） |

### API（JSON）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/status` | 等价 fishctl status（events 统计/费用/presence/心跳/死信） |
| GET | `/api/memory?kind=&q=&limit=` | 记忆列表 |
| DELETE | `/api/memory/{id}` | 删除一条记忆（需确认参数 `?confirm=yes`） |
| GET | `/api/events?limit=&dir=` | 事件流水（in/out） |
| GET | `/api/send_log?limit=` | 发送记录 |

### 页面实现

- 单页 HTML（内联 CSS/JS，无外部 CDN）
- 概览：卡片网格（状态/统计/费用）
- 记忆：表格 + 搜索框 + 删除按钮（fetch DELETE）
- 活动：列表 + 分页
- 每 5-10 秒自动刷新（可选开关）

## 二、配置

```toml
[web]
enabled = false        # 默认关
addr = "127.0.0.1:8090" # 只允许回环地址
```

- `enabled=true` 时 fishd 启动 HTTP 服务（独立 goroutine，可取消）
- 非回环地址配置拒绝（安全）

## 三、安全

- 只监听 127.0.0.1（拒绝 0.0.0.0）
- 无鉴权（本地回环）；文档说明「仅本机」
- 写操作（删记忆）要求 `confirm=yes`
- 不暴露凭据/正文敏感内容（列表截断）

## 四、测试

- httptest：status/memory/events 返回 JSON 结构
- 删除需 confirm（无 confirm 拒绝）
- 非回环地址配置拒绝
- 页面路由返回 200 + 含关键元素

## 五、禁止

- 不改 contracts、不碰调度/发送语义、不引前端依赖、不执行 git

## 六、交付格式

- 文件列表、路由清单、测试结果、配置说明、未做项、是否暂停写入
