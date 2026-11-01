# 0.8 发送器 · WorkBuddy 派发单（分批）

> 背景：2026-11-01 Codex 额度用尽，开发暂由 WorkBuddy 接手。WorkBuddy 只做「固定输入输出的薄任务」，不独立设计状态机/隐私/记账/恢复协议；设计由 DSH 出规格、逐项验收。
> 本单只放行 **0.8a**，后续批次验收后再发。

## 0.8a：复制直播发送脚本（本批唯一任务）

### 目标

把本机 dsh-bili-whale@0.4.5 的直播发送脚本复制进项目，去掉 DSH 依赖，作为独立子进程。

### 精确步骤

1. 复制源文件：
   - 源：`C:\Users\Claran\.dsh\profiles\desktop\node_modules\dsh-bili-whale\tools\commands\danmaku.mjs`
   - 目标：`tools/bili-whale/danmaku.mjs`
2. 复制许可证（若已存在则核对一致）：
   - 源：`C:\Users\Claran\.dsh\profiles\desktop\node_modules\dsh-bili-whale\LICENSE`
   - 目标：`tools/bili-whale/LICENSE`（0.5 已复制；只核对，不重复写）
3. 计算并记录源文件 SHA-256（写入 `tools/bili-whale/ADAPTATION.md` 的发送段，格式参考采集段）。
4. 删除/改造 DSH 依赖：
   - 去掉对 DSH 配置、`~/.dsh` 路径、插件宿主、宿主日志的读取；
   - 凭据只从 `FISH_BILI_COOKIE` 或 `FISH_BILI_COOKIE_FILE` 读取（与采集脚本一致）；
   - 参数收窄为：`--room <规范房间号>`、`--msg <文本>`、`--dry-run`；
   - 保留源脚本的签名/压缩/防重 10 秒/outbox 写入逻辑（可改路径到环境变量或临时目录）。
5. 输出：stdout 只输出有版本的 JSONL：`{"protocol":1,"type":"sent","msg_id":"..."}` 或 `{"protocol":1,"type":"error","kind":"retryable|fatal","message":"..."}`；不输出 cookie/token/原始请求体。
6. 新增 `tools/bili-whale/send.test.mjs`（Node 测试）：参数校验、dry-run 不发送、错误 JSONL 形状、不泄漏凭据。

### 禁止

- ❌ 不改 `internal/`、`cmd/`、`contracts/`、`plans/`（除本派发单与本轮报告）
- ❌ 不实现 Go 侧发送器（0.8c 另批）
- ❌ 不真实发送到 B站（dry-run 且不连平台）
- ❌ 不抄/不引入 npm 依赖（只用 Node 标准库）
- ❌ 不改 0.5 的采集脚本与测试

### 交付格式（写给主人在 WorkBuddy 会话里转交）

- 修改/新增文件列表（精确路径）
- 源文件 SHA-256
- 测试命令与结果（Node）
- 未做项与原因
- 是否暂停写入

### DSH 验收标准（逐条）

1. `tools/bili-whale/danmaku.mjs` 存在，基于源文件改造，凭据只读环境变量
2. 源哈希记录在 `ADAPTATION.md`
3. `node --test tools/bili-whale/send.test.mjs` 通过
4. dry-run 输出 JSONL 且不访问网络
5. 无 `internal/`/`cmd/`/`contracts/` 越界改动
6. 不泄漏任何凭据/请求体

## 后续批次（本单不放行）

- 0.8b：Go 发送器接口与子进程调用（DSH 出接口规格）
- 0.8c：核验/失败分类（retry/dead/skipped）
- 0.8d：`send_log` + `out` 事件（唯一写入责任）
- 0.8e：集成测试（mock Node → 真实 PG）

每批验收通过后才发下一批；WorkBuddy 一次只做一个批次。
