# 文档漂移与隐私审计（2026-11-25）

> 范围：当前 `git ls-files` 跟踪的 Markdown、配置、证据与截图；同时参考
> `README.md`、`SECURITY.md`、`docs/12-security-compliance.md`、`docs/13-deployment.md`。
> 本报告不复制真实房间号、UID、观众昵称或聊天正文；只给路径与行号。

## 结论

当前仓库把**个人部署、协作过程、真实观众数据和人格资产**与开源工程设计文档混在一起：

- 已跟踪 Markdown **316 个**；其中个人/过程相关文件 **225 个，约 16.00 MB**
  （203 个目录/文档 + 22 个含真实房间/UID 的一次性 `scripts/`）。
- 高风险：真实直播间/账号配置、真实弹幕归档、观众侧写、失败案例、角色卡、
  评审证据（含截图/数据库输出）均在仓库中。
- 文档把“个人自用部署”写成了“生产可用”，并把未实现的 retention/删除/审计承诺
  当成已完成；实际代码只保留 7 天日志，事件表没有 90 天归档逻辑。

## P0：不应公开的真实数据

| 文件 | 风险 | 证据 |
|---|---|---|
| `config/fishd.toml` | 真实直播间、本人 UID、主人 UID、主播名、真实送礼开关与房间白名单 | 第 39、40、88、277、278、328、335、362、414、476 行 |
| `docs/handoff-2026-11-17.md` | 本机绝对路径、真实房间、启动/强杀步骤、真实事件量、真实费用、孤儿 node 事故 | 第 3–5、14、23、34、86 行 |
| `shots/弹幕归档-2026-11-05.md` | 真实弹幕/昵称/时间线（30 处 PII 命中） | 全文，尤其第 1–30 行 |
| `personas/dafeiyu/knowledge/viewers.md` | 自动生成的观众侧写：昵称、偏好、出现次数、关系 | 文件约 238 KB；条目 `viewer-auto-*` |
| `plans/failure-cases.md` | 真实观众名、真实聊天原文、真实 UID 行为 | 第 7–55 行 |
| `cards/moyu/` | 个人角色卡、剧情、设定与 4.2 MB 图片 | `card.md`、`FACTS.md`、`moyu.v2.json`、`*.png` |
| `scripts/` 中一次性脚本 | 真实房间/UID、送礼循环、弹幕测试、评论回复原文 | 例如 `gift-loop.sh`、`send-danmaku.py`、`test-danmaku.sh`、`probe-dynamics.py` |

这些内容不是“示例数据”。即使后续改名或删除，**已推送的 git 历史仍保留**；
需要从当前树移除、停止继续上传，并按泄露范围轮换 B 站 cookie/CSRF 与相关密钥。

## P1：个人过程/协作材料

| 目录 | 规模 | 说明 |
|---|---:|---|
| `docs/reviews/` | 84 个 docs 文件中的主要部分 | 评审截图、probe 输出、数据库证据、真实观众样本 |
| `docs/reports/` | 除本次运维报告外的历史短测/实验 | 个人测试记录，不是项目规范 |
| `docs/update/`、`docs/releases/` | 多个 | 会话更新与发布记录 |
| `shots/` | 10 个文件，约 769 KB | 面板截图，可能含真实数据 |
| `cards/moyu/` | 6 个文件，约 4.2 MB | 个人角色卡与图片 |
| `agents/` | 5 个文件 | 多 agent 协作协议，含本机路径 |
| `plans/` | 95 个文件，约 486 KB | 派发单、批次记录、遗留清单、handoff 计划 |
| `personas/dafeiyu/` | 36 个文件，约 10.1 MB | 真实人格、梗库、灵魂记忆、观众档案、评测样本 |

建议：这些内容迁到仓库外的私有目录（例如 `../RaiseMyFish-private/`），
或至少从 git 索引移除并加入 `.gitignore`；开源仓库只保留：
`docs/01..14` 设计文档、`contracts/`、`README`、`SECURITY`、`CHANGELOG`、
脱敏后的部署示例、`docs/testing.md` 与脱敏运维报告。

## P2：文档漂移（与代码/实测不一致）

1. **“生产可用”过度承诺（本轮已改措辞）**
   - 原 README 写直播弹幕“✅ 生产可用”；本轮已改为“个人单机可用（非生产 SLA）”。
   - 实际是单进程、单 PG、单账号、无 HA、无多租户；240 条/分钟压测出现明显积压
     （见 `bench/raw/R1-20261125-021441/drain.json`：测量结束 pending 268，
     180 秒 drain 后仍有 9 条；live P99 约 229 秒）。
   - 建议改成“个人单机稳定运行；非商业多租户/高可用生产系统”。

2. **“1 秒级”没有负载前提（本轮已补限定）**
   - 原 README 把低负载观测写成通用 SLA；本轮已补“低负载”与高负载积压说明。
   - 本次 staging 在 24/48 条/分钟下 P99 约 0.25 秒，但 240 条/分钟出现长尾；
     应把“1 秒级”改为“低负载 fresh 样本”，并链接容量报告。

3. **数据保留承诺未实现（文档已标“计划项”，代码仍未实现）**
   - `docs/12-security-compliance.md:75` 写“事件保留 90 天（之后归档）”，
     但代码中没有事件归档/清理任务；`internal/infra/logger.go:71` 实际只保留
     **7 天**日志。
   - `docs/13-deployment.md:339` 写日报保留 90 天；与日志清理口径不一致。
   - `docs/12-security-compliance.md:76` 写“支持 `fishctl memory delete`”，
     当前 CLI 未提供该删除命令。
   - 建议把第 6 节标成“计划/未实现”，或实现 retention 后再写承诺。

4. **凭据边界被写成安全保证（SECURITY 顶部已补适用范围）**
   - `docs/12-security-compliance.md:21` 明确 cookie/CSRF 在 `accounts` 表明文存储，
     仅靠文件权限；这可以接受为个人本机取舍，但不能写成通用合规能力。
   - 建议在 `SECURITY.md` 顶部加“个人自用、非合规产品”的适用范围，
     并写明备份/共享电脑/磁盘未加密的风险。

5. **未完成事项仍在 README 的“能力矩阵”里**
   - `plans/leftovers.md:106` 的 #59 发送异步化仍未做；`docs/handoff-2026-11-17.md:34`
     的孤儿 node 清理仍未做。README 的“自愈/生产可用”应降到“部分实现”。

6. **LICENSE 已补（MIT）**
   - 根目录已新增 `LICENSE`（MIT，Copyright (c) 2026 Claran309）；
     README 的 License 段已同步。

## 建议的清理顺序

1. **先止血**：把 `config/fishd.toml` 换成脱敏 `config/fishd.example.toml`；
   真实配置只留本地并加入 `.gitignore`；轮换已公开过的 B 站 cookie/CSRF。
2. **移出个人材料**：`docs/handoff-*`、`docs/reviews/`、`docs/update/`、
   `docs/releases/`、`shots/`、`cards/`、`agents/`、`plans/`、
   `personas/dafeiyu/` 的私有部分。
3. **修 README/SECURITY/合规文档**：把“生产可用”和未实现承诺改为实际状态，
   补“个人自用、非多租户/非高可用”的边界。
4. **加自动门禁**：CI 扫描真实房间号/UID/cookie 形态、`docs/handoff-*`、
   `docs/reviews/`、`shots/`、`cards/`；命中即失败。
5. **许可证已决定**：MIT License，根目录与 README 已同步。

本轮已做：README/SECURITY/docs12/docs13 的措辞校正；`.gitignore` 增加个人材料规则；
执行 `scripts/privacy-audit.ps1 -Apply` 并补移除 22 个一次性脚本，从 git 索引移除 225 个个人/过程文件
（本地文件仍在），未 commit/push。

## 复现命令

```powershell
# 统计个人/过程文件
.\scripts\privacy-audit.ps1

# 查看 PII 命中（只列文件，不打印正文）
rg -l -i '<本机私密房间/UID pattern>|SESSDATA|bili_jct|D:/CodeStudy|真实直播间|观众昵称' docs plans shots cards personas agents config

# 查看未实现承诺
rg -n '90 天|保留|删除|审计|明文' SECURITY.md docs/12-security-compliance.md
rg -n 'cutoff :=|fishd-.*log' internal/infra/logger.go
```

> 本轮已按“个人材料不应继续上传”的要求移除索引中的 225 个文件；
> `LICENSE` 已按 MIT 添加。历史提交中的旧内容不会因本次索引移除消失，若已推送需
> 按泄露范围轮换 B 站 cookie/CSRF。
