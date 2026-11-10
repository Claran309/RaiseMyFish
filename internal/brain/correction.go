package brain

// 本文件实现「纠正状态 / 事实撤回」的可见窗口推导。
//
// 目标不是改写数据库里的历史，而是保证**本轮生成输入**里：
//   - 明确记录谁纠正了什么、正确归属是谁；
//   - 被纠正的旧回复（以及由它派生的摘要/事实）不再作为有效资料注入；
//   - 重问时模型能看到原始证据，承认纠正并按正确归属回答。
//
// 判断只依赖本轮事件与已召回的 Focus/Recent/Records/Facts，不新增表；
// 窗口之外的历史纠正在本批不支持（见交付说明的未做项）。

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

const (
	maxCorrections          = 2
	maxCorrectionTextRunes  = 120
	maxCorrectionEvidence   = 2
	maxCorrectionQuoteRunes = 100
)

// correctionSection 是一条已发生的纠正。
//
// superseded 是「已被纠正的旧说法」事件 ID；retracted 是由它们派生的
// 摘要/事实。生成输入里这些内容会被移除，模型不能再当作有效资料。
type correctionSection struct {
	EventID            string                      `json:"event_id"`
	At                 time.Time                   `json:"at"`
	Speaker            actorSection                `json:"speaker"`
	Text               string                      `json:"text"`
	Denied             *attributionSection         `json:"denied,omitempty"`
	Claimed            *attributionSection         `json:"claimed,omitempty"`
	SupersededEventIDs []string                    `json:"superseded_event_ids,omitempty"`
	RetractedRecordIDs []string                    `json:"retracted_record_ids,omitempty"`
	RetractedFactKeys  []string                    `json:"retracted_fact_keys,omitempty"`
	OriginalEvidence   []correctionEvidenceSection `json:"original_evidence,omitempty"`
	// Disputed 表示有人提出纠正，但本轮没有足够证据定位具体被纠正的旧内容。
	// 此时不撤任何事件/摘要/事实，来源保持原样。
	Disputed bool   `json:"disputed,omitempty"`
	Note     string `json:"note,omitempty"`
}

// attributionSection 是纠正里的「谁说的」归属。
type attributionSection struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// correctionEvidenceSection 是正确归属人的原始发言证据。
type correctionEvidenceSection struct {
	EventID string       `json:"event_id"`
	At      time.Time    `json:"at"`
	Speaker actorSection `json:"speaker"`
	Text    string       `json:"text"`
}

// buildCorrections 从当前事件与焦点/房间流里提取最近纠正，并返回失效事件集合。
func buildCorrections(in ContextInput) ([]correctionSection, map[string]bool) {
	all := collectedMessages(in)
	invalid := make(map[string]bool)
	var found []memory.Message
	for _, m := range all {
		if m.Dir == event.DirIn && isCorrectionText(m.Text) {
			found = append(found, m)
		}
	}
	if len(found) == 0 {
		return nil, invalid
	}
	if len(found) > maxCorrections {
		found = found[len(found)-maxCorrections:]
	}
	out := make([]correctionSection, 0, len(found))
	for _, corr := range found {
		entry := buildCorrection(corr, in, all)
		for _, id := range entry.SupersededEventIDs {
			invalid[id] = true
		}
		out = append(out, entry)
	}
	// 失效集合定型后再标记被撤回的派生资料，避免「先过滤再算」漏项。
	for i := range out {
		entry := &out[i]
		entry.RetractedRecordIDs = retractedRecords(in.Memory.Records, entry.SupersededEventIDs)
		entry.RetractedFactKeys = retractedFacts(in.Shared.Facts, entry.SupersededEventIDs)
	}
	return out, invalid
}

// buildCorrection 组装单条纠正：归属、原始证据与失效事件。
func buildCorrection(corr memory.Message, in ContextInput, all []memory.Message) correctionSection {
	denied := resolveDeniedActor(corr, in)
	claimed, hasClaim := resolveClaimedActor(corr.Text, in)
	entry := correctionSection{
		EventID: corr.EventID,
		At:      corr.At,
		Speaker: actorSection{ID: corr.FromID, Name: corr.FromName},
		Text:    clipRunes(strings.TrimSpace(corr.Text), maxCorrectionTextRunes),
	}
	if denied.Name != "" {
		entry.Denied = &attributionSection{ID: denied.ID, Name: denied.Name}
	}
	if hasClaim && !sameAttribution(denied, claimed) {
		entry.Claimed = &attributionSection{ID: claimed.ID, Name: claimed.Name}
		entry.OriginalEvidence = originalEvidenceFor(corr, claimed, all)
	}
	superseded, disputed := supersededEvents(corr, denied, claimed, entry.OriginalEvidence, all)
	entry.SupersededEventIDs = superseded
	entry.Disputed = disputed
	switch {
	case disputed:
		// 找不到「具体是哪句」就不撤：保留来源，避免把一个人的其他正确发言
		// 或房间最近一条无关回复一起否定。
		entry.Note = "有人提出纠正，但本轮没有足够证据定位具体被纠正的旧内容；保留来源事件、摘要与事实，只承认存在争议，不要否定该人的其他发言。"
	case entry.Claimed != nil:
		entry.Note = "已纠正：更正人否认该说法，正确归属是「" + entry.Claimed.Name + "」；被纠正的旧回复与派生摘要已失效，回答先承认纠正，再按正确归属说。"
	default:
		entry.Note = "已纠正：更正人否认或更正了旧说法；被纠正的旧回复与派生摘要已失效，回答先承认纠正，并说明归属不确定。"
	}
	return entry
}

// collectedMessages 合并当前事件、焦点轮次与房间流，按时间升序去重。
func collectedMessages(in ContextInput) []memory.Message {
	all := make([]memory.Message, 0, 1+len(in.Memory.Focus)+len(in.Memory.Recent))
	seen := make(map[string]bool, 1+len(in.Memory.Focus)+len(in.Memory.Recent))
	add := func(m memory.Message) {
		if strings.TrimSpace(m.EventID) == "" || seen[m.EventID] {
			return
		}
		seen[m.EventID] = true
		all = append(all, m)
	}
	add(memory.Message{
		EventID: in.Event.ID, Channel: in.Event.Channel, Session: in.Event.Session,
		Dir: in.Event.Dir, FromID: in.Event.FromID, FromName: in.Event.FromName,
		Text: in.Event.Text, ReplyTo: in.Event.ReplyTo, Private: in.Event.Private, At: in.Event.At,
	})
	for _, m := range in.Memory.Focus {
		add(m)
	}
	for _, m := range in.Memory.Recent {
		add(m)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].At.Equal(all[j].At) {
			return all[i].At.Before(all[j].At)
		}
		return all[i].EventID < all[j].EventID
	})
	return all
}

// isCorrectionText 判断一句话是否在纠正「谁说过什么」。
//
// 只在有「说/讲/问」或明确记忆错误词时成立，避免把普通否定
// （「不是这个意思」）误判成事实撤回。
func isCorrectionText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, marker := range []string{"记错了", "搞错了", "弄错了", "你记错", "你搞错", "你弄错"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, marker := range []string{"不是我说的", "不是我说", "我没说过", "我没讲过", "别乱说", "你说错了", "你讲错了", "你记错"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if strings.Contains(text, "说的") && strings.Contains(text, "不是") {
		return true
	}
	if strings.Contains(text, "不是") && (strings.Contains(text, "那句") || strings.Contains(text, "这句")) &&
		(strings.Contains(text, "说") || strings.Contains(text, "讲") || strings.Contains(text, "问")) {
		return true
	}
	return false
}

// resolveDeniedActor 解析被否认的归属人。
//
// 「你记错了 / 你说错了」否认的是本鱼；「不是我说的」否认的是更正人自己；
// 「不是 B 说的」否认的是 B。无法解析时按更正人处理。
func resolveDeniedActor(corr memory.Message, in ContextInput) actorSection {
	author := actorSection{ID: corr.FromID, Name: corr.FromName}
	text := corr.Text
	for _, marker := range []string{"你记错", "你搞错", "你弄错", "你说错", "你讲错"} {
		if strings.Contains(text, marker) {
			return actorSection{Name: selfName(in.Persona)}
		}
	}
	name := deniedNameFromText(text)
	switch name {
	case "", "我", "本人":
		return author
	case "你":
		return actorSection{Name: selfName(in.Persona)}
	}
	return resolveActor(name, in)
}

// deniedNameFromText 取「不是 X 说的」里的 X。
func deniedNameFromText(text string) string {
	pos := strings.Index(text, "不是")
	if pos < 0 {
		return ""
	}
	rest := text[pos+len("不是"):]
	if end := strings.Index(rest, "说的"); end >= 0 {
		rest = rest[:end]
	}
	rest = strings.Trim(strings.TrimSpace(rest), candidateTrimCutset)
	rest = strings.TrimSuffix(rest, "说")
	if utf8.RuneCountInString(rest) == 0 || utf8.RuneCountInString(rest) > maxCandidateRunes {
		return ""
	}
	return rest
}

// resolveClaimedActor 取「是 X 说的」里的正确归属。
func resolveClaimedActor(text string, in ContextInput) (actorSection, bool) {
	name := claimedNameFromText(text)
	if name == "" {
		return actorSection{}, false
	}
	return resolveActor(name, in), true
}

// claimedNameFromText 取最后一个「是 X 说的」里的 X；「不是我说的」不算归属。
func claimedNameFromText(text string) string {
	idx := strings.LastIndex(text, "说的")
	if idx < 0 {
		return ""
	}
	prefix := text[:idx]
	pos := strings.LastIndex(prefix, "是")
	if pos < 0 {
		return ""
	}
	name := strings.Trim(strings.TrimSpace(prefix[pos+len("是"):]), candidateTrimCutset)
	if name == "" || utf8.RuneCountInString(name) > maxCandidateRunes || strings.Contains(name, "不") {
		return ""
	}
	switch name {
	case "我", "你", "他", "她", "它", "谁", "这个", "那个":
		return ""
	}
	return name
}

// resolveActor 把昵称落到当前可见参与者；落不到就保留昵称、ID 留空。
func resolveActor(name string, in ContextInput) actorSection {
	name = strings.TrimSpace(name)
	if name == "" {
		return actorSection{}
	}
	if strings.EqualFold(name, strings.TrimSpace(in.Event.FromName)) {
		return actorSection{ID: strings.TrimSpace(in.Event.FromID), Name: strings.TrimSpace(in.Event.FromName)}
	}
	for _, group := range [][]memory.Message{in.Memory.Focus, in.Memory.Recent} {
		for _, m := range group {
			if m.Dir == event.DirIn && strings.EqualFold(name, strings.TrimSpace(m.FromName)) {
				return actorSection{ID: strings.TrimSpace(m.FromID), Name: strings.TrimSpace(m.FromName)}
			}
		}
	}
	return actorSection{Name: name}
}

// sameAttribution 判断两个归属是否是同一个人。
func sameAttribution(a, b actorSection) bool {
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return a.Name != "" && strings.EqualFold(a.Name, b.Name)
}

// originalEvidenceFor 取正确归属人在纠正之前的最近发言，作为可核对原话。
func originalEvidenceFor(corr memory.Message, claimed actorSection, all []memory.Message) []correctionEvidenceSection {
	var out []correctionEvidenceSection
	for _, m := range all {
		if m.Dir != event.DirIn || m.EventID == corr.EventID || m.At.After(corr.At) {
			continue
		}
		if !sameAttribution(claimed, actorSection{ID: m.FromID, Name: m.FromName}) {
			continue
		}
		out = append(out, correctionEvidenceSection{
			EventID: m.EventID,
			At:      m.At,
			Speaker: actorSection{ID: m.FromID, Name: m.FromName},
			Text:    clipRunes(strings.TrimSpace(m.Text), maxCorrectionQuoteRunes),
		})
	}
	if len(out) > maxCorrectionEvidence {
		out = out[len(out)-maxCorrectionEvidence:]
	}
	return out
}

// supersededEvents 定位被纠正的具体旧回复。
//
// 证据优先级：
//  1. 纠正消息显式引用（reply_to）到的出向回复；
//  2. 与「被纠正命题」有词面重叠、且在文本上把该发言归给被否认人的出向回复。
//
// 找不到足够证据时返回 disputed=true，调用方不撤任何来源。
// 不再按「被否认人的全部历史」或「房间最近一条回复」批量猜测。
func supersededEvents(corr memory.Message, denied, claimed actorSection, evidence []correctionEvidenceSection, all []memory.Message) ([]string, bool) {
	if target := referencedOut(corr, all); target != nil {
		return []string{target.EventID}, false
	}
	anchors := propositionAnchors(corr, denied, claimed, evidence, all)
	if len(anchors) == 0 {
		return nil, true
	}
	candidates := attributionCandidates(corr, denied, all)
	if len(candidates) == 0 {
		return nil, true
	}
	names := []string{denied.Name, claimed.Name}
	best, bestOverlap := -1, 0
	// candidates 按时间升序；倒序扫描且只在严格更大时替换，平手保留最新一条。
	for i := len(candidates) - 1; i >= 0; i-- {
		if overlap := propositionOverlap(candidates[i].Text, anchors, names...); overlap > bestOverlap {
			best, bestOverlap = i, overlap
		}
	}
	if best < 0 {
		return nil, true
	}
	return []string{candidates[best].EventID}, false
}

// referencedOut 返回纠正消息显式引用到的出向回复；引用入向或不存在的消息时返回 nil。
func referencedOut(corr memory.Message, all []memory.Message) *memory.Message {
	ref := findMessage(corr.ReplyTo, all)
	if ref == nil || ref.EventID == corr.EventID || ref.Dir != event.DirOut || ref.At.After(corr.At) || strings.TrimSpace(ref.Text) == "" {
		return nil
	}
	return ref
}

// findMessage 按事件 ID 在当前可见窗口里查找消息。
func findMessage(id string, all []memory.Message) *memory.Message {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range all {
		if all[i].EventID == id {
			return &all[i]
		}
	}
	return nil
}

// propositionAnchors 收集能定位「被纠正的那句话」的命题词元：
// 引用到的入向原话、正确归属人的原始证据、纠正句里剩下的具体内容。
func propositionAnchors(corr memory.Message, denied, claimed actorSection, evidence []correctionEvidenceSection, all []memory.Message) map[string]bool {
	names := []string{denied.Name, claimed.Name}
	anchors := make(map[string]bool)
	add := func(text string) {
		for gram := range propositionGrams(text, names...) {
			anchors[gram] = true
		}
	}
	if ref := findMessage(corr.ReplyTo, all); ref != nil && ref.EventID != corr.EventID &&
		ref.Dir == event.DirIn && !ref.At.After(corr.At) {
		add(ref.Text)
	}
	for _, e := range evidence {
		add(e.Text)
	}
	add(corr.Text)
	return anchors
}

// propositionNoise 是会污染「具体命题」比对的纠正用语/代词/语气词。
// 按长词优先排列，避免先删短词把长词拆碎。
var propositionNoise = []string{
	"不是我说的", "不是我说", "我没说过", "我没讲过",
	"我记错了", "你记错了", "记错了", "搞错了", "弄错了",
	"你说错了", "你讲错了", "别乱说",
	"那句", "这句", "这个", "那个",
	"说过", "说的", "刚才", "到底", "究竟",
	"我们", "你们", "他们", "她们",
	"不是", "说", "讲", "问",
	"我", "你", "他", "她", "它",
	"是", "的", "了", "吗", "呢", "啊", "吧", "呀", "嘛", "哦", "噢",
}

// propositionStopGrams 是几乎不携带具体命题的二字组合。
var propositionStopGrams = map[string]bool{
	"什么": true, "怎么": true, "就是": true, "这个": true, "那个": true,
	"一个": true, "可以": true, "知道": true, "觉得": true, "现在": true,
	"刚才": true, "还是": true, "已经": true, "没有": true, "我们": true,
	"你们": true, "他们": true, "她们": true, "哈哈": true, "嘿嘿": true,
	"好的": true, "好吧": true, "真的": true, "其实": true, "因为": true,
	"所以": true, "但是": true, "然后": true, "如果": true,
}

// propositionGrams 把一句话压成内容二元组；过短或全是噪声时返回空集。
func propositionGrams(text string, names ...string) map[string]bool {
	cleaned := normalizeProposition(text, names...)
	runes := []rune(cleaned)
	if len(runes) < 2 {
		return nil
	}
	grams := make(map[string]bool, len(runes)-1)
	for i := 0; i+1 < len(runes); i++ {
		gram := string(runes[i : i+2])
		if propositionStopGrams[gram] {
			continue
		}
		grams[gram] = true
	}
	return grams
}

// normalizeProposition 去掉标点、纠正用语、参与人昵称与语气词。
func normalizeProposition(text string, names ...string) string {
	text = strings.ToLower(text)
	var b strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := b.String()
	for _, noise := range propositionNoise {
		cleaned = strings.ReplaceAll(cleaned, noise, "")
	}
	for _, name := range names {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			cleaned = strings.ReplaceAll(cleaned, name, "")
		}
	}
	return cleaned
}

// attributionCandidates 找出在纠正之前、文本上把发言归给被否认人的出向回复。
func attributionCandidates(corr memory.Message, denied actorSection, all []memory.Message) []memory.Message {
	inbound := make(map[string]memory.Message, len(all))
	for _, m := range all {
		if m.Dir == event.DirIn {
			inbound[m.EventID] = m
		}
	}
	var out []memory.Message
	for _, m := range all {
		if m.Dir != event.DirOut || m.EventID == corr.EventID || m.At.After(corr.At) || strings.TrimSpace(m.Text) == "" {
			continue
		}
		if attributesSpeechTo(m, denied, inbound) {
			out = append(out, m)
		}
	}
	return out
}

// speechAttributionMarkers 表示消息在转述/评价某人的发言。
var speechAttributionMarkers = []string{"说", "讲", "问", "提", "称", "表示", "回答", "回复"}

// attributesSpeechTo 判断一条出向消息是否把某句话归给 actor。
//
// 二手转述（「你说…」）必须同时满足：该回复确实在回应 actor 的入向消息；
// 否则「你说」指的是别人，不能算到 actor 头上。
func attributesSpeechTo(m memory.Message, actor actorSection, inbound map[string]memory.Message) bool {
	text := strings.TrimSpace(m.Text)
	if !containsAny(text, speechAttributionMarkers) {
		return false
	}
	if ref, ok := inbound[m.ReplyTo]; ok && sameAttribution(actor, actorSection{ID: ref.FromID, Name: ref.FromName}) {
		if containsAny(text, []string{"你说", "你讲", "你问", "你刚", "你又"}) {
			return true
		}
	}
	return actor.Name != "" && mentionsName(text, actor.Name)
}

// containsAny 判断文本是否包含任一标记。
func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// mentionsName 判断文本是否提到某个昵称；ASCII 昵称按词边界匹配，避免 B 命中 B站。
func mentionsName(text, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if isASCIIWord(name) {
		lowerText, lowerName := strings.ToLower(text), strings.ToLower(name)
		for start := 0; start+len(lowerName) <= len(lowerText); {
			pos := strings.Index(lowerText[start:], lowerName)
			if pos < 0 {
				return false
			}
			pos += start
			end := pos + len(lowerName)
			if (pos == 0 || !isWordByte(lowerText[pos-1])) && (end == len(lowerText) || !isWordByte(lowerText[end])) {
				return true
			}
			start = end
		}
		return false
	}
	return strings.Contains(text, name) || strings.Contains(strings.ToLower(text), strings.ToLower(name))
}

// isASCIIWord 判断昵称是否由 ASCII 字母/数字/下划线/连字符组成。
func isASCIIWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isWordByte(s[i]) {
			return false
		}
	}
	return true
}

// isWordByte 判断字节是否属于 ASCII 单词字符。
func isWordByte(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// propositionOverlap 统计候选回复与命题锚点共享的内容二元组数量。
func propositionOverlap(text string, anchors map[string]bool, names ...string) int {
	if len(anchors) == 0 {
		return 0
	}
	overlap := 0
	for gram := range propositionGrams(text, names...) {
		if anchors[gram] {
			overlap++
		}
	}
	return overlap
}

// retractedRecords 找由失效事件派生的摘要 ID。
func retractedRecords(records []memory.Record, superseded []string) []string {
	if len(superseded) == 0 {
		return nil
	}
	invalid := make(map[string]bool, len(superseded))
	for _, id := range superseded {
		invalid[id] = true
	}
	var out []string
	for _, r := range records {
		for _, id := range r.EventIDs {
			if invalid[id] {
				out = append(out, r.ID)
				break
			}
		}
	}
	return out
}

// retractedFacts 找来源事件已失效的共享事实 key。
func retractedFacts(facts []memory.Fact, superseded []string) []string {
	if len(superseded) == 0 {
		return nil
	}
	invalid := make(map[string]bool, len(superseded))
	for _, id := range superseded {
		invalid[id] = true
	}
	var out []string
	for _, f := range facts {
		if invalid[f.SourceID] {
			out = append(out, f.Key)
		}
	}
	return out
}

// filterRecords 从生成输入里移除已撤回的摘要；不改调用方切片。
func filterRecords(records []memory.Record, invalid map[string]bool) []memory.Record {
	if len(records) == 0 {
		return nil
	}
	if len(invalid) == 0 {
		return append([]memory.Record(nil), records...)
	}
	out := make([]memory.Record, 0, len(records))
	for _, r := range records {
		hit := false
		for _, id := range r.EventIDs {
			if invalid[id] {
				hit = true
				break
			}
		}
		if !hit {
			out = append(out, r)
		}
	}
	return out
}

// filterFacts 从生成输入里移除来源事件已失效的事实；不改调用方切片。
func filterFacts(facts []memory.Fact, invalid map[string]bool) []memory.Fact {
	if len(facts) == 0 {
		return nil
	}
	if len(invalid) == 0 {
		return append([]memory.Fact(nil), facts...)
	}
	out := make([]memory.Fact, 0, len(facts))
	for _, f := range facts {
		if !invalid[f.SourceID] {
			out = append(out, f)
		}
	}
	return out
}

// clipRunes 按 rune 截断，避免切坏 UTF-8。
func clipRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}
