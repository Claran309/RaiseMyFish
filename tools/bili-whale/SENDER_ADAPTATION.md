# 0.8 Node 弹幕薄适配

本目录的 `danmaku.mjs` 只负责只读预检、一次投递和一次只读核验。它尚未接入 Go Sender，也不代表 Phase 0.8 或 P0-SEND-1 契约提案已通过。

## 来源与许可

- 上游：`dsh-bili-whale@0.4.5/tools/commands/danmaku.mjs`。
- 本机只读来源：`C:/Users/Claran/.dsh/profiles/desktop/node_modules/dsh-bili-whale/tools/commands/danmaku.mjs`。
- 来源 SHA-256：`119d0ef000968f3746fee071c31bc502b421b2a785ef50b17060839b29013b1c`。
- MIT 许可证继续使用本目录现有 [LICENSE](LICENSE)，保留原有版权声明。
- 移植：固定 `/msg/send` 表单（csrf/csrf_token、roomid、msg、rnd、fontsize、color、mode、bubble、room_type、jumpfrom、statistics）与只读房间历史路径。
- 删除上游 quota-gate、主目录凭据发现、10秒本地文件防重、outbox/活动流/共享状态写入和原始平台响应输出。未改动 0.5 的协议/监听/许可证文件。

## 函数入口及证据

`preflight(request, deps)` 校验 `request_id`、严格正十进制 `room_id/self_id`、`private:false`、1–40 个 Unicode 字符的完整正文。40 个 emoji 也合法，不做截断；空正文、超过40字、孤立代理项非法。只访问固定 HTTPS nav 与 get_info，登录 UID 和规范房间须完全相同。短房间号不自动转换成其他房间。

凭据复用 0.5 的 `credentialHeader`：只读取显式 `FISH_BILI_COOKIE` 或 `FISH_BILI_COOKIE_FILE`；Sender 额外要求唯一、非空的 SESSDATA/bili_jct/buvid3。函数测试通过 `deps.cookie` 注入纯合成值，`deps.fetch` 注入本机替身。HTTP 默认10秒，可通过 `deps.httpTimeoutMs` 设置1–60000ms；`deps.signal` 支持取消。固定 B站 HTTPS 目标、`redirect:error`，响应最多1MiB，无 POST 重试。

公开 session 只有 request_id/room_id/self_id，已冻结。模块内部 WeakMap 保存秘密及不可变请求副本；拷贝 session 或换正文/请求/房间/账号均不能投递。公开 session 不能跨进程序列化恢复；核验进程须重新只读预检。

`postOnce(request, session, permit, deps)` 每次调用至多一次 POST。permit 必须含相同 request_id、非空 attempt_id、严格正十进制 nonce 和未来 expires_at；未获许可、已过期、预先取消、无效绑定返回 `not_posted`。POST 发起后网络错误、取消、超时、HTTP失败及异常响应统一保守 `unknown`；只有可信成功回执里的稳定非零 ID 才给 `verified:true/evidence:receipt_id`。code=0 无 ID 返回 `accepted/verified:false`。明确负回执区分 rate_limit/content/unavailable/auth/invalid；未识别 code 保守 unknown。结果不含原始错误、message、body、Cookie 或 CSRF。

`verifyOnce(request, session, expected, deps)` 只 GET 指定房间 gethistory。expected 包含 request_id/attempt_id，以及已知 msg_id 或原尝试持久化 nonce；核验不要求许可尚未过期。只匹配本账号、完整正文以及精确已知 ID，证据为 `history_id`。只有未知 ID 时允许匹配平台回显 rnd，且该行须有稳定 ID，证据为 `history_nonce`。已知 ID 不匹配时不会用 rnd 补位；相同正文、时间相近、缺历史均保持 unknown。数组、布尔等伪 UID/rnd/ID 不作为证据。

统一结果结构为 `{outcome,msg_id,verified,error_kind,evidence}`；evidence 仅为 `receipt_id/history_id/history_nonce/none`。这里的 accepted 表示平台受理或有核验证据，不等于必然已经核验。

## 子进程 JSONL

Node 24 导入模块无副作用。发送只有 `node tools/bili-whale/danmaku.mjs --action send --go` 开放；正文、许可、Cookie 均不放命令行。每个进程 stdin 总计最多16KiB，行必须以换行结束，整个握手最多30秒。

1. Go 写入 `{protocol:1,type:"prepare",request_id,room_id,self_id,private:false,text}`。
2. Node 只读预检后输出 `{protocol:1,type:"ready",request_id,room_id,self_id}`，此前零 POST。
3. Go 在 PG 持久化许可与未知尝试后，只写入一次 `{protocol:1,type:"commit",request_id,attempt_id,nonce,expires_at}`。
4. Node 一次投递后输出 `{protocol:1,type:"result",request_id,attempt_id,result:{...}}` 并退出。

Go 必须保持 stdin 打开直到 result。进程收到并处理EOF、SIGINT/SIGTERM或握手超时时，会取消尚未投递的动作；POST 已发起后的取消为 unknown。没有 commit、无效 commit 或过期 commit 不会 POST。退出码0只用于 accepted，非0结果也必须检查 JSONL 分类；管道中断不能推断平台未收到。Windows强杀或其他不可捕获的终止可能来不及执行Signal处理器、也可能没有result帧，父进程对已授权尝试必须按unknown处理，不能把缺帧当作未投递。

核验使用 `--action verify`，无需 `--go`；只读一帧 `{protocol:1,type:"verify",...request,attempt_id,nonce,msg_id}`，重新只读预检后读取历史并输出 result。只有 verified=true 退出码为0。核验针对已存在的投递，缺凭据、预检失败、提前取消及其他核验失败始终为unknown，绝不能返回not_posted；有效attempt_id在预检前保存，失败帧仍关联原尝试。任何失败 stdout 只含受控 JSONL 结果，无原始错误文本。

该 Node 层每次调用不重试，**也不承诺跨调用的 attempt_id 去重**。未来 Go/PG 核心负责持久化许可、唯一尝试、10秒防重、实际POST限流、未知结果只核验和发送记账；不能用 Node 内存或本地文件代替 PG 账本。rnd 是相关性证据，不是平台幂等键。

## 验证记录与限制

2026-11-01，Node v24.14.0。本任务先执行既有 sender 测试，实际 RED 为 `ERR_MODULE_NOT_FOUND`；补充会话绑定、严格标量、Unicode、许可及CLI边界反例后实现。后续增加流大小、超时/取消与CLI两阶段合成用例并复跑 GREEN，不宣称每个后增用例都独立观察过 RED。

- 初版 `node --test tools/bili-whale/protocol.test.mjs tools/bili-whale/sender.test.mjs`：31/31通过（17个既有协议测试、14个发送测试）。
- 质量审查发现核验预检失败会误报not_posted且丢失attempt_id。先新增CLI缺凭据/合成预检失败/合成SIGTERM取消三条反例，执行 `node --test --test-name-pattern="CLI核验" tools/bili-whale/sender.test.mjs` 实际RED为0通过/3失败，均观察到实际not_posted与预期unknown不符；修复后全量34/34通过（17个既有协议测试、17个发送测试）。取消用例通过子进程内process.emit执行已注册处理器，不宣称验证Windows原生强杀会产帧。
- `node --check tools/bili-whale/danmaku.mjs` 与 `node --check tools/bili-whale/sender.test.mjs`：通过。
- 子进程握手通过子进程内注入 fetch 测试，无真实网络。其他CLI反例使用清空的凭据环境，在凭据/参数/输入层失败。
- 未执行真实B站/真实模型/真实凭据操作；没有读取上游运行状态，没有 Git 操作。Go/契约/PG集成及真实平台响应形态仍未验证，不计作已完成0.8。

完成该快照后暂停写入，供父线程规格与质量复核；DSH 独立验收及契约批准仍须单独完成。
