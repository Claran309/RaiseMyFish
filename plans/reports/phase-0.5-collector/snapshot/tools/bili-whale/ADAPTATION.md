# 直播弹幕采集器移植说明

源包为 `dsh-bili-whale@0.4.5`，MIT，Copyright (c) 2026 ALTERSSSS。完整许可证原样保存在 [LICENSE](LICENSE)。

- 本机源路径：`C:/Users/Claran/.dsh/profiles/desktop/node_modules/dsh-bili-whale/tools/commands/danmaku-await.mjs`
- 源文件 SHA-256：`8d9ca3e838ff3ad99584f6b8169360e87b42cf83c3f33f5c0613dfbeba03b5e3`
- 移植日期：2026-11-01。

保留源脚本的 WBI 混淆表和签名算法、16 字节包头、zlib/Brotli 解包、WS 认证与 30 秒心跳思路。生产入口保留同名 `danmaku-await.mjs`；纯协议函数移到 `protocol.mjs`。使用 Node.js 24 标准库与内置 WebSocket，无 npm 依赖。导入模块不启动 CLI，不读凭据。

源脚本实际默认轮询 history，实验 WS 分支每 25 秒轮换连接。本移植只启动一个常驻 WS，直到断开、取消、握手超时或收不到心跳响应而退出；重连、节点轮换、退避与持久化由 Go 父进程负责。删除 DSH 配置、主人/昵称/模板过滤、历史补漏、假 ID、文件队列、状态文件及正文日志。仅过滤已验证 UID 的自身消息，其他观众消息包含重复消息均产出，交总线幂等处理。

## 运行与凭据

```powershell
node tools/bili-whale/danmaku-await.mjs --room 123 --self-id 456
```

`--room` 是配置房间号（允许平台解析短号），`--self-id` 必须匹配 nav 的已登录 UID。可选参数及默认值：

| 参数 | 默认值 | 含义 |
|---|---:|---|
| `--http-timeout-ms` | 10000 | 每个 HTTP 请求与响应读取超时 |
| `--connect-timeout-ms` | 15000 | 连接及认证完成时限 |
| `--heartbeat-ms` | 30000 | 认证后的心跳发送间隔 |
| `--idle-ms` | 70000 | 认证成功/上次 op3 后的存活时限 |
| `--host-index` | 0 | 父进程指定的候选节点索引，按节点数取模 |

Cookie 仅使用显式 `FISH_BILI_COOKIE` 或 `FISH_BILI_COOKIE_FILE`，前者优先。文件支持 Cookie 文本或含 `sessdata`、可选 `bili_jct`/`buvid3` 的原 credentials JSON，上限 64 KiB。不会搜索主目录，不将 Cookie 放入命令行、URL 或日志。

启动固定顺序请求 nav、get_info、getDanmuInfo；HTTP 仅允许固定 B站 HTTPS API 主机并拒绝重定向。WS 节点仅允许 `*.chat.bilibili.com` 的 `wss` URL。弹幕令牌只写认证包。

## stdout 协议

每行一个 JSON 对象，`protocol: 1`。stdout 只用于协议，不写诊断文字或平台原始异常。

| type | 字段与语义 |
|---|---|
| `ready` | 仅 op8 认证 `code:0` 后产出；含规范 `room_id`、已验证 `self_id` |
| `event` | `room_id`、`msg_id`、`from_id`、`from_name`、`text`、RFC3339 平台时间 `at` |
| `heartbeat` | 只在收到 op3 时产出，发送心跳不代表连接存活 |
| `error` | 固定安全 `kind`、`fatal`、`retryable`；不附原始异常、Cookie、令牌或完整 payload；内部 JavaScript Error.code 不作为帧字段输出 |

ID 只取显式 `id_str`/`dmid`，允许字母、数字、下划线、短横线，最多 128 字符；不使用 `info[0][5]` nonce、时间或正文哈希。JSON 数字字面的不安全整数用 Node 24 reviver `context.source` 保留为原始十进制串。UID 和房间 ID 只接受正十进制串。时间只取 `info[0][4]` 的秒/毫秒平台值。缺 ID/时间和单条字段错误产出非致命错误并跳过，不伪造数据。

协议输入、累计未完成包与每次解压总量上限各为 1 MiB；压缩递归最多 4 层，每次解包最多 256 包。stdout 在途写入上限为 1 MiB，超过后受控退出；退出时只给管道有限排空时间。SIGINT/SIGTERM 取消当前 HTTP/WS，清理定时器并结束，模块不建立重连循环。

退出码：`0` 正常取消；`10` auth/credentials（暂停待处理）；`2` runtime（暂停）；`11` network/rate_limit；`12` timeout；`13` protocol/backpressure。单条 `missing_message_id`、`missing_timestamp`、`invalid_message`（以及嵌入 JSON 解析错误）以 `fatal:false,retryable:false` 上报并继续接收。

## 离线验证与限制

```powershell
node --test tools/bili-whale/protocol.test.mjs
```

测试使用合成包、可注入 fetch/WS、临时测试凭据文件和虚拟时钟，不连接真实平台。覆盖稳定 ID/大整数、分帧粘包、压缩和资源上限、凭据来源/泄露、目标限制、认证、自过滤/重复消息、常驻连接、握手/心跳超时、关闭/错误、取消与输出背压。

本轮没有真实平台监听/长连接验证；实际房间是否持续提供可用稳定 ID、平台节点行为和离线漏消息情况需后续独立验证。WS 不补回进程离线期间的弹幕，不声称已达到全 Phase 0 的两小时与离线不漏门槛。
