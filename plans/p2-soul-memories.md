# P2 记忆正式化派发单（长期设定注入）

> 背景：P1 已有 memory_records（fact/preference/promise + LLM 提炼 + from_id + 时效）。还差「设定级记忆」——那些**长期有效、每轮都该知道**的事实（如「主人喜欢甜」「本鱼不爱吃辣」「娇娇是主播」），不该每轮按需召回，应该直接进 system prompt。
> 目标：从高置信度记忆提炼「设定级」条目 → 注入 system prompt（长期人设记忆）。

## 一、设定级记忆存储

`personas/dafeiyu/knowledge/soul-memories.md`（或 memory_records 加 `scope='soul'`）：

- 格式：K0 YAML（title/summary/importance/valid_from/source）
- 内容示例：
  ```yaml
  - title: "主人喜欢甜"
    summary: "主人多次提到喜欢吃甜食"
    importance: 0.9
  - title: "娇娇是主播"
    summary: "娇娇（房间 1992099976）是直播间主播"
  ```
- 来源：memory_records 里 `kind=fact/preference` 且 `importance>=0.8` 的记录 → 定时提炼成设定

## 二、注入（Brain）

`ContextInput` 加 `Soul []string`（设定条目）；`context.go` 在 system prompt 末尾注入：
```
<longterm>
- 主人喜欢甜
- 娇娇是主播
</longterm>
```
（约 5-10 条，总长 ≤500 字；空则不注入）

## 三、提炼与更新

- `extractworker` 加「设定重构」子任务（每小时节流）：
  - 从 memory_records 选高置信度事实/偏好（去重、合并）
  - 写入 soul-memories.md（原子替换）
  - 数量上限（≤20 条，按 importance 排序）
- 时效：fact 更新时（P1.4 的摘要变化→valid_from 刷新）→ 设定重构会带上新内容

## 四、配置

```toml
[memory]
soul_enabled = true       # 默认 true
soul_max_entries = 20     # 注入上限
soul_path = "personas/dafeiyu/knowledge/soul-memories.md"
```

## 五、测试

- 提炼：高置信度事实 → 设定条目；低置信度不入选；去重
- 注入：system prompt 含 `<longterm>` 段；空不出现
- 更新：fact 变化 → 设定更新

## 六、禁止

- 不改 contracts、不碰调度/发送语义、不执行 git

## 七、交付格式

- 文件列表、测试结果、配置说明、未做项、是否暂停写入
