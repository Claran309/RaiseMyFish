# B站账号管理领域设计（可配置 + Web 模块）

> 现状：项目直接用环境变量 `FISH_BILI_COOKIE`（单账号、不可切换、Web 里看不到）。
> 目标：账号作为**独立可配置领域**——多账号存储、切换活跃账号、Web 管理。

## 一、领域模型

```
Account（账号实体）
├── id           TEXT PRIMARY KEY          -- 内部 id（account-{sha256(mid)[:12]}）
├── mid          TEXT NOT NULL UNIQUE      -- B站 UID
├── name         TEXT NOT NULL             -- 昵称（快照）
├── cookie       TEXT NOT NULL             -- 原始 cookie（本地明文，文档注明）
├── csrf         TEXT NOT NULL             -- bili_jct
├── status       TEXT NOT NULL             -- active / invalid / disabled
├── last_check   TIMESTAMPTZ               -- 最近验证时间
├── created_at / updated_at
└── note         TEXT                      -- 备注（如「主号」「备用」）
```

**活跃账号**（当前使用哪个）：
- `state/active_account.json`（`{account_id}`）或 `accounts.is_active` 布尔
- 推荐：`accounts.is_active`（DB 内布尔，唯一约束只有一个 true）

## 二、凭据解析链（兼容现有）

```
账号管理启用时：DB accounts（is_active=true）→ cookie/csrf
否则：环境变量 FISH_BILI_COOKIE（现状，向后兼容）
```

- 采集/发送/送礼/点赞/私信/评论 全部取「当前活跃账号」
- 账号失效（401/认证错）→ 标记 status=invalid + 告警（Web 可看到）

## 三、Web 模块（`/accounts`）

| 功能 | 操作 | API |
|---|---|---|
| 账号列表 | 显示 mid/昵称/状态/最后验证 | GET /api/accounts |
| 添加账号 | 粘贴 cookie（从浏览器复制）→ 验证（调 get_info 确认 mid） | POST /api/accounts |
| 切换活跃账号 | 点选 → 更新 is_active | PUT /api/accounts/{id}/activate |
| 删除账号 | 删除（不能删当前活跃？或自动切到另一个） | DELETE /api/accounts/{id} |
| 测试连接 | 调 B站接口验证 cookie 有效性 | POST /api/accounts/{id}/check |
| 失效提醒 | invalid 状态红色标记 + 一键重新验证 | 同 check |

## 四、安全

- cookie 本地明文（个人项目，文档注明；不打印日志、不出现在 API 响应明文——只回 `cookie_masked`）
- Web 添加账号要 confirm；删除要 confirm
- 只有 127.0.0.1 可访问（沿用面板安全）

## 五、迁移

- 迁移 `0007_accounts.sql`：建表 + `INSERT FROM env`（首次启动把 FISH_BILI_COOKIE 自动导入为「主号」？——可选，避免凭空写入）
- `internal/account`：Service（Load/Active/Ensure/Validate）
- 现有 `internal/bili` 的 cookie 来源改为「account.Resolver」

## 六、多账号（未来）

- 采集账号 vs 发送账号分离（一个看一个发）
- 账号池轮换（风控规避）
- 本设计先做「单活跃账号 + 可切换」

## 七、测试

- 账号 CRUD/切换/验证（mock B站）
- 凭据解析链（DB 优先 → env 兜底）
- 失效标记
- Web API（httptest）

## 八、禁止

- 不改 contracts、不碰调度/发送语义
- 密钥/凭据不进日志与 API 明文

## 九、实施状态（2026-11-05，批次 #44）

- [x] 迁移 `0008_accounts.sql`：accounts 表 + 唯一活跃索引；`database.Ready` 校验列。
- [x] `internal/account`：`Load/Active/Ensure/Validate/Add/Activate/Delete/Check/Resolve`。
- [x] `internal/database.AccountRepo`：Raw 参数化 CRUD、按 mid 幂等 upsert、首个账号自动激活。
- [x] `internal/bili`：`Options.Resolver` 每请求解析凭据；新增 `Nav` 身份校验与 `IsAuthError`。
- [x] fishd 装配：启动时回填 Node/截帧静态配置；Go 客户端走动态 Resolver；DB 读失败保留环境静态凭据。
- [x] Web `/accounts`：列表（脱敏）、添加（nav 验证 UID）、切换、删除、测试连接；写操作 confirm=yes，仅回环。
- [x] 测试：account/bili/database(真实 PG 集成)/web(httptest+页面断言)/fishd 装配；浏览器实测列表、测试连接、切换、添加、删除。
- 限制：配置校验仍要求启用直播/评论时提供环境变量 cookie，暂不支持纯 DB 启动；
  静态消费方（Node 采集/送礼、评论采集/发送、截帧）切换账号后需重启 fishd 生效；
  本阶段不做多账号池与采集/发送账号分离。
