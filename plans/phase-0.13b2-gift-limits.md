# 0.13b-2 送礼限制可插拔化

> 背景：礼物限制目前分两处——配置（gift_id/num/daily_limit）与硬编码（gift.mjs 的 PRICE_CEILING=100、MAX_NUM=100；房间白名单缺失）。
> 本批把限制全部收进 `[gift]` 配置，代码只保留安全默认。

## 允许路径

- `internal/config/gift.go`（加字段 + 校验）
- `tools/bili-whale/gift.mjs`（PRICE_CEILING/MAX_NUM 改为参数，默认保持 100/100 安全值）
- `internal/gift/gift.go`（传递参数 + 房间白名单校验）
- `config/fishd.toml`（[gift] 段补字段）
- 测试文件随实现

## 修改内容

### 1. 配置字段（internal/config/gift.go）

```go
type Gift struct {
    Enabled      bool
    Real         bool
    GiftID       string
    Num          int
    DailyLimit   int
    MaxPrice     int      // 价格上限（price 单位，100=1 电池），默认 100
    MaxNum       int      // 单次数量上限，默认 10（原硬编码 100 收紧）
    AllowedRooms []string // 房间白名单；空 = 不限制
    // node/script/timeout 不变
}
```

校验：
- `MaxPrice >= 0 && MaxPrice <= 10000`（0 表示不限制？保持 >0 更安全：1–10000）
- `MaxNum >= 1 && MaxNum <= 100`
- `AllowedRooms` 每个必须是纯数字房间号；空数组允许（不限制）

### 2. gift.mjs 参数化

- `--max-price <n>`（默认 100）、`--max-num <n>`（默认 100）
- `PRICE_CEILING`/`MAX_NUM` 从参数读取；仍保留硬上限（max-price 不超过 10000、max-num 不超过 100 的二次校验——防配置写错）
- 参数校验失败 → fatal

### 3. Go 侧

- `internal/gift/gift.go` 从配置读 MaxPrice/MaxNum/AllowedRooms
- 房间白名单在 **Go 侧校验**（先于子进程）：`AllowedRooms` 非空且 room 不在其中 → 拒绝（不调用 Node）
- 传参给 gift.mjs：`--max-price`、`--max-num`

### 4. 测试

- 配置校验：非法 MaxPrice/MaxNum/AllowedRooms 拒绝
- Go 侧白名单拦截（不在名单 → 拒绝且未 spawn）
- 参数传递断言（mock Node 收到 --max-price/--max-num）
- gift.mjs 单测：参数解析默认值/非法值

## 禁止

- 不改 contracts、不碰调度/发送
- 不真实送礼（mock）
- 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、默认值表、未做项、是否暂停写入
