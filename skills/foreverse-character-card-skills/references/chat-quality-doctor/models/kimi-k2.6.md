# kimi-k2.6（月之暗面 Moonshot）

meta: providerId=moonshot · 测试日期=2026-07（2026-07 内部实验轮）· 原始 arm JSON 未入库 → 实测节整体 `stale`
适用语言=中文 RP · 复测计划=下一轮盲测

## 默认输出形态 [stale·历史报告摘要]

bare 时偏长；注入节奏约束后收敛显著（与 glm 同属「约束遵守度极高」组）。

## AI 味主源 [stale]

约束缺失时的长旁白。具体例句待复测后补。

## 已验证有效条款 [stale]

四律注入收益大（历史轮记录 AI 味降幅明显）。禁止引用具体分数。

## 已验证无效/矫枉过正 [stale]

与 glm 同：字数上限过低（≤60 字级）→ 电报腔反噬。上限 ≤120 字为宜。

## 社区口碑摘要

未系统采集。不得编造口碑。

## 推荐参数与坑 [stale·style_lab 代码注释（接口约束记录，非实验产物）]

temperature 只接受 1.0（style_lab `--actor-temperature` 注释记录的接口约束；复测轮验证后升 observed）。
