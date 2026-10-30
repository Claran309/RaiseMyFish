# CLAUDE.md

> Claude Code 入口。规则以 `AGENTS.md` 为准，本文件只做指引与工具特定说明。
> 其他 agent（Codex / Cursor / Gemini）读 `AGENTS.md` 即可。

## 必读顺序

1. `AGENTS.md`（硬规则）
2. `docs/02-architecture.md`
3. `contracts/`
4. `plans/phase-0.md`（当前唯一可开工阶段）

## 工具特定说明

- 项目使用 Go 1.25+；命令见 `Makefile`。
- 提交直接 push 到 `main`（或 `dev`），**不走 PR 流程**。
- 改接口/数据模型时同步更新 `contracts/` 或 `docs/`。
- 没有测试的改动不要 push。
- 不确定的设计点写入对应 Phase 的「待定」区，不自行发明。

## 一句话

按 `AGENTS.md` 执行，别引入 Redis / MQ / Milvus / 分布式，别把内部调用写成 MCP。
