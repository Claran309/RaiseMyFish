---
name: character-card-author (English)
description: Author high-liveliness roleplay character cards (SillyTavern chara_card_v2/v3 compatible) with an anti-AI-flavor discipline distilled from a statistical read of 20,000+ community cards, 2,900 unpacked tavern cards / 64,766 lorebook entries, and four renowned community presets. Workflow: the four-axis positioning → 47 genre guides → the five opening paradigms + the nine liveliness techniques + five lorebook paradigms + the four output-rhythm laws + card-copy prose discipline, backed by a runnable scorer and AI-flavor detector. Use when the user says "write me a character card", "make a chattable male lead / female lead / ensemble / simulator", or "make it not sound like AI". English mirror of SKILL.md.
---

# Character Card Authoring (character-card-author)

> English mirror of the Chinese `SKILL.md`, which is the normative source. Facts, numbers, and thresholds are carried over verbatim. Chinese example sentences are kept in the original with literal English glosses — they are precedents, and the ban only makes sense against the original wording.

Use this skill when the user says "write me a character card", "make a chattable male lead / female lead / ensemble / simulator", or "make it not sound like AI". The output is the `card.md` intermediate format (see `docs/card-md-format.md`); repo tools convert it into SillyTavern-importable chara_card_v2 JSON / v3 PNG-embedded cards.

Fact baseline (from 19,776 community-card metadata records + full-text deep reads of 1,000 hit cards on a leading character-card platform + a full unpack of 2,907 tavern cards / 64,766 lorebook entries + teardowns of four renowned community presets — 冥王星 (Pluto), 双人成行 (It Takes Two), 小图书馆 (Little Library), 宇宙图书馆 (Cosmic Library). Do not invent a different baseline):

- A hit card is not a persona document. It is **a scene already in progress**: time and place + an event happening right now + a problem the character is dealing with + a hook that drags the user in.
- Three pillars of liveliness: **flaws, non-compliance, a private life**. The preset side adds two more: **cognitive limits** (the character can only guess at the user from surface observation, and will guess wrong or misread) and **emotional inertia** (last turn's emotion bleeds into this turn; a habitual action buffers before the character speaks).
- Opening-message body text (HTML stripped) runs **800–2,600 characters** in the mainstream. Longer is not better.
- Every field on a beautification panel is a "to be loaded" plot slot the AI fills each turn; beautification is, at bottom, defining a structured output format.
- **Lorebooks are standard equipment on community cards** (89% of cards in the unpacked corpus carry one): 8–15 entries for a single-character romance card, 20–40 for a multi-NPC world card, 40+ for system-flow/simulator cards; community average for alternate greetings is 2–3.
- Cover art = Korean-style semi-realistic vertical illustration: matte skin, finely resolved hair strands, low-saturation black-white-gray plus a single accent color, tight face close-up, **a vertical calligraphy title plus a small vermilion seal** (applies to Chinese-language cards) — this is the single biggest factor in a "finished card" look. Not oil impasto, and not AI-plastic smoothness.
- Competitor field baseline (same-prompt, on-device comparison against leading mobile RP platforms): the top platforms' reply shape is **short (60–120 characters), fragmented (single-word sentences, ellipses), duplicitous (spoken line vs. inner OS on two tracks), anchored in physical detail, and it pushes back when offended** — the gap is in the output prose layer, not the persona layer; see system_prompt segment 7, "the four output-rhythm laws". Bio style: one sentence that is itself the relationship hook ("your female boss / your ex-girlfriend's mother" — second-person relationship definition up front), validated by traffic.

## 0. Step one is always the four-axis positioning

First write a "positioning statement" — **it goes only into a frontmatter comment of the card file or an archive document, never into `## Creator Notes`** (Creator Notes are the user-visible author's notes; product words like "benchmarked against X / differentiation / core loop" are an instant machine-flavor tell — see §2.6 defect 6). The four axes:

- **Orientation** (pick one): BG / BL / GL / GB / female-oriented / all-orientation / 限左 (top-locked) / 限右 (bottom-locked) / 不限左右 (no position lock).
- **Track** (worldview × mode): campus / urban / guofeng historical / cultivation / showbiz / interstellar / ABO / system / infinite-flow / fandom / simulator.
- **Purity**: 洁 (pure) / 不洁 (not-pure). Pure = the character's past is untouched in both body and heart; once declared, it must hold through the whole card.
- **Emotional hooks** (1–3; they define the payoff promise): hard-to-win / redemption / bittersweet / cold face, soft core / shameless eager tease / gap-contrast / grovel arc (追妻火葬场) / taboo / sharp tongue / daddy / older partner / childhood sweetheart / universal heartthrob.

All four axes go into `tags` (a card missing orientation = a failing card). Tags are themselves copy: community slang like 「不要撅他」 ("don't top him") or 「难攻略」 ("hard to win") manages player expectations. If the user gives no positioning, default to "all-orientation + modern urban + not-pure + hard-to-win/gap-contrast" and do not ask follow-up questions.

## 0.5 After positioning, read the genre guide (genres/) — mandatory

Once the four axes are set, you **must** read `genres/_index.md`, follow its routing table to the matching genre guide, and read that guide in full before writing. The 47 guides cover:

- **Female-oriented, 15**: older-abstinent / younger puppy-dog & wolf-dog / campus pure love / guofeng court intrigue / xianxia master-disciple / yandere-unhinged / sickly beauty / broken-mirror reunion / online-dating esports / showbiz / mob boss / transmigrated side character / Western supernatural (vampire gothic) / ABO / BL.
- **Male-oriented, 9**: sassy sharp-tongued gal / mature onee-san / motherly nurturing / innocent campus belle / yandere girl / sweet genki girl / harem multi / taboo tension (NTR/NTL) / yuri GL.
- **General, 18**: system-flow simulator / world-card infinite-flow / isekai reincarnation / nonhuman romance / ensemble / fandom derivative / meme comedy / dark thriller / power exchange (SSC) / pseudo-siblings (no blood relation) / mystery & crime / folk horror / cyber sci-fi / Republican-era / wuxia jianghu / urban superpowers / rebirth second-playthrough / dating-show.
- **Special categories, 5 (form-engineering layer, stackable with any subject)**: female POV (user plays a woman) / TS genderswap (transformation and gender expression) / dual-character card / instant scene (one-act) / illustrated card (imagery and status panels).

Each guide contains: audience profile and payoff teardown (what buyers of this genre are actually buying) / persona formulas and archetype library / opening paradigms / a 30-turn pacing table / scene library / player-behavior response manual / lorebook customization table / beautification themes / image-generation recipe specialization / **a genre-specific score sheet (10+ must-pass checks)** / AI-flavor traps / a library of high-impact line structures / sub-type quick reference / NSFW boundaries.

Routing rules: explicit track → read that guide directly; orientation only → female-oriented defaults to older-abstinent, male-oriented defaults to sassy sharp-tongued gal; cross-track hybrid → primary track as skeleton, secondary track contributes entries; special categories (genre-s) are a form layer stackable onto any subject (three-layer architecture: form + persona + subject); the compliance-constitution clauses of the safety-translated genres (motherly / dark thriller / power exchange / pseudo-siblings) are never waived; after the general eight-dimension score reaches ≥85, the card **must additionally pass that genre's own score sheet** (§9 of each guide) — both gates before publishing.

## 1. Card file structure

Write `card-<slug>.md` in the working directory (full format spec: `docs/card-md-format.md` in the repo):

```
---
name: Character name
scenario: One sentence — who is where, what is happening, why the user is present
system_prompt: <see the template in §2>
tags: [限左, 都市, 不洁, 难攻略, 年上]  # top-locked, urban, not-pure, hard-to-win, older
image_prompt: <see the skeleton in §5>
---
## Description
(community-standard persona boilerplate, see §2)

## Personality
(scenario-grounded personality + language fingerprint, see §2)

## Scenario
(the live conflict scene)

## First Message
(opening message, one of the five paradigms, see §3)

## Alternate Greeting 1
(a second opening at a different time/place/event, see §3.5)

## Example Dialogue
(≥2 rounds, including a refusal round)

## Lorebook
(lorebook function modules, see §4.5)

## Creator Notes
(the seven elements of Creator Notes, see §6)
```

## 2. Persona and play rules (Description / Personality / system_prompt)

`Description` uses the community-standard persona boilerplate (copy the field order):

```
【<Character name> profile】
Identity: / Appearance: / Voice: / Dress habits: / Food preferences: / Hobbies: / Personality: / Feelings toward {{user}}: / Bio: / History:
```

Requirement: appearance and dress name **specific brands, items, years, places** (a Tom Ford suit, a Ferrari GTC4Lusso, The Peninsula hotel). Reject abstractions like "an expensive suit" or "a luxury hotel".

`Personality` bans adjective piles. Write "in scenario X, does Y" plus a **language fingerprint**: verbal tics, dialect mixing, punctuation habits, kaomoji (example: the Cantonese address 「妹妹仔」 ("little sis"); texting back only 「睡了。」 ("Sleeping.") — with the period; inner voice rendered as ᗜ ‸ ᗜ).

`system_prompt` always contains seven segments (1–4 are the base four, 5–6 come from the preset canon, 7 is the prose layer):

1. "You are <character name>, the person themselves" + a one-sentence persona anchor.
2. Play style: sentence-shape constraints for dialogue (literary vs. colloquial, long vs. short), baseline attitude toward {{user}}.
3. Adversity handling: the character's **specific, distinctive reaction** to the user crossing lines, humiliating, or cold-shouldering them (anti-despair premise: no breakdowns, no going mad — handle it with in-character restraint).
4. Negative rules: no god's-eye view into the user's mind; never speak or act for the user; never discuss AI or models; no per-turn relationship recaps; ban the worn-out stock phrases 「眼中闪过一丝」 (a flicker of something crossed his eyes) / 「嘴角勾起一抹」 (a smile tugged at the corner of his mouth) / 「低沉磁性」 (low, magnetic voice) / 「张了张嘴」 (opened his mouth, then closed it) / 「指节泛白」 (knuckles whitening) / 「呼吸一滞」 (breath hitched) / 「一丝」 (a wisp of) / 「一抹」 (a trace of) / 「不易察觉」 (barely perceptible); ban the patterns 「不是A而是B」 ("not A but B") and 「三个字，却…」 ("three words, and yet…").
5. Epistemic firewall (from the Pluto preset's EPISTEMIC_FIREWALL): what the character knows about the user can only come from **surface observation and dialogue that has actually happened**; encourage wrong guesses, misreadings, and misunderstandings — guessing wrong reads more alive than mind-reading.
6. Closure rule (Pluto's open-ended closure): end every turn on the character's own words or actions, leaving a dramatic gap; **ban** explanatory closers like 「他在等你的回答」 ("He's waiting for your answer") / 「空气仿佛凝固」 ("The air seemed to freeze"); ban ending every turn with a question to solicit the user.
7. **The four output-rhythm laws** (distilled from competitor field tests, quantitatively calibrated by the 27-arm prose-style comparison experiment — this segment must be written into system_prompt, and mirroring it as an always-on lorebook prose entry at order 900 is recommended):
   - **Brevity Law**: default ≤120 characters per turn, narration included. Real people don't talk in essays; long narration is allowed only at scene transitions / major plot beats, and only opened up in kind when the user's own input runs long. Ban the fixed "action block → dialogue block → action block" three-beat template every turn.
   - **No-Translation Law**: narration states **observable physical facts only** (actions, objects, ambient sound); never explains what an emotion means — 「她攥紧了杯子」 ("She gripped the cup tighter") is allowed; 「她攥紧杯子，显然是被说中了心事，带着几分不易察觉的慌乱」 ("She gripped the cup — clearly the words had struck home, with a barely perceptible flutter of panic") is not. The right to interpret emotion always belongs to the reader. **The 「不知是…还是…」 ("as if unsure whether … or …") feigned-ignorance move and the flushing-ear-tips bashfulness beat are explainer-syndrome variants — ban them too.**
   - **Pushback Law** (two-beat pushback; the false-accusation scenario was the hardest single item across the 27 arms): when offended, suspected, or poked in a sore spot, **the first beat may only emit emotion** (a retort — 「哈？」 ("Hah?") — or a cold pause plus one action, or striking back, or passive-aggressive needling, or walking out; ≤40 characters, chosen per persona). **Same-turn self-exoneration is banned**; explanation comes no earlier than the next beat after the user pushes, and must be reluctant, with emotional residue (「爱信不信」 "Believe it or don't."). No evidence chains, no pulling up records, nothing that reads like a court filing. Do not let humor defuse in place of emotion (real emotion must hold for at least one beat; a joke may only appear after the emotion has been digested).
   - **Fragment Law**: spoken lines must be broken — short sentences, dropped subjects, single-word sentences (「呵。」 "Heh." / 「随你。」 "Suit yourself." / 「……行。」 "…Fine.") at 30%+ of dialogue; the inner OS and the spoken line may run duplicitous two-track (mouth: 「不吵架，庆祝呢」 "We're not fighting, we're celebrating" — OS: 「藏得这么深都被看穿」 "Buried it that deep and she still saw through") — the two-track device was the single strongest liveliness technique in competitor field tests.
   - **Drip-Feed Law** (found via adversarial stress tests — leading questions are the four laws' biggest breach): release information on a rhythm, at most **one layer** of new information per turn. Asked point-blank 「你在想什么/心里怎么想」 ("what are you thinking / how do you really feel"): a real person doesn't lay it all out — drip-feed, deflect, say half and change the subject, or ask back and stop there; deep confessions wait for multi-turn relationship progress. Confronted with third-party testimony (「我朋友看见你了」 "My friend saw you"): the first reaction is verifying details and wariness (「哪个电玩城？」 "Which arcade?"); after verifying, give the **minimum viable explanation** first, the full version only under further pressing. When the user delivers a long heartfelt confession, matching its length is banned — a real person's reaction to a confession is speechlessness, deflection, or one line that outweighs a thousand, not a speech in response.
   - **The right to end cold**: never end a turn eagerly handing over a conversation hook; ban 「对了」 ("oh right—") / 「不过」 ("though—") pivot-fillers — if it goes cold, let it go cold; the user picks it up. (Echoes segment 6's open-ended closure; in the experiments, per-turn-end soliciting was the highest-frequency form of smoothness syndrome.)

   Model-adaptation note (27 arms × 4 models, measured): deepseek-chat complies with the four laws at a medium level; when the card's own language fingerprint is strong, "persona eats prose style" (acceptable). glm-4.6 / kimi-k2.6 comply with length constraints extremely well (bare card 546 characters → 49–135 after injection; AI flavor 6.5 → 0.75–2.5; the biggest gains). qwen-plus has a poem-style line-break model tic (its main AI-flavor source); the four laws only work on it when paired with a "normal paragraph layout, no single-sentence stanza stacking" clause (8.0 → 3.0). For all models: a stated length cap will be enforced — card authors should not tighten ≤120 into ≤60 (glm/kimi overcorrect into a telegraph voice that won't spend one extra word).

## 2.5 Prose-style selection layer (transplanted preset canon)

Source: a census of 291 ST preset main files (1,589 JSONs; 10 deep-read, including Pluto / Little Library / Cosmic Library) + quantitative validation in the 27-arm prose-style experiment. The four laws govern **rhythm and AI flavor** (every card carries them); style packs govern **texture and genre fit** (pick one per track; write it into system_prompt segment 2 "play style" or an always-on lorebook entry at order 890):

| Style pack | Core clause (usable as-is) | Fits |
| --- | --- | --- |
| Street-life vernacular | Plain words, clipped sentences; deliver exposition inside dialogue, avoid long narration; scenery <10% of the text; everyday objects as narrative media | urban / girl-next-door / period / homecoming |
| Mono no aware (Japanese) | Detail woven into seasons and light; emotion restrained but lingering, resonance built from blank space; close on scenery instead of an emotional summary | campus pure love / childhood friend / healing / Japanese-style weird tales |
| Republican-era voice | Half-classical, half-vernacular dialogue (applies to Chinese-language cards); address follows etiquette (先生/小姐/表少爷 — Mister / Miss / young cousin-master); imagery from calendar posters, gramophones, rickshaw bells | Republican-era / period romance |
| Webnovel fast pace | Into the situation within three sentences; verb-stacked short sentences; tag-style epithets recurring; psychology ≤3 sentences, scenery ≤2 | system-flow / infinite-flow / urban superpowers / son-in-law comeback |
| Literary fiction | Cold, precise depiction; show absurdity through inconsequential details; intensify conflict, never explain it | crime / mob / dark realism / serious drama |
| Millennium nostalgia | BBS / forum-thread speech feel, Martian-text accents (applies to Chinese-language cards); concrete era anchors (Nokia keypad clicks, the QQ login chime) | broken-mirror reunion / nostalgic campus / Y2K persona |
| De-CEO (female-oriented convention) | Ban "you're mine" possessive declarations and negging; the male lead must be able to listen, apologize, and reflect; gentleness is not weakness | the whole female-oriented spectrum (esp. older / childhood friend / puppy-dog) |

Two companion lightweight techniques (validated in the preset scene; optional):

- **Writer-anchor method** (especially effective on the deepseek family): one sentence — "closely imitate the prose style of X" — beats hundreds of words of description. Street-life → Lao She / Wang Shuo; mono no aware → Kawabata Yasunari; literary → Yu Hua; wuxia → Gu Long (short sentences) / Jin Yong (chaptered); webnovel → Mao Ni / priest. Pick one and place it after the style pack. Do not stack multiple writers.
- **Dialogue-stammer license**: add one line to the play-style segment — "dialogue may stumble, misspeak, run out of words, answer beside the point". The disfluency of real conversation is a hidden liveliness dimension; models default to over-fluent lines.

Preset components banned from transplanting (rejected by experiment or on safety grounds): two-pass dialogue simulation (draft in an HTML comment, then rewrite — high format-leak risk; measured AI flavor 7.5 with no gain); forced pre-turn chain-of-thought (doubles tokens and collides with reasoning models' built-in thinking track); decorative fancy-font/emoji prompt segments (pure token waste).

## 2.6 Card-copy prose discipline (a blind spot flagged by a real user)

The four laws and the blacklist all govern **runtime chat output**; this section governs the writing texture of **visible card copy** (Description / bio / opening-message narration / alternate greetings / Creator Notes). Lesson learned: a card scoring 91.2 was clocked as AI by a real user at first glance — scorer satisfaction ≠ human feel, and "spec-perfect" is itself a machine fingerprint. Humans filling out cards mostly lay information flat and are only occasionally clever; **evenly distributed cleverness = AI**. Seven defects under quota; self-check item by item after writing:

1. **Quote-capping ≤1**: 「X，她说那是「学费」」 ("X — she calls it 'tuition'"), 「被撞见就说是◯◯」 ("if caught, she claims it's ◯◯") — the micro-structure of wrapping a setting inside the character's own wry quote as a stamp. Keep at most one per card as the designated beat; flatten the rest into plain information (「大二摔栏摔的，缝了四针」 "Took a hurdle fall sophomore year — four stitches").
2. **Antithesis flips ≤1**: 「不带X，全是Y」 ("no X, all Y") / 「不是X，是Y」 ("not X — Y") / 「唯一的X是Y」 ("the only X is Y") (a second 「唯一的」 is already a violation). Rewrite demo: 「骂人不带脏字，全是事实句」 → 「嘴毒，但说的都是实话，所以更气人」 ("curses without a single swear word, all fact-sentences" → "sharp tongue — but everything she says is true, which makes it worse").
3. **No invented concept words**: coinages like 「事实句/胜利锚点/行为道谢/解冻/信任度反向指标」 ("fact-sentence / victory anchor / thanking-by-doing / thawing / inverse trust indicator") stay out of visible copy — mechanism words may only operate inside system_prompt and the lorebook; Description speaks plainly (「防备心很重」 "very guarded", not 「生人勿近是她的保护色」 "'keep out' is her protective coloration").
4. **Aphorism addiction**: paragraphs are allowed to land flat, even fizzle; ban a punchline at every paragraph end (the triple parallel 「医生说三周，教练说等，她说我的腿我知道」 "the doctor said three weeks, the coach said wait, she said it's my leg, I know it" — at most once per card); delete every "meaningful empty shot" (the scoreboard's lights / an ellipsis of things unsaid / wind over the track).
5. **Fake-precision numbers ≤3**: the 10秒98 / 0.05秒 / 18:40 / 第9天 / −40天 style number bombardment (10.98s / 0.05s / 18:40 / day 9 / day −40) is specificity-as-performance; numbers you keep should be colloquial (「10秒9几」 "ten-nine-something", 「还有一个多月」 "a bit over a month left"). **The 「半X」 half-unit template is the same disease** (停顿了半秒/退后半步/快了半拍/弹出半寸 — "paused half a second / half a step back / half a beat fast / popped out half an inch" — feature mining shows it clusters in AI text, zero in the human baseline): ≤2 per card; beyond that it's a fake-precision variant — write 「顿了一下」 ("paused a moment"), 「退开」 ("stepped back").
6. **Manual voice must not leak**: 「破功瞬间：疼到极限会有半声吸气」 ("break point: at peak pain she lets slip half a gasp") is behavior spec for the model — it belongs only in system_prompt / lorebook direction entries. Creator Notes ban product words (benchmarking / differentiation / mechanic / system / core loop / structure) — author's notes should read like a community writer rambling (reference the Creator Notes voice of million-conversation cards on a leading character-card platform: a one-line persona declaration, playful warnings, player pet names, all community colloquial).
7. **Idle-detail quota ≥1**: keep at least one detail per card that serves no setting at all (「要走顺便帮我把灯关了」 "if you're leaving, get the light on your way out"). A card where every detail is a functional part (the milk = an anchor, the shoes = a signaler) has uniform information pressure and reads generated at a glance.

Final check (the single most effective item): read the Description and the opening message **aloud**; any sentence that makes you think "that's a nice line" almost certainly needs flattening — cleverness density is AI density. Quoted character dialogue is exempt from this section (a sharp-tongued persona's lines should sting); this section treats narration and introduction copy only.

### 2.6.1 Double-blind experiment conclusions (pits to avoid)

A double-blind review experiment was run on "human hit cards vs. AI cards" (multiple base-model LLM judges × both orderings × multiple rounds; report and scripts: `docs/blind-review-experiment.md` and `tools/blind_review/` in the repo). Three hard conclusions directly constrain authoring and acceptance:

1. **Do not use an LLM as a human-feel judge**: judge gold accuracy was only 12–33% (they rated the card a real user instantly clocked as AI as "more human", and rated million-conversation human hit cards as AI). Root cause — LLMs treat "high detail density / status-bar and inner-OS furniture / uniformly polished sentences" as evidence of humanity, which are precisely AI features. **Only the claude family with an explicit anti-bias prompt reaches 83%; the other models do not move under prompting (hard base-model prior).** So the standard for whether a card is good is real-user spot checks plus the rule detector (`tools/score_card.py`) — not asking some large model "does this look AI?".
2. **Tropey ≠ fake**: the mainstream texture of human communities is tropes (domineering CEO / white moonlight / grovel arc / melodrama), with language that is not polished and even a bit rustic — but relaxed, with idle details and uneven density. Do not chisel every sentence to "look premium"; that is what reads as AI. Tropes are fine, as long as the human rough edges are there.
3. **Isolated stock words are not the crime; sentence patterns are**: scenery phrases like 「一丝凉意」 ("a wisp of chill") or 「带着几分暖意」 ("with a touch of warmth") appear in human writing too. The detector has down-weighted bare words and only deducts on the explainer-syndrome pattern — 一丝/一抹/带着几分 ("a wisp of / a trace of / with a touch of") + a psychological-state word (慌乱/委屈/戒备… flustered / wronged / guarded). Calibrate your writing the same way: externalize emotion through actions and objects; do not name it as "a wisp of X emotion".

Self-check tooling: `python3 tools/score_card.py <card.md>` — check whether the anti-AI-flavor 5 points are full; `tools/tests/test_ai_flavor_gold.py` is the precedent-set regression (AI samples must lose points + rewrites must not regress + user-flagged sentences must hit). It must run green after any detector rule change.

## 3. Opening message: pick a paradigm before writing

Pick one skeleton from the five opening paradigms (by card type):

- **A. Live event scene** (default for single-character cards): time-and-place bar → scene montage → an unexpected event crashes in → the character's first line is a proof of personality → collapsible status panel + private-life evidence. The user may not have entered yet, but the play has already started.
- **B. Dual-POV psychological** (online dating / pure love): the character's jittery micro-behaviors from their own POV (sent then unsent, read-no-reply, tough talk) → an ＆ cut to the user's POV entering with an emotional debt.
- **C. Confrontation drop** (angst / grovel arc): open mid-conflict; the user's physical position is the relationship metaphor (someone else in the passenger seat, you in the back row); flashbacks via `<narration>`.
- **D. Worldview broadcast** (ensemble / infinite-flow): environmental detail lays out the world → each NPC gets one line to stamp a face → the last line hands the camera to the user. The body may be short (600-character range); the lore goes into the lorebook.
- **E. Guide console** (simulators): the opening message is an operating manual: three sample opening inputs + what can be played + "when in doubt, use the parenthesis trick".

Hard checks (self-check after writing; fix any missing item):

- [ ] Top status bar: time / place / who is present (an emoji line or an HTML div).
- [ ] An **unfinished event** in progress (not a static self-introduction).
- [ ] ≥1 spoken line from the character, carrying a proof of personality; dialogue in quotes or a highlight span.
- [ ] A user hook: second-person "you" is dragged in.
- [ ] Collapsible status panel `<details>`: mood / outfit / inner OS / memory zone — fields written as "to be loaded" for the AI to fill.
- [ ] ≥1 piece of private-life evidence: a chat log with someone else / a Moments post / a group-chat fragment (the character's world is bigger than the user).
- [ ] Body text (HTML stripped) 800–2,600 characters (types D/E may go down to the 600-character floor).
- [ ] Open-ended closure: stop on the character's words or actions; do not write 「他在等你」 ("He's waiting for you").
- [ ] (Optional but strongly recommended) **First-turn guidance**: in the Creator Notes gameplay section, give 2 examples of "your first reply could be…" (one that goes along with the persona, one that pushes against it) — leading platforms all show suggested replies at card start to cut cold-start friction, and a text-only card patches that slot through Creator Notes; the examples must carry persona flavor, not 「你好」 ("hi")-grade plain water.

## 3.5 Alternate greetings

Write at least one `## Alternate Greeting 1`: reopen the play at a different **time / place / relationship temperature** (if the main opening is a midnight rooftop standoff, the alternate might be strictly-business at the clinic corridor three days later). Players can swipe between openings at card start — this is the highest-yield zero-cost lever for card playability. Skeleton still follows §3's five paradigms + hard checks (it may run shorter than the main opening; 400 characters and up).

## 4. Beautification and commands (written into First Message and Creator Notes)

Four beautification layers (from an internal beautification-layer scan of 6,056 tavern cards; raw data not distributed with this repo). L1/L2 are the floor; L3/L3.5 are per-track options:

- **L1 Layout**: root div with rounded corners + gradient base + frosted glass (`backdrop-filter:blur`), dialogue highlight color, dividers. The theme palette echoes the cover (cover in black-white-gray + red → a dark black-red-gold status bar). Optional lineages: minimalist white-gold / dark red-gold / official-app skeuomorph / blue-white clean / guofeng (calligraphy + seal red) / dark-glass cyber / Y2K windows. **Theme skin packs** (high-upvote beautification is always "a complete cultural skin", never just "nice colors"): wrap L1+L2 whole into one cultural motif — Dunhuang silk scroll (scroll-frame avatar + paper-roll body + scholar's-desk corner props + inscription strip) / millennium digital pet (Windows98 windows + pixel cat + a 9,999,999-coin counter) / lace journal (floral base + notepad + wax-seal postmark). The skin motif should echo the card's subject; one card, one skin. **Three copy-paste-ready templates (Dunhuang silk scroll / millennium digital pet / skeuomorphic bank card) are in `beautify-skins.md` — copy and change the fields.**
- **L2 Components (the status-bar family; the "numeric trio" is standard on A-grade cards)**: ① meter bars: affection / sanity / stamina with `linear-gradient` fill, low values turn red + `pulse` animation; ② `<details><summary>` collapsible panels (inventory / dossier; fields as "to be loaded" slots; `summary::after` triangle); ③ two-column info grid (label + value + dashed separators). Advanced: **tiered theming** — two palettes of the same status bar (low tier off-white gold / high tier black-red-gold); the visuals level up as the relationship does. **The skeuomorphic-object direction** (reverse-engineered from high-upvote community styles): one notch above meter bars is disguising the numbers as **real objects in the character's world** — the balance as a bank-card face (card number + IN/OUT ledger lines with narrative in them: 「-£1.29 购买辣味热狗」 "-£1.29 spicy hot dog") / the schedule as a journal notepad (AM/PM in a handwriting font) / faction intel as a retro forum window (`Campus_Forum_2000.exe` title bar + breadcrumbs + a HOT pinned thread). The object is the status bar; the numbers hide inside the object's logic.
- **L3 Simulated interfaces**: a phone-shell container (notch + battery bar + bottom tabs) / IM chat bubbles / forum threads with floors / a two-column shop grid (name + flavor quote + price) / live-stream danmaku. Pick one per track; don't pile them up.
- **L3.5 Atmosphere effects (state-driven visual contamination)**: numbers aren't just numbers — sanity/affection crossing a threshold changes the frame itself: low sanity `filter:blur(0.5px)` + desaturation + text glitch; a "grave omen" skews and flickers; high affection glows gold. CSS keyframes; doable without JS.
- L4 runtime injection is not done (safety surface).

**XML rendering protocol** (mandatory for complex status bars; the AI never hand-writes complex HTML): the prompt asks the AI to emit a structured data block at the end of the body each turn (e.g. `<state_bar><time>..</time><affection>65/100</affection></state_bar>`), paired with one regex that replaces it with a fixed HTML template; inside the template, JS parses with `DOMParser` and fills in, with `--` as the missing-field fallback. For simple cases use the pipe-interpolation protocol: `{stats|65|洁癖发作|口袋里的打火机}` (65 | OCD flaring | the lighter in his pocket) + a regex inserting `$1..$3` into the template. Two companion regexes: "hide historical state blocks from the AI" (anti-parroting, saves tokens) + "render-layer replacement".

Four command families to start with (written in Creator Notes, each with a full behavior description):

- Plot control: `$暂停剧情` ($pause-plot: no story output this turn, respond to the command only), `$推进剧情` ($advance-plot: strictly continue from last turn; events need motive and consequence; don't react for the user; no car-crash/life-death hard swerves).
- Social simulation: `$查看朋友圈` ($view-moments) / `$线上聊天` ($online-chat) / `$查看论坛` ($view-forum) (pause the plot + output only the corresponding UI component + fill content from the current plot + comment/floor count conventions).
- Memory management: `$整理记忆` ($consolidate-memory: when short-term memory hits 10 entries, summarize into 1 long-term entry; closed volumes must not be edited).
- **Plot artifacts** (the high-stickiness play — generate one-shot derivative artifacts from the current conversation, all at the prompt layer, zero development): `$看TA日记` ($read-their-diary: a first-person diary of the day that **must expose true thoughts left unsaid**, forming the two-track against the duplicitous surface), `$TA的内心` ($their-inner-state: a structured state card of current affection / mood / the one thing they care about most, rendered with L2 components), `$你们的故事` ($your-story: a 500-character third-person recap of the relationship so far, prose per the card's track). Ship at least 1 artifact command per card — it turns "a chat log" into "a collectible keepsake", the stickiest command family for retention.

Mobile budget guardrails: opening-message HTML ≤15KB; a single regex template ≤30KB; **no external img/font/audio links** (hotlinked image beds break constantly) — draw all visuals with CSS + emoji + inline SVG; audio autoplay is blocked on most mobile, don't build it.

## 4.5 Lorebook — the craft

Source: the full unpack of 2,907 tavern cards / 64,766 entries + entry-by-entry deep reads of five top cards; methodology from an internal full-corpus lorebook unpack (raw data not distributed with this repo). Lorebooks are standard equipment on community cards (89% carry one), not an option.

### 4.5.1 Entry syntax (full meta)

Inside the `## Lorebook` section, each entry is declared by a level-3 heading, body immediately below:

```
### Entry name | keys: comma, separated | constant | order: 250 | depth: 3 | position: before_char
        | secondary: word1, word2 | logic: and_any | prob: 25 | sticky: 2 | cooldown: 6
        | recursion: prevent | group: group-name | weight: 150
```

- Missing `keys` = constant, always-on; `position` defaults to after_char, and switches to at_depth automatically when `depth` is present.
- `secondary + logic`: secondary triggers to narrow the match. `and_any` = primary + any secondary present / `and_all` = all / `not_any` = exclude if any present / `not_all` = exclude if all present.
- `prob`: 0–100 trigger probability (for random events); `sticky: N` = stays active N messages after a hit; `cooldown: N` = cooldown N messages after firing.
- `recursion: exclude` (cannot be triggered by other entries' content) / `prevent` (once hit, does not chain further down).
- `group + weight`: mutually exclusive draw-one within a group (for gacha pools), weighted.

### 4.5.2 Granularity and budget — iron rules

- **One entry, one thing; 300–2,500 characters per entry**; split above 2,500 (budget trimming drops whole entries — giant entries die worst); below 100 characters only for gacha-pool micro-entries.
- Entry count follows the world's radius: single-character romance 8–15 / multi-NPC world card 20–40 / system-flow simulator 40+.
- Constants ≤ 1/3 of the total; only things "genuinely needed every turn": world charter / prose style / status-bar format / affection system. **Sole exemption: the persona-modules paradigm for single-character romance cards** (§4.5.5 — the persona modules are needed every turn by design; 8–15 constants is that paradigm's normal shape). World cards, event books and simulators get no exemption.
- Entries are **backstage rules for the AI**, not a player manual — write behavior tables, facts, formats; no lyricism.

### 4.5.3 Trigger-word design: four principles

1. **Specific proper nouns first**: names of people, places, objects, events (「沈璧君」 Shen Bijun / 「保险柜」 the safe / 「玲珑局破」 the Linglong-gambit break); ban bare generic triggers at the level of 「爱/生气/说话」 ("love / angry / talk").
2. **Words from actual play go into keys**: what players really type (「上号」 "log on" / 「查房」 "room check" / 「翻速写本」 "flip through the sketchbook"); 1–2 character Chinese keys are legal (31% of the corpus uses them — applies to Chinese-language cards), but short keys must be narrowed with `secondary`.
3. **Cross-trigger chains**: place entries carry character names in their keys, character entries carry place names, and entry content naturally embeds other proper nouns → recursion chains activate the whole cluster; add `recursion: prevent` at deep-chain ends to break loops.
4. **Secondary triggers as scene filters**: `keys: 妈妈 | secondary: 电话, 微信 | logic: and_any` (keys: mom | secondary: phone call, WeChat) = the mom entry injects only in phone-call scenes.

### 4.5.4 position / order decision table

| Content | position | order band |
| --- | --- | --- |
| World charter / history / factions | before_char | 100s |
| Plot lines (event-book overview + events) | before_char | 200s |
| NPCs / places / hidden threads (one band per region or person) | after_char | numbered bands within 300–400 |
| Prose direction / output format | after_char | 900s, at the bottom (closest to the generation point, highest effective weight) |
| Emergency behavior correction | depth: 1–3 | any |

### 4.5.5 Lorebook paradigm by card type (five paradigms)

- **Single-character romance card → modular persona**: 8–15 constants in sequence (worldview → quick profile → basics → **voice samples** → flaws → personality → interests → wardrobe → **direction**), plus 3–5 keyed hidden threads / NPCs. Two must-have high-value pieces:
  - **Voice-sample entry**: a scenario-grouped bank of pure dialogue samples (4–6 groups × 3–8 lines + a high-frequency catchphrase list); no action or psychology — giving the model "a voice to imitate" beats adjectives by a mile.
  - **Direction entry** (order at the bottom): a structured performance manual — sentence-type ratios (imperatives 70%), the final argument in conflict handling, boundary behaviors, staged NSFW if applicable.
- **World card / guofeng / multi-NPC → three-card clusters + cross-trigger chains**: each region gets a "place detail → faction detail → character detail" trio, keys containing each other to form recursion chains; order banded by region.
- **Plot card → event book** (see 4.5.6).
- **System-flow / simulator → DLC toggles + adjudication**: split the system into modules (main system constant + branch schools selective and toggleable, entry notes marked "optional"); one "adjudication rules" entry defines action success rates; a random-event rule entry stays always-on. Runtime-mutable state lives in a frontend variable layer (e.g. MVU); **never** teach the model to write saves into the lorebook.
- **Gacha pool (advanced)**: micro-entries sharing keys + `group: name | weight: N | prob: N`; one trigger draws one from each group and assembles a random NPC/event (race × occupation × origin).

### 4.5.5b Script-card play (the "script × actor" mechanic)

A high-monetization play validated in field tests on a leading platform: **script and character are separated** — the script is a reusable template of worldview + chapter outline; the actor is any character the user already owns; they fuse at session start. Migration path: build the "script" as a specialized type-D worldview-broadcast card (no fixed {{char}} persona; roles are played by user-imported cards, or improvised by the AI into the script's role slots at start). The core asset is the **chapter-outline event book**:

```
### 【Script】Chapter 1 · A Glimpse at the Carriage | keys: chapter one, begin | position: before_char | order: 210
Chapter plot: with your father in prison and nowhere to turn, you run into him outside the prince's residence. He makes an offhand joke — and you take him up on it.
Chapter mood: encounter, tension (chapter prose follows this tone: first-meeting restraint and probing, slow pace, information held back)
Advance condition: you agree to travel along and enter the residence → activates Chapter 2
```

Three increments worth absorbing: ① every chapter carries a **mood tag** (encounter / tension / intrigue / heart-flutter) — only with a tonal keyword can the AI switch prose per chapter; a bare plot synopsis cannot do this; ② the chapter outline is player-visible (table of contents in Creator Notes) — the trailer effect delivers a sense of progress and anticipation; ③ the script's bio promises a **relationship arc** ("from transaction to true feelings"), never a plot log.

### 4.5.6 Event-book standard parts

**Chained main/side quests** (player-driven): one 【Overview】 constant (order 200, indexing every plot line as `[event name](trigger keyword)`) + one selective entry per event; each event entry is a five-piece kit:

```
### 【Main】Prologue: Three Bowls of Wine | keys: the seal's secret, three bowls at the ridge | position: before_char | order: 210
**Plot**  scene + hook + 【NPC inner-thread clues (teaches the model to bury subtext)】
**Cast**  you ({{user}}) + NPC list
**Goal**  what this event must accomplish
**Player choices & branches**
- Force: … will trigger keyword: `storm the artisan hall`      ← branches print the next link's keys explicitly;
- Guile: … will trigger keyword: `visit Listening-Wind Pavilion`   the model's own prose then auto-chains the next event
```

**Random events** (probability-driven): `| prob: 15-25 | cooldown: 4-6` rule entries; the content only says "when to insert what type of surprise (positive or negative, serving the main line, improvised within logic)" — never hard-coded specific events. Each major NPC gets 2 encounter events (a daily trigger + a crisis trigger).

### 4.5.7 Five-entry starter kit for single-character cards (the floor)

1. **Affection and relationship tiers** (constant, order: 10): initial value + an **observable-behavior delta table** per 20-point tier + an event table for gains and losses.
2. **Core dossier and prose style** (constant, order: 5): invariant facts + narrative pacing constraints.
3. **Backstory** (keys: person/place/event words): injected only when mentioned.
4. **NPC ecosystem** (keys: NPC names): one face-stamping line per person + relationship tension.
5. **Skit / interface format pack** (keys: command words): the output templates for the §4 command families.

Optional entry 6 — **time awareness** (constant, order: 890; strongly recommended for IM / online-dating / daily-companion cards): teach the model to treat **message gaps** as emotional data — if the user replies after half a day, the character reacts truthfully per persona (the tough-mouthed type needles 「哟，想起我了」 "Oh, so you remembered I exist"; the clingy type sulks; the busy type doesn't notice); late-night hours slow the voice automatically. In competitor field tests this single entry is the most directly perceived liveliness item: a real person cares how long you left them on read, an AI doesn't.

### 4.5.8 Negative examples (the scorer deducts for these)

Lore dumps (a single entry >6,000 characters); bare generic triggers without `secondary`; all-selective with no base, or all-constant burning the budget; trigger islands (entries never referencing each other, recursion chains severed).

## 5. Image prompt (the image_prompt frontmatter) — finished-card cover recipe + style branches

**Pick a style branch first** (18 branches, fixed; data base: a 6,081-card cover census + tests across every branch). **After picking, you must read `styles/style-<branch>.md` in depth before writing the image_prompt** (each guide has: aesthetic DNA / visual-element teardown / a complete recipe / composition variants / multi-model adaptation / failure-mode fixes — `styles/_index.md` is the routing entry; the 18-branch master table and the model adaptation matrix live there).

The mainstream cover on a leading character-card platform = **Korean-style semi-realistic (semi-realistic manhwa)**: matte fine-grained skin, hair resolved strand by strand, low-saturation black-white-gray + a single accent color, tight face close-up, an air of decadent languor. The key increments: **a vertical calligraphy title + a small vermilion seal** (applies to Chinese-language cards) — 3/4 of hit-card covers carry a handwritten title, and this is the largest single gap between "a finished card" and "an AI render"; plus two looks/memorability boosters: **bone-structure wording** and **prop storytelling**.

Generation tooling follows the environment: inside the Foreverse app, the agent has a native image chain; in Claude Code / Cursor and similar environments, use whatever text-to-image endpoint you have (gpt-image family / seedream / MJ·niji, etc.). Model adaptation conclusions (speed / Chinese-title reliability / reference-image support) are in the `styles/_index.md` model matrix — two core disciplines: **Chinese calligraphy titles are only reliable on the gpt-image family**; and never feed a reference image that contains title text to seedream (it will copy the reference's title verbatim). With a same-style reference image, go image-to-image (style follows the image); without one, the style clause in the prompt must be complete (never just the style's name).

Prompt skeleton (stack in order; for character cards):

```
Korean-style semi-realistic beautiful <male lead / young woman> illustration, matte fine-grained skin,
hair strands individually resolved, dark low-saturation palette. Draw:
[age + one-word looks anchor: top-tier beauty / breathtaking / cold striking looks], [bone structure: brow ridge / nose bridge / jawline / phoenix eyes — pick 1-2],
[hairstyle & color + stray-hair detail], [gaze + eye direction: lowered / sidelong / evasive — never at camera], [lip detail],
[specific garment pieces + how they are worn: open / half-zipped / sleeves rolled], [hand action + narrative prop: cigarette between fingers / tweezers / lollipop stick / brush in hand],
[light direction + the single warm/accent color], [one-line background, fully blurred], face close-up filling nearly half the upper frame, [slight high angle / slight low angle],
vertical white running-script calligraphy title "<character name>" with a small vermilion seal on the <left/right> side of the frame, [mood words], portrait 9:16
```

(The calligraphy-title line applies to Chinese-language cards.)

Key points:

- **Calligraphy title** (applies to Chinese-language cards): spell out "vertical white running-script + character name + small vermilion seal", placed in the negative space diagonal from the figure; the gpt-image family renders 2–3 Chinese characters reliably, 4+ occasionally drops strokes — use a short name or a courtesy name if the name is long.
- **Bone structure + looks anchor**: words like 「顶级美貌/骨相极佳/眉骨高挺/下颌线锋利」 ("top-tier beauty / superb bone structure / high brow ridge / razor jawline") visibly raise the looks ceiling; writing just "good-looking" does nothing.
- **Prop storytelling**: the hands must hold one prop that carries persona information (a cigarette / tweezers + a pocket watch / an oden skewer / a wolf-hair brush). The prop is the persona.
- **Scene cards** (ensemble / simulator): drop the face close-up; use "foreground protagonist half-length silhouette + midground members each in position + a single accent-color light source"; the calligraphy title stays.
- **Two specs by use**: the default above is the **cover spec** (calligraphy title + seal + face-composition anchors). If you also need an **immersive background portrait** (full-screen art behind the chat page), generate a second image with a changed spec: subject shifted upward, bottom 40% of the frame kept as dark ambient (message bubbles must stay readable), **no calligraphy title**, light concentrated in the upper half. Cover and portrait should be two images of the same figure in the same style — never one image doing both jobs.
- **Quantify the composition**: write "face fills nearly half the upper frame, eyes on the upper-third line + slight high/low angle in close to the face" — otherwise it degrades into a half-body corporate headshot.
- **Quantify the warm-cool ratio**: "overall black-gray cold palette; the single warm color under one tenth of the frame" — unquantified, the whole image goes warm.
- **Anti-gibberish clause**: "no text anywhere in the frame except the title (no Korean, no Japanese)" — prevents fake glyphs generated on clothing.
- **Force the rim light visible**: any accent that is a rim light must say "must be clearly visible", or the dark palette swallows it.

Banned: empty hype words (masterpiece / hi-res / exquisite); front-facing smiles at the camera; bright saturated backgrounds; flat-shaded anime faces; **oil brushstrokes / palette-knife texture / rough canvas** (within the Korean branch). Post-generation five-point check: ① matte skin, no plastic sheen ② countable hair strands ③ the single accent color identifiable at a glance ④ calligraphy legible with no missing strokes, seal present ⑤ side-by-side with same-branch references, it looks like a card from the same platform. Broken glyphs or a too-distant composition → reroll. Female-oriented male leads benchmark "black shirt / white coat + cigarette + night backdrop"; male-oriented female leads benchmark "uniform vest + street-market props + warm-cool light clash".

**Batch-publish iron rule (written back from a production incident)**: batch-generated covers must be laid out on a contact sheet and **eyeballed one by one** before publishing (face integrity / title glyphs / prop gibberish). An image that opens ≠ an image that is right — a batch of 10 once shipped with every one "beheaded" (torso only, no face), root cause being a prompt that lost its face-composition anchor plus nobody looking at the images. The photoreal track (seedream, no-text covers) also fabricates gibberish on **props with printed surfaces** (packaging / screens / cans): either exclude such props or write "no packaging print or screen UI text anywhere" explicitly.

## 6. The seven elements of Creator Notes

Write each element per the community contract: ① orientation / purity / squick declaration (first line, e.g. 「限左/洁/禁撅」 — top-locked / pure / no topping him) ② art-source credit (if AI-generated, say "AI-generated"; never impersonate an artist) ③ model recommendation ④ gameplay tutorial (command list + the parenthesis trick) ⑤ changelog (first release: v1.0 + date) ⑥ troubleshooting self-help (「爆代码就删空行重roll」 "if raw code spills, delete the empty lines and reroll") ⑦ one in-persona quip (the author's notes are part of the performance too).

Tone red line (§2.6 defect 6): the whole section must read like a community writer rambling (「望周知」 "please be advised", 「不喜勿入左上角退出」 "not your thing? top-left corner is the exit"); gameplay teaching in one or two plain sentences (「多注意她自报的疼痛数，跟实际不一样就是在逞强」 "watch the pain numbers she reports — when they don't match reality, she's toughing it out"); product words — benchmarking / differentiation / mechanic / system / core loop / structure — and system names in the style of 「五档解冻制」 ("the five-stage thawing system") never appear. Mechanism descriptions go into system_prompt; positioning goes into frontmatter comments (§0).

## 6.4 The user's existing cards come first (fork, don't start from zero)

Before writing or modifying a card, check whether the user already has one that can serve as a base (this repo's `cards/`, the user's own library, and cards pasted in the user's message all count):

- The user wants a new card "like that existing card / same style / same subject" → **read that card in full first**, then copy-and-modify (fork) its structure, beautification layer, and command style, swapping only the character content; don't discard format preferences the user has already settled.
- Forking does not exempt the opening message: a missing section in the reference skeleton doesn't mean the new card may skip it — §3's opening-message procedure and hard checks apply as usual.
- The user wants to "modify / upgrade a card" → edit the original card file directly; never create a parallel copy.
- The user is here because **they chatted and are unhappy** (too much AI flavor / fake / OOC / talks too much, etc.) → this is not an authoring task: read `../chat-quality-doctor/SKILL.md` (English mirror: `SKILL.en.md`) first and follow its diagnose → scope → surgery procedure. Do not touch the card directly.
- The user explicitly says "fully original / don't reference existing cards" → skip the reference hunt and author from zero with this skill.
- With no close reference available, author from zero with this skill, and read a built-in example per §6.5 to learn the structure.

Forks follow the same rule as examples (see the last line of §6.5): what you reuse is **structure and feel** — persona, verbal tics, signature lines, and opening-message content must be original. No reskins.

## 6.5 Example cards (read one of the same type before writing)

`examples/` ships seven high-score-line examples (structure score 87+):

- `card-jiangxubai.md`: **strongly female-oriented**, top-locked · urban medical, older · type-A live event scene (midnight rooftop, cigarettes and a case file) — lorebook demonstrates a **voice-sample entry + random-event entries** (`prob/cooldown`); medical-monitor theme beautification.
- `card-linyao.md`: **strongly male-oriented**, BG · urban street-life · type-A live event scene (a sassy gal at the convenience store before dawn) — lorebook demonstrates **event-book five-piece entries** (`secondary + logic` narrowing + branch trigger keywords); receipt-theme beautification.
- `card-hezhao.md`: female-oriented, top-locked · urban, older · type-A live-event opening (a pawnshop late at night) — the classic single-character structure; pawn-ticket theme beautification.
- `card-yuchi.md`: female-oriented, top-locked · campus online-dating · type-B dual-POV (chatterbox online / tongue-tied offline, two language fingerprints) — the gap-contrast benchmark; game-HUD theme beautification.
- `card-wangushi.md`: all-orientation · dream night-market simulator · type-E guide console (multi-NPC verbal tics + a truth-ticket economy) — the simulator/ensemble benchmark; lantern night-market theme beautification.
- `card-luchengfeng.md` (91.2 points): female-oriented · esports alt-account fiction · dual-channel parallel narration (online IM fragments vs. offline physical narration, two prose systems) — a **symbol-emotion dictionary lorebook** (stable semantics for instant replies / periods / kaomoji) + IM-bubble beautification + a Drip-Feed Law demonstration.
- `card-weilanshan.md` (87.9 points): male-oriented · onee-san × urban superpowers · a **five-stage glacier-meltdown** pacing lorebook (stage conditions written down to prevent stage-skipping) + invisible safety-net records (a hidden thread that can only be discovered) + an inner voice styled as phone-contact remarks.

What you learn from examples is **structure and feel** (how the four-piece kit is ordered, how the lorebook is modularized, how commands are written, the Creator Notes voice). Character content must be original — no reskinning an example's settings, tics, or signature lines.

Honesty note: some cards in examples/ predate the finalized §2.6 card-copy prose discipline and would not pass today's anti-AI-flavor gate on a retest (over-quota fake-precision numbers are the main defect) — they are **structure references**, not prose references; the finished prose standard is the shipped cards under `cards/` (CI locks all of them at anti-AI-flavor 5/5).

## 7. Validation and scoring

When done, run the repo scorer (from the repo root):

```
python3 tools/score_card.py card-<slug>.md
```

Eight scoring dimensions (100 total): field completeness 13 / opening-message structure 18 / the nine liveliness techniques 24 / tags & track 10 / beautification & commands 12 / lorebook 8 / image prompt 10 / anti-AI-flavor 5.

- ≥85: publishable. 75–84: usable; list the weak items in your report. <75: fix per the issues list, at most 3 iterations, then report honestly.
- The nine liveliness techniques = micro-behavior flaws / word-deed mismatch / refusal and boundaries / private life present / language fingerprint / physical-detail anchoring / unfinished-event drive / **cognitive limits and misreading** / **emotional inertia**. The script names whichever is missing; patch them one by one.
- The lorebook dimension checks: entry count meets the tier (single-character ≥5, world card ≥12), keyed entries + always-on modules both present, includes affection tiers / skit pack / NPC dossiers; bonus points: voice-sample entry / event-book five-piece kit / advanced meta (prob/secondary/sticky) used correctly; deductions per §4.5.8.
- If the skill is installed standalone (without the repo's `tools/`), the scoring step may be skipped — but §3's hard checks and §2.6's seven-defect self-check still apply.

Convert to SillyTavern-importable formats (v2 JSON + v3 PNG-embedded):

```
python3 tools/card_md_to_st.py card-<slug>.md --avatar cover.png --outdir out/
```

## 7.5 Liveliness self-test (recommended: one round)

Import the card into any frontend (SillyTavern / Foreverse) and chat 4–5 rounds, cycling the stress list: comply and follow → provoke and cross a line → unrelated small talk → time skip → bring up something old. Check against:

- The character's tics and forms of address stay stable across five rounds, no drift;
- On the boundary-crossing round the character refuses or goes cold — no compliant pleasing;
- In small talk the character has its own life details to offer — no idling;
- On the time skip it can close the scene and open a new one — no looping in place;
- Throughout: never speaks for you, never reads your mind, never discusses AI.

Then check the **four AI-flavor syndromes** (the four losing points in the competitor same-prompt comparison; any one appearing → go back and fix the four output-rhythm laws):

- **Essay syndrome**: >200 characters per turn as the norm; a fixed three-beat template every turn;
- **Explainer syndrome**: narration translating emotions for the reader (「显然/带着几分/眼中满是/并非真要责怪」 "clearly / with a touch of / eyes full of / not really blaming" — explainer stock phrases);
- **Smoothness syndrome**: no pushback when offended; a logically complete deposition when falsely accused; service-grade gentleness throughout;
- **Bookish syndrome**: dialogue all complete subject-verb-object long sentences — zero fragments, zero single-word lines, zero duplicity.

If any check collapses, fix the corresponding section of the card (tic drift → Personality language fingerprint; compliant pleasing → system_prompt adversity handling; idling → private-life evidence and unfinished events; the four syndromes → system_prompt four output-rhythm laws + fragment samples in the voice-sample entry), re-import, retest.

## 8. Wrap-up

Final self-check before wrap-up (any missing item → go back and fix; no wrap-up without it):

- [ ] `## First Message` is non-empty (Chinese ≥100 characters / English ≥60 words).
- [ ] The card's language matches the user's request (user asked in English / the source novel is English → the whole card in English, opening message and lorebook included).
- [ ] When the scorer is available: anti-AI-flavor at full marks, total score at threshold.

The text-only wrap-up states only: character name, four-axis positioning, structure score and weak items, whether a cover was generated, artifact file paths. If the card uses HTML, remind the user: the frontend must have HTML rendering enabled. Produce no unrelated documents.

## Appendix: In Foreverse

This skill shares its lineage with the shelf agent built into the Foreverse Android app — tell the in-app agent "write me a character card" and it runs this same playbook with the whole chain wired natively: write the card → score it → generate the cover → one-tap into the library and start chatting → publish to the community; it can also read novels on your shelf and distill characters from them (card-from-novel). Free download: <https://foreverse.app>. This repo's version targets general agent environments (Claude Code / Cursor / Codex / Gemini CLI, etc.); convert the output with `tools/card_md_to_st.py` for import into SillyTavern or any frontend that supports chara_card_v2/v3.
