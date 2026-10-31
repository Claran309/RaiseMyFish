# Phase 0.5 · 知识库 K0（文件知识库）

> 目标：在 Phase 0 人格过线后、Phase 1 之前，补上最便宜的一层知识：
> 梗库、设定、观众画像、游戏知识，做成文件 + 关键词召回。
> 依据：`docs/knowledge.md` 的 K0 阶段。

## 依赖

- Phase 0 退出标准通过（尤其 0.7c 人格 ≥90%，或主人明确接受现状继续）
- `contracts/persona.md`（P0-PACK-1）与 `contracts/brain.md`（P0-BRAIN-1）基线

## 任务清单

### K0.1 知识目录与格式
- [ ] `personas/dafeiyu/knowledge/`：lore.md、memes.md、viewers.md、games.md
- [ ] 每条知识带标签：`channel`、`tags`、`priority`
- [ ] 格式规范写入 docs/knowledge.md

### K0.2 梗库迁移
- [ ] `~/.dsh/tts/meme-ctx.mjs` 的 7000 条 → memes.md（分批，先 500 条）
- [ ] 保留「原词 + 接法」结构，不丢原始数据
- [ ] 迁移脚本可重复执行

### K0.3 加载器与召回
- [ ] knowledge 包：加载文件、解析条目、按通道+关键词召回
- [ ] 召回预算 ≤ 上下文 15%
- [ ] 与 memory 共用预算，不挤占人格

### K0.4 接入 Brain
- [ ] 契约变更由 Codex 裁决（`ContextInput.Knowledge` 或复用 memory.Context）
- [ ] 拼进 user context 段，不塞 system

### K0.5 验收
- [ ] 知识条目可加载
- [ ] 关键词命中抽查
- [ ] 预算不挤占人格
- [ ] 不破坏 0.7c 证据与既有测试

## 退出标准

1. 知识库能加载三类知识（梗/设定/观众）
2. 召回命中率人工抽查通过（≥80% 相关）
3. 知识段 ≤ 15% 预算，人格与事件完整
4. `go test ./...` 全绿，不改变现有验收快照

## 风险

| 风险 | 应对 |
|---|---|
| 梗库迁移量大 | 分批迁移，先 500 条验证 |
| 召回污染 | 标签 + 优先级 + 人工抽查 |
| 契约变更影响 Brain | 由 Codex 裁决，先按 memory.Context 扩展 |
