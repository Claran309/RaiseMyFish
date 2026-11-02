# 0.13b 送礼派发单（默认 dry-run，真送需配置确认）

> 背景：按 dsh-bili-whale 对齐，移植 gift.mjs（小花花 1 电池）。
> ⚠️ 送礼是真花钱（电池/余额），默认 dry-run，只有显式配置 `[gift] real=true` 才真发。

## 允许路径

- 新增 `tools/bili-whale/gift.mjs`（移植自 dsh-bili-whale@0.4.5，MIT）
- 新增 `internal/gift/`（Go 薄封装：Node 子进程调用 + 结果分类）
- `internal/config/gift.go` + `config/fishd.toml` 的 `[gift]` 段
- 测试文件随实现
- 不碰 contracts、调度/发送/人格/知识

## 移植要求（gift.mjs）

1. 源文件：`C:\Users\Claran\.dsh\profiles\desktop\node_modules\dsh-bili-whale\tools\commands\gift.mjs`
2. 保留 LICENSE 与来源 SHA 记录（沿用 `tools/bili-whale/ADAPTATION.md` 或新建条目）
3. 去掉 DSH 依赖（配置/状态/宿主日志）；凭据只从 `FISH_BILI_COOKIE`/`_FILE`
4. 参数：`--room <房间号>`、`--gift-id <礼物ID>`、`--num <数量>`、`--dry-run`（默认 dry-run）
5. 输出 JSONL：`{"protocol":1,"type":"sent","gift_id":"...","num":1}` 或 `{"protocol":1,"type":"error","kind":"retryable|fatal","message":"..."}`
6. **dry-run 必须不调平台 API**（只打印将发送的内容）

## Go 封装（internal/gift）

```go
type Sender interface {
    Send(ctx context.Context, room string, giftID string, num int) (Result, error)
}
type Result struct {
    Sent   bool
    GiftID string
    Num    int
    Error  string
}
```

- 走 Node 子进程（复用 collector 的子进程模式：环境隔离、超时、取消）
- 失败分类：retryable/fatal（同 sender 约定）
- 配置 `[gift]`：`enabled=false`（默认关）、`real=false`（默认 dry-run）、`gift_id`（默认 "小花花" 对应 ID 需查）、`num=1`、`daily_limit=1`（防超额）

## 禁止

- ❌ 不真实送礼（测试全 mock；真送留验收时由主人显式开启）
- ❌ 不改 contracts、不碰调度/发送
- ❌ 不执行 git

## 交付格式

- 文件列表、gift.mjs 来源 SHA、测试结果、dry-run 行为说明、未做项、是否暂停写入
