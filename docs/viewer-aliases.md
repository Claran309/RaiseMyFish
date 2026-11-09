# 观众别名与同音组

观众认人除 UID/昵称/编辑距离/语义外，还会读取一份**人工维护**的别名与同音组文件。
文件默认路径 `personas/dafeiyu/viewer_aliases.yaml`，由 `[viewer] alias_file` 指定。

## 1. 数据来源与确认状态

别名/同音组只能来自人工维护的 YAML，不从聊天内容自动生成、不自动改写原始昵称、不写库。

每条数据必须显式声明 `status`：

| status | 含义 | 是否参与认人 |
|---|---|---|
| `candidate` | 待确认候选，仅留档 | 否 |
| `confirmed` | 人工确认 | 是 |
| `rejected` | 人工拒绝（负例） | 否；并抑制「该别名 → 该档案」的别名/近似/同音候选 |

`rejected` 只压制弱证据（别名、编辑距离、同音）；如果档案的**当前昵称**与查询完全相同，
仍按昵称精确命中——那是本人现在的名字，不是猜测的关联。

## 2. 文件格式

```yaml
schema: fish-viewer-aliases/v1

aliases:
  - alias: "长弓三溢"       # 别名原文
    uid: "5005"             # 与 name 二选一
    kind: typo              # manual/nickname/historical/typo/phonetic
    status: confirmed

  - alias: "弓长三亿"
    name: "弓长三益"         # 按当前昵称归属（改名前快照可能失效）
    kind: phonetic
    status: candidate       # 待确认：不注入

phonetic_groups:
  - words: ["贞德", "真得"]  # 至少 2 个词
    status: confirmed
```

约束（加载时校验，失败保留旧快照）：

- 文件 ≤ 1 MiB，别名 + 同音组 ≤ 2000 条；
- `alias`/`name`/同音词 ≤ 64 字，不能含 NUL；
- `uid` 必须是纯数字平台 UID；
- `uid` 与 `name` 必须且只能填一个；
- 同一条别名+归属人不能重复；
- 未知字段、未知 `kind`/`status`、多 YAML 文档整体拒绝。

## 3. 匹配档位

- `aliases` 中 `confirmed` 的条目 → **TierAlias**（`alias_exact`），档位低于当前昵称精确、高于近似；
- `phonetic_groups` 中 `confirmed` 的组 → **TierNear**（`phonetic`），只扩大候选，不自动认定；
- 多个同档候选按既有歧义规则返回 `ambiguous`，不会替用户合并身份。

同音组只影响查询期候选，不持久化、不改写事件与人设资料。

## 4. 热更新

`fishd` / `fishctl viewer search` / `fishmcp` 每次认人前比对文件 `mtime` 与大小：

- 变化 → 重新解析并原子替换快照；
- 解析失败 → 保留上一份可用快照，记录告警，下次文件变化再试；
- 文件暂不存在 → 视为空别名表，出现后自动加载；
- 无需重启进程，也没有后台轮询 goroutine。

## 5. 边界

- 别名归属只接受显式 `uid` 或当前昵称，不跨平台用同名推断；
- 私密会话不因别名获得额外可见性——档案/检索本身只使用公开投影；
- 本表是识别增强，不是人格包或知识库的一部分。
