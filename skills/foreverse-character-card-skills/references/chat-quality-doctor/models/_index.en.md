# Per-model symptom profile index (English)

> Note: the per-model profile bodies are currently written in Chinese — they are data files, and agents can read them directly. For a human-readable English summary, the table below (with its one-line takeaways) is sufficient.

Before recommending any model switch, read the matching profile and relay its confidence labels honestly (SKILL §6).

Four confidence tiers:

- `observed`: traceable internal measurement (result JSON + date + seed count on file) — numbers may be quoted.
- `community`: sourced community consensus (ST Chinese tutorial-site roundups / high-upvote Xiaohongshu posts, collected in the 2026-07 community survey) — qualitative relay only.
- `hypothesis`: speculation; must be flagged as speculation whenever cited.
- `stale`: historical experiment summary (raw data not archived) or unretested for over a quarter — **quoting precise numbers is forbidden**; say only "one old experiment round, pending retest".

| Profile | Measured status | Reputation status | One-line takeaway |
| --- | --- | --- | --- |
| deepseek-chat.md | stale (27-arm comparison, historical summary; blind retest pending) | community | Community-rated "Chinese-prose ceiling" and price-performance king — the entry pick and daily-chat workhorse; for romance RP use the chat series, not the R reasoning series. |
| glm-4.6.md | stale (same as above) | not collected | No community data; the stale internal round recorded it as the biggest gainer from rhythm constraints — state a length cap and it obeys, so keep the cap at ≤120 characters, no tighter. |
| kimi-k2.6.md | stale (same as above) | not collected | No community data; stale internal round: long-winded when bare, converges sharply under the four laws; interface note — temperature only accepts 1.0. |
| qwen-plus.md | stale (same as above) | not collected | No community data; stale internal round: its poem-style line-break tic is the main AI-flavor source — the four laws only work on it with an explicit layout clause. |
| claude.md | none | community | Community's "liveliness ceiling": subtlest prose, real pushback and subjecthood — but the priciest, with the strictest safety layer; the standard community pattern is "DeepSeek/Sonnet for daily chat, switch to Opus for key scenes". |
| gpt.md | none | community | Community's "guiding partner": stable and considerate, but emotional intensity deliberately damped and input filters touchy; RP immersion rated below Claude/DeepSeek. |
| gemini.md | none | community | Community's "logic anchor": million-token context makes it the long-serial and memory-pain remedy, and it's cheap — but its Chinese prose is flat, so it's the wrong switch for "no flavor" complaints. |
| grok.md | none | community | Community's "uncensored maverick": loosest content limits (the NSFW fallback slot); prose and stability rated middling — the wrong switch for prose-quality complaints. |

Models not on this list = no profile data of any kind; when recommending, say honestly "not tested, only trying will tell".
