# Presence · 三心二意状态表

> 2026-10-31 主人提出。定位：肥鱼「现在在做什么」的当前态注册表（注册表 + 状态机）。
> 与 Persona（怎么说）、Knowledge（知道什么）、Memory（记得什么）并列；状态变更走 Event Bus，表是「当前态」的物化视图。

## 1. 主体与会话分离（产品模式）

「肥鱼」（主体）独立于「会话」（所有活动任务）存在：

```
主体（肥鱼）
├─ Persona  我是谁（静态：soul/rules/samples/context）
├─ Presence 我现在在做什么（动态：直播中/看直播中/剪视频中）
└─ Memory   我记得什么（历史：事件/提炼/召回）
        ↑ 三者共享给所有会话

会话（一次活动任务）
├─ Event  一条弹幕/评论/私信/QQ 消息
├─ Task   要回复什么
└─ Brain  调用一次，读「主体三件套 + 事件」
```

- 会话本身**无状态**：每次回复完即结束
- 状态全部挂在主体上：persona 静态、presence 动态、memory 历史
- 现有设定已按此分离（persona 与会话解耦）；Presence 补的是「主体当前状态」这一层

## 2. 定位

| 层 | 回答 | 存放 | 查询时机 |
|---|---|---|---|
| Persona | 怎么说 | 人格包 | 全量进 system |
| Knowledge | 知道什么 | 知识文件/表 | 按需召回 |
| Memory | 记得什么（过去） | events / memory_records | 三因子召回 |
| **Presence** | **现在在做什么** | `presence_states`（PG 表） | **每轮查摘要，详情按需** |

**问题背景**：肥鱼同时开播、看别人直播时，两个会话互不知情。弹幕问「你怎么还能边看别人直播边直播啊？」时，模型上下文答不上来——因为没有「当下状态」层。

## 3. 状态模型（多值并存）

```go
type State struct {
    ID               string            // 状态实例 ID
    Owner            string            // fish（固定：肥鱼主体）
    Name             string            // 直播中 / 看直播中 / 剪视频中
    Summary          string            // 对外摘要：「正在看直播」
    Visibility       string            // public | private：其他会话能否读到该状态（连摘要）
    DetailVisibility string            // public | private：能否读到 Detail
    Detail           string            // 详情：「在看娇娇的直播」
    StartedAt        time.Time
    RenewedAt        time.Time
    ExpiresAt        time.Time         // TTL 续租约
    SourceSession    string            // 注册方会话 ID
    Metadata         map[string]string // 状态编号等机械数据
    Revision         int               // 乐观并发
}
```

- **多值并存**：直播中 + 看直播中 + 剪视频中可同时存在，读取时返回全部摘要
- 生命周期：Active → Released（主动）/ Expired（TTL 到期）

## 4. 可见性规则（服务端过滤，不靠模型自觉）

可见性由**注册方按状态实例设置**，不是按通道一刀切：

| Visibility | DetailVisibility | 其他会话能读到的内容 |
|---|---|---|
| private | — | **连摘要都不返回**（SQL 层不查） |
| public | private | 摘要（如「正在看直播」），**Detail 不返回** |
| public | public | 摘要 + Detail（如「在看娇娇的直播」） |

- 过滤在 repository/SQL 层实现：私密状态在查询时就不进入结果集
- 所有会话都能读状态表；但每个状态的 Visibility/DetailVisibility 各自过滤
- 不因「都是主人」例外；私密状态不进公开输出

## 5. 注册方与生命周期

| 来源 | 注册方式 |
|---|---|
| **本项目任务**（看直播/开播等采集任务） | 派发阶段（collector/scheduler 装配处）直接注册 |
| **外部 agent 任务**（游戏/剪视频等） | `fishctl state register` CLI + 接口契约；流程注入到对方 prompt/skill |

**外部 agent 用 etcd 式租约（lease）**：

```
开工    fishctl state register --name "剪视频" --ttl 5m   → 返回 lease_id
进行中  fishctl state renew <lease_id>                    （每分钟）
完成    fishctl state release <lease_id>
崩溃    → 不续租 → TTL 到期自动过期（0.4 调度器清扫兜底）
```

- **主动 release 是快路径，TTL 是兜底**
- 外部 agent 的契约：开工 register、收工 release、中间 renew；进程死了靠 TTL
- 「别的 agent 运行」与「项目本身运行」解耦：本项目只提供状态表与 CLI 契约

## 6. 查询

| 内容 | 时机 | 说明 |
|---|---|---|
| 摘要 | **每轮自动查**（表小、便宜） | 拼进 Brain 上下文 `ContextInput.Presence` |
| 详情 | 按需（模型明确问时） | **P0 不开放**；P1 加 `presence.get_detail` 工具调用 |

无状态返回空快照；查询失败报错，不伪装空。

## 7. 历史归记忆层

- 状态表**只存当前态**，结束即删；不设状态历史
- 历史由 Memory 承接：状态中的会话（如「看直播」）结束时自行写一条 Memory（「今天看了娇娇直播」）
- 状态变更仍走 Event Bus（`state.registered` / `state.renewed` / `state.released` / `state.expired`），事件可回放

## 8. 与 Brain 契约

- 新增 `ContextInput.Presence *presence.Snapshot`（与 Memory/Knowledge 并列）
- 快照只含可见性允许的摘要；详情 P1 走工具
- Brain 不自己查表，装配方注入

## 9. 阶段

| 阶段 | 内容 |
|---|---|
| **P0 最小版（0.6）** | 表 + 注册/释放 + 摘要注入 + 可见性服务端过滤 + TTL |
| P1 正式版 | 详情工具调用、心跳续租、调度器兜底、事件回放、外部 agent 契约文档 |

## 10. 验收

- 多状态并存：三个状态同时注册，摘要全部可见
- 可见性：private 状态对其他会话不可见（SQL 层断言）；DetailVisibility=private 时只返回摘要
- 生命周期：主动释放、TTL 过期、崩溃后调度器清理
- 事件：注册/释放事件写入事件流
- 不参与人格评分；不改变 Memory/Knowledge 语义

## 11. 非目标

- ❌ 分布式状态同步（单机单库）
- ❌ 模型自己判断可见性（服务端过滤）
- ❌ 替代 Memory（历史）与 Knowledge（知识）
- ❌ P0 开放详情查询（P1 工具）
