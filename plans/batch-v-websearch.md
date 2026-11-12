# 批 V：#92 web_search 公共 SearXNG 多实例轮换

## 背景

主人不想常开 Docker，也不想为搜索 API 付费/限额 → 用**公共 SearXNG 实例**做免费无限搜索。
`[web]` 段已有 `search_url` / `protocol`（generic/serper/bing）/ `allowed_domains` / `search_key_env`，
缺的是**searxng 协议**与**多实例轮换/降级**。

## 已确认的关键坑（实现必须处理）

1. **公共实例大多不返回 JSON**：SearXNG 的 `format=json` 需实例在 settings.yml 里显式开启，公共实例通常只给 HTML。
   → 实现：先试 `format=json`；403/非 JSON → **退化为 HTML 解析**（解析结果 `<article class="result">` 的标题/链接/摘要）。
2. 实例会限速（429 / 硬性验证码）→ 视为该实例本轮不可用，跳到下一个。
3. 结果域名白名单应作用于**最终 URL**，与现有 `allowed_domains` 语义一致。

## 修法

1. **协议 `searxng`**：`GET {search_url}?q={query}&format=json`（可选 `&language=zh-CN`）；
   JSON → `results[].title/url/content`；失败或 403 → HTML 解析兜底。
2. **多实例**：新增 `search_fallbacks = [...]`（1-8 个）；总尝试上限 6 次，单实例超时 8 秒；
   全部失败返回明确错误「搜索暂不可用（所有实例失败）」，工具层降级（不编造结果）。
3. **重试**：同实例 429 → 立刻换下一个；网络错误 → 该实例记一次失败，本轮不再重试。
4. **白名单**：`allowed_domains` 在合并去重后按最终 URL 过滤（现有逻辑复用）。
5. **可观测**：每次尝试打 `web_search` 日志（实例、状态、耗时），成功记录最终实例。
6. **配置示例**：
   ```toml
   [web]
   search_url = "https://searx.be/search"
   protocol = "searxng"
   search_fallbacks = ["https://search.bus-hit.me/search", "https://priv.au/search"]
   allowed_domains = []
   ```

## 验收

- 单测：searxng JSON 解析 / HTML 兜底解析 / 429 跳实例 / 全失败明确报错 / 白名单过滤 / 超时
- 真机：配 2-3 个公共实例，搜一次「B站 直播」，返回带标题+URL 的结果；断网/坏实例场景能自动降级

## 执行规则

fishd 不重启（交付后 DSH 验证）；不执行 git；不改 personas。

## 交付状态（2026-11-13）

- V1/V2/V3 已完成：`searxng` 协议、`search_fallbacks`（≤8）、总尝试 ≤6、单实例 8 秒超时、
  JSON→HTML 兜底、429/反爬/网络错误换实例、合并去重后白名单过滤、`action=web_search` 尝试日志。
- 单测覆盖：JSON 解析、HTML 兜底（403/200 非 JSON）、429 跳实例并记日志、反爬页跳实例、
  全失败明确报错、超时换实例、6 次尝试上限、去重后白名单、HTML 解析边界。
- 真机探测（2026-11-13，本机网络）：
  - `searx.be` → 200 但为 Anubis「Verifying your browser…」；
  - `search.inetol.net` → 200 但为 Substation「Security check」；
  - `baresearch.org` → 200 但为 Anubis「Making sure you're not a bot!」；
  - `priv.au`、`searx.tiekoetter.com` → 429；`opnxng.com` → 网络错误。
  - 结论：本机网络下**没有**公共实例能返回真实结果，属外部反爬/限速，不是客户端缺陷。
  - 用实际配置跑客户端（3 实例）实测到：`searx.be` blocked 1.8s → `search.inetol.net` timeout 8.0s
    → `baresearch.org` blocked 1.3s，最终返回「搜索暂不可用（所有实例失败）」，且每次尝试都有
    `action=web_search` 日志；超时、反爬识别、轮换与降级均按预期工作。
- `config/fishd.toml` 已配 3 个公共实例（searx.be + 2 备用）；DSH 可在其它网络复测或替换实例。
  自建 SearXNG 需在 `settings.yml` 开启 `format: json` 才走 JSON，否则自动走 HTML 兜底。
