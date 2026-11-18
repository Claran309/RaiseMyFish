# 生图风格深度指南索引（styles/）

风格选型后，**必须**读对应风格指南再写 image_prompt。每份指南含：美学 DNA / 视觉要素解构 /
完整配方与构图变体 / 多模型适配 / 分类绑定表 / 失败模式与修正 / 延伸风味。
数据底座：本地 6081 卡封面普查 + 2026 各社区口碑调研（吉卜力单日 2300 万提示词 / AI 手办社交爆款 /
MJ sref 热门 / 小红书电子功德赛道）+ 三轮 20+ 张全量实测（gpt-image-2 / seedream-5.0 / z-image-turbo / MJ v7 / niji 7）。

## 风格总表（18 分支）

> **参考图说明**：表中 `ref-XX` 是内部参考图编号，**本仓库不随附这批图**（含非我方来源素材，不宜再分发）。使用时按各指南「参考图选择」行的气质描述自备同风格参考图走图生图；没有参考图时，按指南「视觉要素解构」的完整风格句核纯文生图同样可用。

| 指南 | 风格 | 参考图 | 首选模型 | 心智定位 |
| --- | --- | --- | --- | --- |
| [style-hanfeng.md](style-hanfeng.md) | 韩系半写实（默认） | ref-01~04 | gpt-image-2 | 头部卡站对标·颓废高级 |
| [style-realistic.md](style-realistic.md) | 写实摄影（真人质感） | 无需 | gpt-image-2 / seedream | 猫箱对标·现实系代入天花板 |
| [style-anime-impasto.md](style-anime-impasto.md) | 日系柔光厚涂 | ref-08 | gpt-image-2 | 酒馆社区大盘 |
| [style-anime-cel.md](style-anime-cel.md) | 赛璐璐平涂 | ref-06 | z-image-turbo / niji 7 | 青春明快 |
| [style-guofeng.md](style-guofeng.md) | 国风厚涂 | ref-09 | gpt-image-2 | 古风华丽 |
| [style-gothic.md](style-gothic.md) | 哥特厚涂 | ref-07 | gpt-image-2 | 暗调华丽 |
| [style-ghibli.md](style-ghibli.md) | 吉卜力水彩 | 无需 | gpt-image-2 | 治愈顶流 |
| [style-blindbox.md](style-blindbox.md) | 盲盒手办 3D | 无需 | gpt-image-2 | 社交爆款 |
| [style-impasto-jp.md](style-impasto-jp.md) | 日式厚涂概念 | 无需 | gpt-image-2 / niji 7 | 高级感男主 |
| [style-ethereal.md](style-ethereal.md) | 唯美幻想 | 无需 | gpt-image-2 | 仙气幻想 |
| [style-inkwash.md](style-inkwash.md) | 新中式水墨 | 无需 | seedream-5.0 | 意境文人 |
| [style-retro90s.md](style-retro90s.md) | 90s 复古赛璐璐 | 无需 | gpt-image-2 | 怀旧氛围 |
| [style-pixel.md](style-pixel.md) | 像素艺术 | 无需 | gpt-image-2 | 复古游戏 |
| [style-manga-mono.md](style-manga-mono.md) | 黑白漫画 | 无需 | gpt-image-2 | 本子扉页 |
| [style-dopamine.md](style-dopamine.md) | 多巴胺撞色 | 无需 | seedream-5.0 | 元气网感 |
| [style-y2k.md](style-y2k.md) | Y2K 千禧蒸汽波 | 无需 | seedream-5.0 | Z 世代猎奇 |
| [style-amcomic.md](style-amcomic.md) | 美式漫画 | 无需 | seedream-5.0 / MJ v7 | 欧美硬汉 |
| [style-chibi2d.md](style-chibi2d.md) | Q版二头身 | 无需 | seedream-5.0 | 软萌彩蛋 |

## 模型适配矩阵（OpenAI 兼容网关实测结论，2026-07 模型归因实验修订；详见 SKILL.md §5）

**总优先级（实测定型）：gpt-image-2 → z-image-turbo → niji 7（开发侧，须 --sref）→ seedream-5.0**，排在前的可用就不往后走。

| 模型 | 速度 | 中文书法标题 | 参考图 | 长处 | 短板 |
| --- | --- | --- | --- | --- | --- |
| gpt-image-2 | 60–140s | **最稳（唯一全风格可靠）** | ✅ /images/edits（**带字参考图唯一安全**） | 质感天花板/指令服从 | 慢、贵 |
| z-image-turbo | ~10s | **偶发错字**（实测「聿→律」，生成后必须核对） | ❌ edits 网关不通 | 赛璐璐最纯正/极速打样 | 韩系偏 K-pop 写实 |
| niji 7（--niji 7） | ~130s 任务式 | ❌ | ✅ --sref（**韩系必配**，不配是日漫线条另一种画风） | 二次元线稿纯度第一/sref 后韩系可用 | 无中文标题、任务式、四格需 upscale |
| doubao-seedream-5-0-260128 | ~16–48s | **不可控**（带字垫图原样抄参考图标题、自写偶发错字） | ✅ /images/edits（**严禁配带字参考图**） | **韩系质感复原度实测第一**/水墨意境最佳/批量性价比 | 标题硬伤；prompt 里年龄数字会上屏 |
| MJ v7（mj_imagine） | ~180s 任务式 | **❌ 中文乱字** | --sref/--oref（v7+sref 实测跑真人写实，韩系不用） | 光影氛围天花板/构图艺术感 | 无中文标题、任务式慢、四格需 upscale |
| flux 系 | — | — | — | （社区口碑：写实与文字融合强） | 未纳入实测轮 |

**MJ/niji 的使用范式**：走 MJ 任务式管线（submit→poll→U1-U4 upscale→下载）。
中文标题两条路：① MJ 出无字底图 → gpt-image-2 `/images/edits` 二次加工补书法标题；② 出图后前端叠字。
prompt 全英文+尾部参数（`--ar 9:16 --niji 7` 或 `--v 7 --stylize 250`），--niji 与 --v 二选一。

## 路由规则

1. 用户点名风格 → 直接读对应指南。
2. 用户只给分类 → 查各 genre 指南 §8 的「本分类风格路由」行（首选+备选），或按本表「心智定位」列匹配。
3. 用户给了参考图 → 无视路由表，gpt-image-2 + 用户参考图走 edits（风格跟图走）。
4. 竖排书法标题是全库身份标识：默认必须保留；MJ/niji 产出的无字图必须走二次加工补标题（除非用户明说不要）。
5. 批量生产（≥4 张）优先 seedream-5.0 / z-image-turbo 打样，选中后用 gpt-image-2 出正式版。
6. 风格杂交（如「水墨×赛博」）：主风格指南为骨架，副风格取 2–3 个视觉要素词嵌入——先读两份指南的「视觉要素解构」节再拼。
