# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与语义化版本。

## [Unreleased]

### Added
- 项目文档骨架：愿景、架构、事件、调度、大脑、记忆、通道、人格、数据模型、可观测成本、MCP、安全合规、部署、决策记录
- 接口契约：event / collector / sender / brain
- 开发计划：Phase 0–8
- 工程配套：editorconfig、golangci-lint、CI、Makefile、环境变量模板（个人项目，不设协作流程）
- 原生 tool calling 第一批：Provider 可选 `ToolProvider`、工具 JSON Schema、`RunNative` 循环与文本回退（默认 `native_tools=off`）
- B1–B11 改进计划与延迟诊断 L 系列计划；延迟口径与评论/私信排查文档

### Changed
- 仓库卫生：根目录临时文件归档到本地 `tmp/`；`AGENTS.md`、`CLAUDE.md`、协作表、内部报告与 prompts 改为本地保留，不再上传

### Fixed
- 回退 3 条未评审的人格评测用例，修正 v1.3 人格包基线与 `fishctl` 预算断言，恢复 `go test ./...` 全绿

---

<!-- 发布时记录：

## [0.1.0] - YYYY-MM-DD
### Added
- Phase 0：弹幕单线闭环
-->

## 版本规划

| 版本 | 内容 |
|---|---|
| 0.1.0 | Phase 0：弹幕单线闭环 |
| 0.2.0 | Phase 1：评论 + 私信 |
| 0.3.0 | Phase 2：记忆正式化 |
| 0.4.0 | Phase 3：QQ |
| 1.0.0 | Phase 4–5：稳定与面板 |
