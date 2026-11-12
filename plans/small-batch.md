# 小单批次（2026-11-12）

## 批 P1：#2 点赞路径确认

**问题**：点赞用 `room/v1/Room/like`——B站还有 `xlive/web-ucenter/v1/roomAdmin/like`——**不确定哪个有效**。

**改法**：验收时看 `code != 0` 就改常量（无需重构）。

## 批 P2：#4 私信会话翻页

**问题**：`get_sessions` 只取首屏 20 会话——消息多的会话漏。

**改法**：`get_sessions` 翻页（end_ts 游标）——拉更多会话——消息多的会话用 `get_session_msgs` 拉详情。

**验收**：会话数 >20 的场景能拉到全部。

## 批 P3：#5 评论多级回复

**问题**：采集的 root/parent 只存 Meta；回复统一 root=parent=rpid（单级）。

**改法**：回复用真实 root/parent（多级：回复楼中楼时 parent=被回复评论的 rpid，root=根评论 rpid）。

**验收**：回复楼中楼时用真实 parent（不是一个 rpid 走天下）。

## 批 P4：#8 多 persona 支持

**问题**：soul_path 固定 dafeiyu——多 persona 时按 pack 派生。

**改法**：soul_path 按 persona pack 派生（`personas/<name>/soul.md`）。

**验收**：配置多个 persona 时各自 soul 正确。

## 不派

- #59 发送异步化（架构—主人没让派）
- #13/#14 web_search（主人还没配 API）
- #24 Edge 截图（主人取消）
