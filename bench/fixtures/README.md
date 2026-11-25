# 压测样本与 mock 发送

本目录只放**可复现脚本**，产物写 `bench/raw/` 或 `bench/out/`（均已被 `.gitignore` 忽略）。

## 1. 脱敏真实事件

```powershell
go run ./bench/fixtures --in raw-export.jsonl --out bench/raw/events-sanitized.jsonl
```

- `from_id` / `session` / `meta.uid` / `meta.recv_actor` 等 UID 统一加盐 SHA-256 前缀；
- 正文按 rune 截断（默认 80），`meta.pics` 删除；
- 事件 ID 与 Session 同步改写，保证仍能通过 `store.ValidateEvent`。

## 2. 生成合成样本

```powershell
go run ./bench/fixtures --generate 10000 --out bench/raw/events.jsonl
```

默认通道占比 live 70% / comment 15% / dm 15%，`at` 严格升序，可直接交给
`cmd/fishload` 按固定速率或突发模式回放。

## 3. mock 发送脚本

```powershell
node bench/fixtures/mock-send.mjs --room 10000001 --text "压测" --msg-id bench-1
```

只返回稳定 JSON 回执，不触网；`--fail` 用于注入发送失败。
