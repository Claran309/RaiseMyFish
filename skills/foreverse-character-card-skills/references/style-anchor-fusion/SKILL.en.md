---
name: style-anchor-fusion
description: Corpus-grounded style mimicry for long-form prose — deep-read real published novels before writing, distill them into a "voice-anchor notes" file, then write with the notes (what to sound like) AND the anti-AI-flavor discipline (what to ban) running together. Use for long-form fiction, character-card copy (intro / greetings / example dialogue), any "make it read like a real book" task, and "the prose feels too AI / too machine" repair requests. Evidence framing (8 manuscripts × ~100k chars + a human re-audit, §8): machine-verified — the discipline blacklist drives slop to 0.00, and the corpus contributes at the vocabulary/objects/register layer; the "ranked #1 in blind review" claim was downgraded on re-audit to LLM-judge preference and is no longer cited as proof the workflow works; heaviest lesson — the fragmented "literary" cadence is the model's own tic, reading corpus does not change sentence shape, so explicit bans + fingerprint acceptance are mandatory (§5.5/§5.6). Division of labor in one line — discipline bans; corpus models the vocabulary/objects/register layer. Tools: anti-plagiarism gate tools/plagiarism_check.py + fragment-cadence census tools/fragment_census.py. This file is a translated mirror of SKILL.md (the Chinese original; the experiment was run on Chinese web novels).
---

# Voice-Anchor Fusion Mimicry (style-anchor-fusion)

> Translated mirror of `SKILL.md`. The experiment behind this skill was run on Chinese
> web-novel prose; the method transfers, but the quoted numbers, the blacklist examples
> and the sliding-window tool are calibrated for Chinese. For English prose, keep the
> workflow and re-derive the word-level details (the plagiarism checker's docstring
> explains the word-token variant).

To make a model write like a real book, use **both** the corpus and the bans — but be
precise about which layer each one actually governs. The framing below comes from a
controlled experiment (8 isolated agents blind-writing ~100k characters on the same
premise) **plus a human re-audit that overturned the experiment's taste-based
conclusions** (provenance in §8): **discipline governs the vocabulary layer** — driving
slop phrases into the human band is machine-verified; **corpus governs the
vocabulary/objects/register layer** — how office talk sounds, what the props are
called, how trade cant runs, provable by side-by-side reading; **neither governs
sentence shape** — the fragmented "literary" cadence is the model's own tic, and 740k
characters of corpus did not move it. Sentence shape yields only to explicit bans +
fingerprint acceptance (§5.5/§5.6). This skill freezes the workflow: **deep-read the
corpus before writing → distill voice-anchor notes → write with notes + anti-AI-flavor
discipline together → re-read to prevent drift → sliding-window plagiarism check +
fragment-fingerprint acceptance on the finished draft**.

Three iron rules up front:

1. **Corpus must ship together with the anti-AI-flavor discipline. Corpus without
   discipline is a net negative** — a machine-metric verdict (the human re-audit only
   strengthened it): in the corpus-only arm nobody stopped the slop reflexes, and the
   sentence layer learned nothing either — its fragment fingerprint was among the worst
   in the field (bare-quantifier appositive insertions at 2.64 per 10k chars, highest
   of all eight manuscripts, while the three corpus books measure 0). Three books read,
   zero automatic gain on either layer: the output is "similar but dirty".
2. **Division of labor: discipline bans, corpus models — and what the corpus verifiably
   models is the vocabulary/objects/register layer.** Discipline supplies the negative
   list (slop-phrase blacklist, sentence-pattern quotas, anti-uniformity structure
   rules); corpus supplies the positive templates (voice vocabulary, address terms,
   trade detail, idle-detail material). Sentence rhythm is not something the corpus can
   fix (§5.5); on conflict, **discipline wins** (worked examples in §5).
3. **Learn the voice, never copy the words.** All mimicry happens at the style layer:
   no verbatim run of ≥15 characters, zero corpus proper nouns, sliding-window
   self-check on the finished draft (§6). In the experiment, 6 corpus-arm manuscripts
   went through 64,265 windows with **0 hits** — the red line is fully achievable.

The anti-AI-flavor discipline itself lives elsewhere: for card copy see
`skills/character-card-author/` (the card-copy prose discipline and the style-selection
layer); for standalone prose scans use `tools/ai_flavor_check.py`. For long-form work,
assemble the same three pieces — a slop blacklist, sentence-pattern quotas (e.g. "not
A, but B" ≤3 per 100k chars), and anti-uniformity rules (high sentence-length variance /
≥2 idle details per volume / no same-shape chapters). This skill covers only the corpus
side and how the two stacks combine.

## 1. Picking the corpus: the three-book method

**Pick 2–3 real published books from the same fusable genre domain, with complementary
jobs**:

| Role | What it owns | Experiment example (premise = Eastern classical supernatural detective) |
| --- | --- | --- |
| Skeleton | Plot pacing, mainline reference, payoff structure | *Da Feng Da Geng Ren*: case-chain deduction, yamen office ecology, street-level officialese |
| Atmosphere | Genre texture, scene grain, suspense craft | *Lao Shi Ren*: folk-horror daily-life grain, mid-sentence chapter breaks, rural spoken register |
| Prose ceiling | Text-density ceiling, rhetorical register | *Chi Xin Xun Tian*: prose-poem short paragraphs, classical parallelism, poetic restraint |

Selection criteria:

- **The three books' style axes must be orthogonal** (payoff register / atmosphere
  register / prose register each owning one lane). That is what makes fusion carry real
  information; three same-flavored books are padding, not nutrition.
- **Think through the rejections too**: a book whose genre fits but whose register
  doesn't can serve as atmosphere but never as skeleton (*Lao Shi Ren* fits
  "supernatural" but lives in a 1980s riverside town — wrong register for a dynasty
  setting); the best-prose book with zero detective elements would float the draft off
  premise if deep-mimicked alone (*Chi Xin Xun Tian*).
- **Anti-example warning: cross-register fusion is noise.** The experiment deliberately
  refused a 4th book — romance/Western-fantasy/esports corpora clash with an Eastern
  classical supernatural premise and only dilute the voice. Two complementary books
  beat four mixed ones.

**When single-book deep mimicry applies**: only when the genre fits extremely closely
(scene vocabulary borrows directly, no era/register mismatch) AND the style is strong
and recognizable (success is visible to the naked eye). Know that it is the
**high-variance option**: a commercial book's reader-taste positioning comes bundled
(the experiment's deep-mimic manuscript swung from 1st to 5th across LLM judges of
different tastes — the ranks themselves are only judge preference, but a swing that
wide shows the taste bundle does get inherited wholesale), even the typography layer
migrates (curly quotes), and the corpus's performative tics (banter voice, four-dot
ellipses) will fight your register consistency. When unsure, take three-book fusion.

## 2. How much to read

- **Skeleton book: close-read ≥10 chapters. Every other book: ≥8 chapters each.**
  (Experiment reference: the deep-mimic arm close-read ~30 chapters / 128k characters
  before writing; the fusion arms read each book to its job, close + skim.)
- "Close-read" = read with the pen down: you are writing §3's voice-anchor notes as you
  go — quoting anchor lines, logging craft moves, listing banned proper nouns. You are
  done when you can close the book and reproduce, from notes alone, how it opens
  chapters, closes chapters, and voices dialogue.
- Deep reading happens **before writing**. Notes land on disk first, then you write.
  Mid-writing corpus contact follows §4's re-reading rule.

## 3. The voice-anchor notes (style-notes.md) spec

The notes live in the work's working directory (next to the outline and the writing
ledger), finished before chapter one. **Six dimensions are mandatory**:

1. Sentence rhythm (paragraph length, short/long mix, where single-sentence paragraphs go)
2. Chapter openings / closings (opening moves, hook shapes, the mid-sentence break trick)
3. Dialogue register & address terms (speaker tagging, honorific system, interrogation chains)
4. Interiority handling (whose head we live in, when inner voice goes silent, how it cuts into narration)
5. Description density & placement (how few strokes stand a character up, scenery share, how numbers are used)
6. Payoff / suspense layout (the core loop, press-then-release, where slice-of-life sits)

**Quote 5 passages of ≤50 characters per book as "voice anchors"** — these are what you
flip back to mid-writing to re-tune your ear. Pick the lines that carry the book's voice
most, and label what craft move each one anchors. (Anchors are your private notes; the
red line governs the *finished draft*, which must contain no corpus text or proper
nouns — see §6.)

Template (structure distilled from the fusion+discipline arm's real notes; sample lines below are
invented illustrations, not quotes from any corpus):

```markdown
# <Work> fusion recipe (done before writing; consult throughout, self-check every 5 chapters)

## 1. Style DNA per book (one section each)
### 1.x <Book> = atmosphere (one line on what it owns)
Voice anchors (verbatim quotes, ≤50 chars, each labeled):
1. "House rules: money gets counted to your face, truths never get said to it." — trade-talk parallelism, street register
2. "The river lay quiet. Quiet like something under it was listening." — horror grown out of daily objects
   … (5 total)
Style notes:
- Horror grows on daily life: make the porridge, the cigarettes, the credit ledger solid first — only then does the ghost land. Mundane setup ≥1/3 of any horror chapter.
- Chapter-end breaks: cut mid-sentence ("She—", "Then,") and let the next chapter finish the breath. Not every chapter; rotate.
- **What NOT to learn** — invent this world's version of the old men's swear words, never copy the originals.

## 2. Fusion rules (which register owns which scene)
| Scene | Lead register | Support | Notes |
| Yamen, casework, interrogation | skeleton book | atmosphere book (spoken register) | dialogue-driven; the clue chain must advance every chapter |
| Elegy, life-and-death set pieces | prose-ceiling book | skeleton book (banter as pressure release) | the only big budget zone for short paragraphs and parallelism |
Transitions: switch registers across an "object bridge"; ≤3 consecutive lyric paragraphs, then fall back to action or dialogue.

## 3. Character voice fingerprints (recognizable with the name covered, one line each)
- Protagonist: procedure rhymes on his lips, sharp-tongued ledger-keeping in his head, few words out loud.
- Supporting A: ends sentences with "mark me"; Supporting B: officialese, everything "per regulation".

## 4. Banned proper nouns (corpus names/places/orgs/technique names/signature set pieces — zero in the draft)

## 5. Discipline self-check list (slop blacklist + pattern quotas + anti-uniformity — copy the key rules in)
```

Key points: the fusion-rules table is the soul of the three-book method — **registers
are not blended, they are sliced by scene**; once voice fingerprints are written down,
every character's dialogue has a checkable standard; and the discipline checklist is
*copied into* the notes (not just referenced) so that §4's "re-read after context
compaction" needs exactly one file.

## 4. The re-reading rule (drift prevention)

Writing is a long run and the voice drifts — either collapsing into a single source
(three-book fusion decaying into skeleton-book-only) or sliding back to the model's
default register (more and more "GPT" as chapters pass). Machine audit confirmed the
decay is a **continuous trend, not a cliff** (the worst manuscript's slop density:
41.7 per 100k chars in the first half → 199.7 in the second half, a 4.8× degradation),
so checks must be periodic and back-loaded:

- **Every 5 chapters written, re-read 1 chapter of each corpus book** to re-tune the
  ear; run §3's discipline checklist in the same sitting.
- **Set checkpoints by character count, not chapter count**: one self-audit every 20k
  characters, focused on your 5 most recent chapters — slop counts, chapter-length
  trajectory (last/first ratio <0.7 = density-decay alarm), voice bleed.
- **"Skim one chapter at the ¼ mark and one at the ¾ mark, compare the voice"** — a
  judge's working method from the experiment, stolen for self-audit. If the two don't
  match, you have drifted.
- **Long-form ledger discipline**: update the writing ledger (state file) every 4–5
  chapters (mainline progress / character states / foreshadowing ledger / timeline);
  **after any agent context compaction, re-read style-notes.md + the outline + the
  last 2 chapters before writing on** — the notes are the only anchor that recovers
  the voice after compaction amnesia.

## 5. Stacking with the anti-AI-flavor discipline (the load-bearing section)

**Division of labor: discipline bans, corpus models.** Corpus supplies positive
templates (how office talk actually sounds, how a chapter-end break lands, how dense
idle details run); discipline supplies the negative list (slop blacklist,
sentence-pattern quotas, anti-uniformity). Neither alone is enough:

- Discipline only = **clean but sourceless** (slop at 0.00 is machine-verified — the
  bans do work; but the vocabulary/objects/register layer has nowhere to learn from —
  office talk, trade cant, prop detail are positive templates bans cannot supply).
- Corpus only = **similar but dirty** (writer's log plus machine counts: "blacklisted
  words have muscle memory — they grow back on their own in any unguarded descriptive
  sentence" — without bans standing guard, no amount of reading stops the slop; and
  the sentence layer does not automatically become more human either, see §5.5).

**Conflict rulings (two worked examples, both from the fusion+discipline arm)**:

1. **Corpus-frequent word hits the blacklist → discipline wins; keep the beat, change
   the wording.** The corpus loves "pupils contracted" and it is blacklisted — you
   don't drop the beat, you swap emotion-naming for **externalized action** (what the
   hands do, what the object does). One line: **voice follows the corpus, vocabulary
   follows the discipline.**
2. **Corpus meme clashes with your setting → keep the voice-and-manner, drop the
   form.** The corpus's modern-transmigrator memes (internet slang, modern-noun
   banter) clash with a classical setting — the banter register **stays** (the
   ledger-keeping, sharp-tongued, self-mocking manner), the content is **fully
   re-localized** (this world's vocabulary: lamp oil, copper coins, trade cant). You
   learn *how it snarks*, not the words it snarked.

> **Bolded warning (hard-coded, no bypass): corpus without the anti-AI-flavor
> discipline = net negative.** Machine counts: the corpus-only arm's slop density ran
> unchecked AND its fragment fingerprint was among the worst in the field
> (bare-quantifier insertions 2.64 per 10k chars, highest of all eight) — a loss on
> both the vocabulary layer and the sentence layer; the human re-audit only
> strengthened this conclusion. Every gain from the corpus workflow is conditional on
> the discipline being present; cutting the discipline and keeping the corpus means
> three books read for a worse manuscript.

Final-sweep calibration: **do not chase "absolute zero"**. Grinding the blacklist down
to 0.00 puts you *below* every real book's rough-edge level (measured baseline across
16 human-written novels: even the cleanest has 0.09 per 10k characters) — "too clean"
is itself a reverse fingerprint. Aim inside the human band (≤1 per 10k characters) and
spend the saved revision budget on idle details and supporting-cast voices — that is
where readable, felt differences live, not in grinding the blacklist from 0.5 to 0.

## 5.5 Fragment-cadence guardrails (the sentence layer — the one the corpus cannot reach)

The heaviest finding of the human re-audit: **the comma-chopped "literary" fragment
cadence is the model's own tic, not something learned from the corpus.** A bare arm
that read zero corpus produced nearly the same cadence as the fusion arm, and their
narration-layer fragment fingerprints landed in the same band — while every real book
(including the 740k characters of corpus) narrates on a mid-to-long-sentence base,
using fragments only as occasional crests on emotional peaks (measured: a famous
horror set piece lands 3 fragment beats in thirty lines, all on peaks). **Reading
corpus does not fix sentence shape. Explicit bans plus post-draft fingerprint
acceptance do.**

Three sentence-pattern bans (quotas anchored to the measured corpus usage):

1. **Bare-quantifier appositive insertion — banned.** The pattern "The stele wasn't
   tall, four feet, its body black": a lone bare measurement suspended between two
   commas as a spec for the previous clause. Zero hits in 740k characters of corpus —
   pure model invention. (Progressive enumeration — "one loop, two loops, three
   loops" — does exist in real books and is not banned.)
2. **Suspended-time verdict sentence — ≤1 per volume.** The pattern "Tonight, it was
   his hand": a time word chopped off alone, then an "it was…" lyric pronouncement.
   Zero hits in the corpus — every corpus "…, it was…" is dialogue response or spoken
   inference, never a narrated lyric suspension.
3. **Dangling scene-setting paragraph — ≤18 per 10k chars, chapter-opening/scene-change
   only.** The pattern "In the fog, the night-watch clapper sounded." / "Nightfall.
   Deployment.": a location/time word + comma + short predicate (or a bare noun)
   standing as its own paragraph. This shape IS real — but real books use it as a
   chapter-opening or scene-change gong, once or twice a chapter, then return to long
   sentences; writing it as the default beat of ordinary narration (once every few
   lines) is the machine cadence at 2–5× the human rate. 18 per 10k chars is the top
   of the corpus band.

Post-draft acceptance — run the fragment census against the human-book band:

```bash
python3 tools/fragment_census.py --target draft-dir/
# add --anchor corpus1.txt corpus2.txt to re-anchor the band locally;
# --examples prints the hit lines; alerts exit non-zero
```

Human-book reference band (band measured across the experiment's 16 human-written
novels; the table shows representative values from 3 corpus books + 2 control anchors;
**re-anchor with --anchor whenever the corpus/genre changes** — and note the tool's
regexes are Chinese-specific; for English keep the idea, re-derive the metrics):

| Metric (narration layer, dialogue stripped, per 10k chars) | Human band | Alert |
| --- | --- | --- |
| Clauses ≤4 chars, share | 8–19% | — |
| Location/time-led sentence openings | 0–2.5 | — |
| 1–2-char standalone sentences | 0.1–2.1 | >5 |
| Bare-quantifier appositive insertions | 0 | >0 |
| Dangling scene-setting paragraphs | 3–18 | >20 |

The three alert lines separated 7 of the 8 machine manuscripts from all 5 reference
human books at the time of measurement. The mechanism in one line: in real books
fragments are the crest and long sentences are the water; in machine prose fragments
are a metronome — each one reads "literary" alone, and together they read as the tic.

## 5.6 Acceptance discipline (style acceptance must not rest on LLM cross-review alone)

The experiment's four LLM judges (four models × four personas × four reading orders,
never talking to each other) produced a consensus that pointed **backwards** on the
"human-like prose" dimension: they crowned the manuscript whose fragment fingerprint
sat farthest from every real book, and unanimously ranked second-to-last on prose the
only manuscript whose narration fingerprint fell entirely inside the human band. That
mirrors our earlier blind experiment (`docs/blind-review-experiment.md` in this repo):
inter-judge agreement 86%, consistently wrong. LLM priors about "literariness" are
highly correlated across models — a head consensus is not four independent votes.

Hard rule: **style acceptance requires one of the two, no exceptions** —

- **Human spot-reading**: pull one continuous narration passage each from the
  beginning / middle / end (250–400 chars each), read side-by-side against the corpus,
  asking per passage "does this pattern/density actually occur in the corpus?";
- **Human-book fingerprint anchor**: run the §5.5 fragment census + slop density
  against the human band, by machine.

LLM cross-review remains useful, but only for **line-level evidence** (verbatim repeat
paragraphs, meta-narrative leakage, slop counts, chapter-length decay — judges catch
these reliably and machines can re-verify); the verdict on "does it read human" is not
theirs to give.

## 6. Anti-plagiarism red lines

Mimicry learns the **voice**, never the **words**. Three hard lines:

1. **No verbatim run of ≥15 characters** (pure-CJK-stream metric, punctuation stripped
   before matching — re-punctuating cannot launder a copy).
2. **Zero corpus proper nouns**: no person/place/organization/technique/title from the
   corpus may appear in the draft. List the full ban list in your notes before writing
   (§3 template, section 4); signature set pieces may only be reused structurally if
   deformed beyond recognition — or just don't.
3. **Run the sliding-window self-check on the finished draft**; only zero hits passes:

```bash
python3 tools/plagiarism_check.py \
  --corpus corpus1.txt corpus2.txt corpus3.txt \
  --target draft-dir/        # add --sample 5 for a fast first/¼/mid/¾/last pass; non-zero exit = hits
```

Independent audit in the experiment: 6 corpus-arm manuscripts × 5 chapters each,
64,265 15-char windows, **0 hits** — the red line costs the mimicry nothing. There is
no excuse for crossing it. (The tool's CJK metric is Chinese-specific; for English,
swap the Han regex for a lowercase word-token stream and window over words.)

## 7. Model minefields (long-form selection & symptom watch)

The same fusion brief went to four models and came back as everything from
LLM-judge favorite to unanimous last place (the bottom had line-level receipts) —
**switching models moves the needle more than switching methods**. Selection notes
for 100k-character work (evidence = the experiment's model axis, line-level/machine
evidence first; taste-ranking claims are labeled per the re-audit; n=1 per arm, treat
as symptom lists, not verdicts):

- **Head of the field (caution: "head" = LLM-judge preference, not human-calibrated)**:
  Opus 5 was the judges' favorite (first in all four sub-dimensions), but its dangling
  scene-setting density was the highest of the whole field (76 per 10k chars, 4×+ the
  corpus band) — the judges' literary marks reward exactly that fragment cadence, so
  pairing it with the §5.5 bans is mandatory; characteristic weakness = under-supplied
  payoff moments, chapters growing longer and longer. Fable 5 + this skill's full
  workflow is the value tier (the workflow secures the vocabulary layer and
  anti-plagiarism; for its sentence-layer minefield see the next item).
- **Fable-family minefield: the fragment "literary" cadence is built in (present when
  writing bare; corpus does not correct it).** Warning signs = the three patterns:
  bare-quantifier appositive insertion ("not tall, four feet"), suspended-time verdict
  sentences ("Tonight, it was…"), high-density dangling scene-setting ("Nightfall.
  Deployment." every few lines). Watch: run the §5.5 fragment census against the human
  band; write the three bans into the brief explicitly — do not expect corpus exposure
  to fix it.
- **Sonnet-family minefield: runaway "emotion labeling" + back-half density decay.**
  Symptoms: emotion-naming phrases ("with a trace of…") recurring at high frequency
  (measured 101 per 100k chars, degrading 4.8× in the back half as a continuous
  trend); chapter-length cliff (last-5/first-5 ratio 0.42). Watch: count the
  "trace-of" family and plot chapter lengths every 20k characters; on a worsening
  trend, stop — it does not self-heal. The other side, recorded honestly: its sentence
  shape was the only one in the field to fall entirely inside the human-book
  fingerprint band — it loses on slop control and long-range density, not on cadence.
- **Grok-family minefield: long-range synopsis collapse + meta-narrative leakage.**
  Symptoms: mean chapter length collapsing under 1k characters (first attempt: 30
  chapters, 27k chars total); craft jargon leaking into the prose ("end of volume
  one", "the hook", "(addendum)", "the reader"); verbatim paragraph repeats and
  template paragraphs rotating across chapters. Watch: one `grep` for craft-jargon
  words + a ≥30-char verbatim-repeat scan. **Warning: imposing a hard word-count floor
  on a density-collapsing model treats the symptom and rebounds** — with a
  "2,500–4,500 chars per chapter" clause added, the collapse converted into padding
  via template and repeat paragraphs. The fix is switching models or cutting the task
  shorter, not adding clauses. **Short-form exemption**: the same model's first two
  chapters earned a judge's "best language of the round" — it fails at 100k-character
  self-management, not at prose. No need to ban it from short pieces like card
  greetings.
- **Four machine gates** (cheap early-warning, consistent with the bottom two arms'
  line-level receipts): slop density ≤1.5 per 10k chars (human-band ceiling);
  "trace-of"-family count (the runaway manuscript: 149 vs ≤16 everywhere else);
  chapter-length last/first ratio ≥0.7; the three fragment-fingerprint alert lines
  (§5.5: dangling scene-setting >20 per 10k chars, 1–2-char standalone sentences >5
  per 10k chars, bare-quantifier insertions >0).

## 8. Evidence & provenance

The numbers and conclusions in this skill come from one controlled experiment we ran
in 2026-07 (internally: style-fusion-bench) **plus a human re-audit** (triggered by a
human reader challenging three sentence patterns in the top-ranked manuscript; the
re-audit re-tried every taste-based conclusion with verbatim quote-matching against
the corpus and a fragment-fingerprint census):

- **Writing**: 8 mutually isolated agents blind-wrote ~100k characters each on the
  same premise (Eastern classical supernatural detective). Method axis, 5 arms on one
  model: bare / discipline-only / single-book deep mimicry / three-book fusion /
  fusion+discipline. Model axis, 4 arms on one brief: Fable 5 / Opus 5 / Sonnet 5 /
  Grok 4.5.
- **Review**: the 8 manuscripts were anonymized and ranked by four LLM judges (four
  models × four personas × four reading orders), restricted to same-premise relative
  ranking with quoted evidence — no "is this AI?" absolute judgments (our earlier
  blind experiment showed LLM judges are unreliable at that; see
  `docs/blind-review-experiment.md` in this repo). Machine metrics (sentence-length
  CV / slop density / dialogue share / chapter-length trajectory / fragment
  fingerprint) were audited against a 16-novel human-written baseline.

**When citing conclusions from this skill, keep the three evidence tiers apart**:

- **(a) Machine-verified (citable as hard evidence)**: the discipline blacklist drove
  slop to 0.00 (machine counts — hard proof the bans work, though only at the
  vocabulary layer); the corpus's contribution at the vocabulary/objects/register
  layer (provable by side-by-side reading against corpus text — the evidence is
  parallel passages, not judge quotes); the anti-plagiarism red line is achievable
  (6 corpus-arm manuscripts, 64,265 15-char windows, 0 hits); the bottom two arms'
  line-level receipts (verbatim paragraph repeats, craft-jargon leakage, "with a
  trace of" ×101, chapter-length last/first ratio 0.42 — grep-level evidence,
  independent of anyone's taste).
- **(b) Overturned/downgraded by the re-audit (may only be described as "LLM-judge
  preference", never as proof the workflow works)**: "fusion+discipline ranked #1
  overall", the "reads most like a serialized human novel" special mention, "the
  combination jumps two tiers", "Opus 5 is the literary-texture ceiling" — all of
  these rest on the four judges' aesthetic consensus, and the re-audit showed that
  consensus points **backwards** against measurable standards: the judges crowned the
  manuscript whose fragment fingerprint sat farthest from every real book, and
  unanimously ranked second-to-last on prose the only manuscript whose narration
  fingerprint fell entirely inside the human band. Four independent judges agreeing
  on the fragment cadence mirrors our earlier blind experiment's "86% inter-judge
  agreement, consistently wrong".
- **(c) The heaviest lesson of the experiment**: the fragment "literary" cadence is
  the model's own tic, and corpus mimicry does not change sentence shape — the
  zero-corpus bare arm produced the same cadence, same fingerprint band, as the
  fusion arm. Sentence shape yields only to explicit bans (§5.5) + fingerprint
  acceptance (§5.6). The defensible gains of "reading real books" shrink to: the
  vocabulary/objects/register layer, plus stylistic safety in the anti-plagiarism
  sense.
- **Honest limits** (read before citing): n=1 per arm; the hard conclusions that
  survive the re-audit are "corpus-only no gain (strengthened by the re-audit)",
  "the bottom two arms' line-level receipts", "discipline's vocabulary-layer
  suppression is machine-verified", and "the fragment cadence is model-native" —
  four items; "fusion+discipline is best" is no longer a hard conclusion, and the
  method-axis head ordering awaits a human blind test; single premise, Chinese
  long-form — extrapolation to other registers/languages is limited.

Field lessons are welcome back into this file (PRs): new conflict rulings, new model
minefields, template improvements — with checkable evidence attached.
