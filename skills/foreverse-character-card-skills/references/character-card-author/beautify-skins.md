# 主题皮肤模板库（v10.5）

> 来源：小红书酒馆美化二轮调研（敦煌绢本 272 赞「让我嫁给玲」/ Y2K 千禧合集 1120 赞 / 拟物银行卡状态栏 1734 赞）。
> 高赞美化的共性 = **完整文化母题皮肤**，不是配色好看。每套模板含 L1 容器 + L2 拟物件状态栏，直接复制改字段。
> 预算护栏不变：开场白 HTML ≤15KB、无外链 img/font/audio、视觉全 CSS+emoji+内联 SVG。

## 皮肤一 · 敦煌绢本（古风/民俗/仙侠/权谋）

设计要点：绢本底色（暖沙金渐变）+ 卷轴式头部 + 「题字条」竖排标题 + 文房印章角饰 + 纸卷正文区。数值伪装成「功德/心念」类世界内概念。

```html
<div style="border-radius:6px;background:linear-gradient(160deg,#c9a876 0%,#e8d5ae 30%,#dfc99b 70%,#b8935f 100%);padding:3px;">
<div style="border:1px solid rgba(120,80,30,.45);border-radius:4px;background:linear-gradient(175deg,#f4e8cd 0%,#eddfbc 100%);padding:14px 16px;color:#4a3a22;font-size:14px;line-height:1.85;">
<div style="display:flex;justify-content:space-between;align-items:flex-start;border-bottom:2px solid rgba(140,90,40,.35);padding-bottom:8px;margin-bottom:10px;">
  <div style="font-size:12px;color:#7a5c34;">敦煌 · 莫高窟第 220 窟外 · 暮鼓时分</div>
  <div style="writing-mode:vertical-rl;font-size:16px;letter-spacing:4px;color:#5a3a1a;border:1px solid rgba(140,90,40,.4);padding:6px 3px;background:rgba(255,250,235,.5);">一眼千年</div>
</div>

（正文……对白用 <span style="color:#8a2be2;">「」</span> 或加粗；此处开演。）

<div style="margin-top:12px;border:1px dashed rgba(140,90,40,.45);border-radius:4px;padding:8px 10px;background:rgba(255,250,235,.45);">
  <div style="font-size:12px;color:#7a5c34;margin-bottom:6px;">◈ 行囊 · 心念卷</div>
  <div style="display:flex;justify-content:space-between;font-size:12px;">
    <span>心念相通</span>
    <span style="letter-spacing:2px;">▰▰▰▱▱▱▱ 三成</span>
  </div>
  <div style="display:flex;justify-content:space-between;font-size:12px;margin-top:4px;">
    <span>壁画残卷</span><span>已集 2 / 7 卷</span>
  </div>
  <div style="text-align:right;margin-top:6px;"><span style="display:inline-block;border:1.5px solid #a33;color:#a33;font-size:10px;padding:2px 3px;border-radius:2px;transform:rotate(-6deg);">朱砂印</span></div>
</div>
</div></div>
```

## 皮肤二 · 千禧电子宠物（Y2K/网恋电竞/整活/怀旧）

设计要点：Windows98 窗口壳（标题栏+最小化按钮）+ 粉紫像素感底 + 「金币计数器」拟物数值 + 状态栏做成 `日记.txt` 记事本窗口。禁真像素字体外链——用等宽系统字模拟。

```html
<div style="border-radius:2px;background:#e8d0f0;border:2px solid #b090c8;box-shadow:3px 3px 0 rgba(120,80,150,.4);font-size:14px;color:#4a3050;">
<div style="display:flex;justify-content:space-between;align-items:center;background:linear-gradient(90deg,#c8a0e8 0%,#e8b8d8 100%);padding:4px 8px;border-bottom:2px solid #b090c8;">
  <span style="font-size:12px;font-weight:bold;color:#fff;">💾 聊天室_2000.exe</span>
  <span style="font-size:11px;color:#fff;background:#b090c8;padding:0 6px;border-radius:1px;">— □ ×</span>
</div>
<div style="padding:12px 14px;line-height:1.8;background:repeating-linear-gradient(0deg,#f8ecf8 0,#f8ecf8 26px,#f0e0f4 27px);">

（正文……对白高亮用 <b style="color:#c04888;">粉紫粗体</b>；此处开演。）

<div style="margin-top:10px;border:2px inset #d0b0e0;background:#fff8fc;padding:8px 10px;">
  <div style="font-size:11px;color:#9070a8;border-bottom:1px dashed #d0b0e0;padding-bottom:4px;margin-bottom:6px;">📄 日记.txt - 记事本</div>
  <div style="font-family:monospace;font-size:12px;line-height:1.9;">
    好感金币: <b style="color:#c04888;">0009999</b> ▲<br>
    今日已读: 47 条 │ 秒回率 98%<br>
    状态: <b>在线</b> · 挂机借口第 3 个<br>
    <span style="color:#b8a0c8;">最后编辑 23:47 ごめん</span>
  </div>
</div>
</div></div>
```

## 皮肤三 · 拟物银行卡（都市/职场年上/黑道——数值拟物件的标准示范）

设计要点（小红书 1734 赞样式反推）：状态栏不做进度条，做成一张**角色世界里的真实卡片**——卡号、余额、带叙事的流水行。流水行是灵魂：每条支出讲一个剧情梗（「-¥12 购买辣味热狗」）。

```html
<div style="margin-top:12px;border-radius:12px;background:linear-gradient(135deg,#1a1a1e 0%,#2a2a30 60%,#1e1e24 100%);padding:14px 16px;color:#d8d4c8;box-shadow:0 4px 12px rgba(0,0,0,.35);">
  <div style="display:flex;justify-content:space-between;font-size:11px;color:#9a9484;">
    <span>◼◼ PLATINUM · 关系账户</span><span>VALID THRU ∞</span>
  </div>
  <div style="font-size:20px;letter-spacing:2px;margin:10px 0 4px;">¥ 8.71</div>
  <div style="font-size:11px;color:#8a8474;">CURRENT BALANCE · 好感余额</div>
  <div style="border-top:1px dashed rgba(200,190,160,.25);margin-top:10px;padding-top:8px;font-size:12px;line-height:1.9;">
    <div style="display:flex;justify-content:space-between;"><span>IN +¥0 　今日他一句软话没说</span></div>
    <div style="display:flex;justify-content:space-between;"><span>OUT −¥1.29 替你挡酒（未入账·你不知道）</span></div>
  </div>
</div>
```

## 使用规约

1. 皮肤母题必须与卡的题材呼应（敦煌皮不配赛博卡）；一卡一皮肤，禁止混搭两套母题。
2. 模板字段全部可改：地点条/数值名/流水文案按卡改写，**流水行必须带叙事**（每条是一个剧情梗，不是干数字）。
3. 配色可整体换色相（敦煌皮换青绿=千里江山皮；Y2K 粉紫换蓝白=初代 QQ 皮），结构不动。
4. 折叠面板（`<details>`）可嵌进任意皮肤的正文区，样式继承皮肤色系。
5. XML 渲染协议（SKILL §4）优先级高于皮肤：复杂状态栏仍走「AI 输出数据块+regex 替换模板」，皮肤只是模板的视觉层。
