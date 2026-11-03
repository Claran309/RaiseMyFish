# ReAct 工具一次性补齐派发单（P6 工具层）

> 目标：把最终版工具清单一次补齐（只读 7 个 + 副作用 5 个），走统一 Toolbox。
> 副作用工具全部带护栏（额度/限频/黑名单/默认关）。

## 一、只读工具（7 个）

| 工具 | 参数 | 返回 | 数据源 |
|---|---|---|---|
| `viewer_memory` | from_id（纯数字）、limit(1-20) | 该观众最近 N 条消息 + 记忆记录 | events 按 from_id |
| `reply_history` | limit(1-20) | 最近 N 条 out 事件（自己发过什么） | events dir=out |
| `live_status` | 无 | 房间是否在播/标题/在线人数 | B站 room/get_info（带 cookie） |
| `quota` | 无 | 今日已送礼物数/剩余额度/电池余额（如有） | gift QuotaStore |
| `now` | 无 | 当前时间（ISO + 时区） | 本地时钟 |
| `vision` | 无 | 当前直播画面描述（复用 watch.Snapshot + VisionDescriber） | watch 包 |
| `web_search` | query（≤100 字） | 搜索结果摘要（≤500 字） | 需配置搜索源（可先返回「未配置」，不硬编） |

约束：只读、结果截断 ≤500 字、参数校验、不写库。

## 二、副作用工具（5 个，全部护栏）

| 工具 | 参数 | 护栏 |
|---|---|---|
| `gift` | room、gift_id、num | `[gift]` 白名单/价格/数量/每日额度（复用 QuotaStore）；超额度返回「额度不足」；enabled=false/real=false 只返回 dry-run 说明 |
| `follow` | mid、action（follow/unfollow） | 限频 1 分钟 1 次；单次最多 5 个；**永不取关主人（mid 313591854）**；不取关自己 |
| `like` | room、count(1-3) | 限频 1 分钟 1 次（点赞接口，带 cookie） |
| `dm` | mid、text(≤200 字) | 只能发给主人/白名单（配置 `[dm] allowed_mids`）；限频 1 分钟 1 次 |
| `post_dynamic` | text(≤200 字) | 限频 10 分钟 1 次；默认关 |

统一护栏：
- `[reasoning] allow_side_effect_tools = false`（默认 **false**——开了才生效；关着时所有副作用工具返回「未授权」）
- 每次调用记日志 `action="tool_side_effect"`（工具/参数摘要/结果/额度剩余）
- 失败不重试（避免重复送礼/关注）

## 三、注册与测试

- 全部注册进 `internal/reasoning/tools.go` 的 Toolbox（按事件 Scope 构造）
- 测试：
  - 只读：mock 数据源断言参数/截断/limit 校验
  - 副作用：额度拦截、限频拦截、不取关主人、开关关闭拒绝、dry-run 不真发
  - 工具名/描述完整（LLM 能选）
- 不调真实模型/平台（mock）

## 四、禁止

- 不改 contracts、不碰调度/发送语义、不执行 git
- 副作用工具必须默认关（allow_side_effect_tools=false）

## 五、交付格式

- 文件列表、工具清单（名称/参数/护栏）、测试结果、配置说明、未做项、是否暂停写入
