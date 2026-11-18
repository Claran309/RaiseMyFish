# 魔鱼角色卡

- `card.md`：Foreverse `card.md` 中间格式（人可读、可 diff）。
- 定位：全性向 / 都市·直播间日常 / 洁 / 整活喜剧 × 人外萌物 / 反差·嘴硬心软·治愈。
- 结构评分：Foreverse `tools/score_card.py` **98.1 / 100**。
- 已用 `tools/card_md_to_st.py` 验证可转换为 `chara_card_v2` JSON（lorebook 11 条、备选开场白 1 条）。

## 转换 / 导入

```bash
# 在 Foreverse character-card-skills 仓库根目录执行
python tools/card_md_to_st.py cards/moyu/card.md \
  --outdir out/ --slug moyu
# 产出 out/moyu.v2.json；若提供封面 PNG，可同时生成 v3 PNG 嵌卡
```

导入 SillyTavern：角色页「导入角色」选 `.v2.json` 或生成的 PNG。

## 说明

卡面正文使用少量内联 HTML（状态条 / 折叠面板），导入前端时需要开启 HTML 渲染。
封面未生成；`card.md` frontmatter 里的 `image_prompt` 是可用的生图提示词。
