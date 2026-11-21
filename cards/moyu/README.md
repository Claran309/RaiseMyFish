# 魔鱼角色卡

- `card.md`：Foreverse `card.md` 中间格式（人可读、可 diff）。
- `moyu.v2.json`：可直接导入的 `chara_card_v2` JSON。
- `moyu.png`：带封面与 `chara` / `ccv3` 两个 tEXt 块的 v3 PNG 嵌卡。
- `cover.png`：封面原图（AI 生成）。
- [`FACTS.md`](FACTS.md)：逐条事实来源核对；卡面虚构场景与已删除编造也列在这里。
- 定位：全性向 / 都市·直播间日常 / 洁 / 整活喜剧 × 人外萌物 / 反差·嘴硬心软·治愈 / 魔的乐子人（善良底色优先，乐子只是调料）。
- 结构评分：Foreverse `tools/score_card.py` **98.7 / 100**（反 AI 味 5/5）。
- 已验证转换：`chara_card_v2` JSON（lorebook 13 条、备选开场白 1 条）+ v3 PNG 嵌卡。

## 事实来源与边界

- 形象：`C:\Users\Claran\.dsh\tts\you` 设定图（深海蓝长卷发、鲸鱼鳍耳、细金属圆框眼镜、深蓝女仆裙、白围裙鲸鱼刺绣、鲸鱼尾巴）。
- 人格：`personas/dafeiyu/` 的 soul / rules / context / knowledge / soul-memories。
- 弹幕事实：`events` 表 `live` / `comment` 的公开入向记录（只读抽取）。
- 卡面开场白是**虚构的下播日常场景**，不是真实事件记录；涉及具体邀约、日期、地点、食物、谁说过什么，一律以记忆/弹幕为准，没有就写「不记得 / 没印象」，不编造。

## 转换 / 导入

```bash
# 在 Foreverse character-card-skills 仓库根目录执行
python tools/card_md_to_st.py cards/moyu/card.md \
  --avatar cards/moyu/cover.png \
  --outdir cards/moyu --slug moyu
```

导入 SillyTavern：角色页「导入角色」选 `moyu.png` 或 `moyu.v2.json`。
导入 Foreverse：角色页导入同一份 PNG / JSON。

## 说明

卡面正文使用少量内联 HTML（状态条 / 折叠面板），导入前端时需要开启 HTML 渲染。
封面为 AI 生成；`card.md` frontmatter 里的 `image_prompt` 是可复现的生图提示词。
