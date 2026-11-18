---
name: dice
description: 观众要求掷骰子、随机数或抽签时，用本地脚本产生真实随机结果
version: 1
entry: scripts/roll.ps1
timeout_seconds: 5
---

# 掷骰子技能

适用场景：观众说「掷骰子」「roll」「1d6」「随机一个数」「抽签」。

调用约定：
- `FISH_SKILL_ARGS` 传骰子个数（1-6），留空默认 1 个。
- 脚本返回真实随机结果，不要在回复里自己编数字。

回复方式：先报脚本结果，再用大肥鱼口吻接一句；不要解释随机算法。
