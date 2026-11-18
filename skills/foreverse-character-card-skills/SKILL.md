---
name: foreverse-character-card-skills
description: Foreverse 角色卡创作与聊后调优技能包（写新卡/改卡/诊断出戏/AI 味/文风），完整资料按需分页读取
version: 1
---

# Foreverse 角色卡技能包

这是一个**入口技能**，完整原文在 `references/` 下。不要一次读完整包；按当前任务只读一个子技能，再按需读它的分类/风格文件。

## 子技能

| 何时用 | 读这个文件 |
|---|---|
| 写中文角色卡、改卡、开场白、世界书、卡面文案 | `references/character-card-author/SKILL.md` |
| 写英文角色卡 / 英文平台受众 | `references/character-card-author-en/SKILL.md` |
| 已经聊过但觉得太 AI、OOC、记不住、小作文、替用户说话 | `references/chat-quality-doctor/SKILL.md` |
| 仿写文风、去 AI 味、长篇/卡面文案的语料腔调 | `references/style-anchor-fusion/SKILL.md` |

## 读取方式

用 `skill_read` 的分页参数；先读子技能正文，再按它里面的相对路径读具体资料：

```text
skill_read {"name":"foreverse-character-card-skills","file":"references/character-card-author/SKILL.md","offset":0,"limit":1800}
```

分类指南在 `references/character-card-author/genres/`，风格指南在 `references/character-card-author/styles/`，示例卡在 `references/character-card-author/examples/`。每次只读当前任务需要的一个文件，读到哪一页由 `offset` 控制。

## 使用边界

- 这是创作辅助技能，不是直播回复技能；直播回复不要调用它。
- 生成的角色卡先给主人看，不要直接写进人格包或记忆。
- 完整资料来自 Foreverse `character-card-skills`：代码 MIT，技能/文档/卡片 CC BY 4.0，使用或改编时保留署名。
