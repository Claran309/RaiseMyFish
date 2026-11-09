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
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

const (
	maxCorrections          = 2
	maxCorrectionTextRunes  = 120
	maxCorrectionEvidence   = 2
	maxCorrectionQuoteRunes = 100
	maxSupersededEvents     = 4
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
	Note               string                      `json:"note,omitempty"`
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
	entry.SupersededEventIDs = supersededEvents(corr, denied, all)
	if entry.Claimed != nil {
		entry.Note = "已纠正：更正人否认该说法，正确归属是「" + entry.Claimed.Name + "」；被纠正的旧回复与派生摘要已失效，回答先承认纠正，再按正确归属说。"
	} else {
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

// supersededEvents 找被纠正的旧回复：
//   - 回复过被否认发言的出向事件（reply_to 证据）；
//   - 纠正前最近一条已发出的出向事件（通常就是错误归属的那句）。
func supersededEvents(corr memory.Message, denied actorSection, all []memory.Message) []string {
	deniedIDs := make(map[string]bool)
	for _, m := range all {
		if m.Dir == event.DirIn && !m.At.After(corr.At) && sameAttribution(denied, actorSection{ID: m.FromID, Name: m.FromName}) {
			deniedIDs[m.EventID] = true
		}
	}
	var out []string
	seen := make(map[string]bool, maxSupersededEvents)
	add := func(id string) {
		if id == "" || seen[id] || len(out) >= maxSupersededEvents {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, m := range all {
		if m.Dir != event.DirOut || m.At.After(corr.At) {
			continue
		}
		if m.ReplyTo != "" && deniedIDs[m.ReplyTo] {
			add(m.EventID)
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		m := all[i]
		if m.Dir == event.DirOut && !m.At.After(corr.At) && strings.TrimSpace(m.Text) != "" {
			add(m.EventID)
			break
		}
	}
	return out
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
