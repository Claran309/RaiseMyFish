package main

// 本文件实现统一上下文快照：普通 Brain、ReAct、主动弹幕共用同一份资料。
//
// 边界：
//   - 权限与私密过滤在 repository 的查询层执行，这里不写 SQL、不自行越界拼装；
//   - 召回失败只降级为空快照并记日志，不阻塞回复；
//   - 「档案读取失败」与「陌生人」必须分开：前者注入明确状态，不能伪装成没有档案。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// viewerUnavailableText 是档案读取失败的显式标记。
//
// 陌生人（ErrNotFound）不注入任何片段；真实故障如果也返回空，模型会把
// 「查不到」当成「不认识」，所以这里给一条状态，让模型知道本轮无法确认身份。
const viewerUnavailableText = "【对方档案读取失败，本轮无法确认其身份】"

// 主动注入的窗口与长度：读取量大于展示量，先取回再按优先级挑选。
const (
	// viewerMemoryQueryLimit 是单次读取相关记忆的上限。
	viewerMemoryQueryLimit = 20
	// viewerMemoryRelatedMax 是「跟他相关的记忆」展示条数。
	viewerMemoryRelatedMax = 6
	// viewerMemoryRecentMax 是「最近 7 天聊过的」展示条数。
	viewerMemoryRecentMax = 4
	// viewerMemoryRecentDays 是近期对话的时间窗。
	viewerMemoryRecentDays = 7
	// viewerMemoryLineRunes 限制单条摘要长度，避免一条长记录挤掉其他记忆。
	viewerMemoryLineRunes = 56
	// viewerTextMaxRunes 是 ContextInput.Viewer 整段预算（派发单：≤400 字）。
	viewerTextMaxRunes = 400
	// viewerProfileRunes 是当前观众档案段上限，给记忆与乙档案留预算。
	viewerProfileRunes = 220
	// viewerRosterMax/viewerRosterQuery 控制房间活跃名单的展示与读取条数。
	viewerRosterMax   = 5
	viewerRosterQuery = 8
)

// profileFinder 按昵称检索观众档案；viewer.Searcher 满足该接口。
//
// 只用于「甲问乙」且乙只在 @/回复目标里出现（没有可信 UID）时按昵称定位；
// 有 UID 时直接按 UID 取档案，不经过检索。
type profileFinder interface {
	Find(ctx context.Context, query string, limit int) (viewer.Result, error)
}

// injectionMaxItems 是单轮最多拼接的看板注入条数；新的优先。
const injectionMaxItems = 5

// contextBuilder 组装一次生成所需的全部资料。
type contextBuilder struct {
	pack       persona.Pack
	memory     memory.Reader
	knowledge  knowledge.Reader
	viewers    store.ViewerRepo
	soul       *soulCache
	state      func(time.Time) string
	finder     profileFinder
	injections store.InjectionReader
	log        *zap.Logger
}

// newContextBuilder 构造快照装配器；可选依赖为 nil 时对应区块留空。
func newContextBuilder(pack persona.Pack, mem memory.Reader, know knowledge.Reader, viewers store.ViewerRepo, soul *soulCache, state func(time.Time) string, log *zap.Logger) *contextBuilder {
	if log == nil {
		log = zap.NewNop()
	}
	return &contextBuilder{
		pack:      pack,
		memory:    mem,
		knowledge: know,
		viewers:   viewers,
		soul:      soul,
		state:     state,
		log:       log,
	}
}

// BuildContext 组装统一快照。
//
// 记忆（同会话最近消息 + 跨通道公开消息 + 摘要 + 共享事实）默认注入；
// visual 由调用方提供（主动弹幕识图，被动为空）；
// withKnowledge 由调用方按意图决定，避免每条短句都触发语义检索。
func (b *contextBuilder) BuildContext(ctx context.Context, ev event.Event, visual string, withKnowledge bool) brain.ContextInput {
	scope := memory.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private}
	memCtx, shared := recallMemory(ctx, b.memory, scope, ev.Text, ev.FromID, b.log)

	var knowCtx *knowledge.Context
	if withKnowledge {
		knowCtx = recallKnowledge(ctx, b.knowledge, ev, b.log)
	}

	stateText := ""
	if b.state != nil {
		stateText = b.state(time.Now())
	}

	input := brain.ContextInput{
		Event:     ev,
		Persona:   b.pack,
		Memory:    memCtx,
		Shared:    shared,
		Knowledge: knowCtx,
		State:     stateText,
		Visual:    strings.TrimSpace(visual),
		Injection: b.injectionText(ctx),
		Soul:      b.soul.load(ctx),
	}
	// Viewer 段依赖问题主体（甲问乙）与记忆快照，必须最后组装。
	input.Viewer = b.viewerText(ctx, input)
	return input
}

// injectionText 读取当前有效的看板注入，按新旧拼接并压到 prompt 预算内。
//
// 注入是增强能力：读库失败只记日志并返回空串，绝不阻塞回复。
func (b *contextBuilder) injectionText(ctx context.Context) string {
	if b.injections == nil {
		return ""
	}
	rows, err := b.injections.ActiveInjections(ctx, time.Now())
	if err != nil {
		b.log.Warn("看板注入读取失败，本轮不注入",
			zap.String("action", "injection_context"),
			zap.Error(err),
		)
		return ""
	}
	parts := make([]string, 0, injectionMaxItems)
	used := 0
	for _, row := range rows {
		content := strings.TrimSpace(row.Content)
		if content == "" {
			continue
		}
		cost := utf8.RuneCountInString(content)
		if len(parts) > 0 {
			cost++
		}
		// 与 brain.maxInjectionRunes 同口径；放不下的旧条目直接省略。
		if len(parts) >= injectionMaxItems || used+cost > store.MaxInjectionRunes {
			break
		}
		parts = append(parts, content)
		used += cost
	}
	return strings.Join(parts, "\n")
}

// viewerText 组装 Viewer 段：当前观众档案 > 历史记忆 > 时间窗 > 乙档案 > 名单，
// 总长 ≤ viewerTextMaxRunes（超出按低优先级先裁）。
//
// 陌生人（无档案）不注入其档案与相关记忆，但「甲问乙」的乙档案与房间名单仍可补充；
// 档案读取失败注入显式状态，不能伪装成没有档案。
func (b *contextBuilder) viewerText(ctx context.Context, in brain.ContextInput) string {
	ev := in.Event
	if b.viewers == nil || strings.TrimSpace(ev.FromID) == "" {
		return ""
	}
	profileText := ""
	var segments []viewerSegment
	profile, err := b.viewers.GetProfile(ctx, ev.FromID)
	switch {
	case err == nil:
		profileText = formatViewerProfile(profile)
		segments = append(segments, b.viewerMemorySegments(ctx, ev)...)
	case errors.Is(err, store.ErrNotFound):
		// 陌生人：本人没有档案与相关记忆可注入。
	default:
		profileText = viewerUnavailableText
		b.log.Warn("观众档案读取失败，本轮不注入档案",
			zap.String("action", "viewer_profile"),
			zap.String("event_id", ev.ID),
			zap.String("ch", string(ev.Channel)),
			zap.Error(err),
		)
	}

	exclude := map[string]bool{ev.FromID: true}
	if target, ok := b.targetViewerSegment(ctx, in); ok {
		segments = append(segments, target.segment)
		if target.uid != "" {
			exclude[target.uid] = true
		}
	}
	if roster, ok := b.rosterSegment(ctx, ev, exclude); ok {
		segments = append(segments, roster)
	}
	return composeViewerText(viewerTextMaxRunes, profileText, segments)
}

// viewerSegment 是 Viewer 段里一组同优先级资料。
//
// raw 非空时整段填充（放不下可按剩余预算截断）；否则按行贪心填充。
type viewerSegment struct {
	header string
	lines  []string
	raw    string
}

// composeViewerText 按 segments 顺序（优先级从高到低）填充预算。
//
// 当前观众档案最高优先，整体保留并按上限截断；其余段逐行加入，
// 放不下的行、段直接丢弃——低优先级先被裁掉。
func composeViewerText(max int, profileText string, segments []viewerSegment) string {
	if max <= 0 {
		return ""
	}
	var parts []string
	used := 0
	separator := func() int {
		if len(parts) == 0 {
			return 0
		}
		return 1
	}

	profileText = strings.TrimSpace(truncateRunes(strings.TrimSpace(profileText), viewerProfileRunes))
	if profileText != "" {
		profileText = truncateRunes(profileText, max)
		parts = append(parts, profileText)
		used = utf8.RuneCountInString(profileText)
	}

	for _, segment := range segments {
		room := max - used - separator()
		if room <= 0 {
			break
		}
		if segment.raw != "" {
			raw := strings.TrimSpace(segment.raw)
			if raw == "" {
				continue
			}
			cost := utf8.RuneCountInString(raw)
			if cost <= room {
				parts = append(parts, raw)
				used += separator() + cost
			} else if room >= 40 {
				// 低优先级整段放不下时按剩余预算截断，至少保留姓名与统计。
				parts = append(parts, truncateRunes(raw, room))
				used = max
			}
			continue
		}
		block := fitViewerLines(segment.header, segment.lines, room)
		if block == "" {
			continue
		}
		parts = append(parts, block)
		used += separator() + utf8.RuneCountInString(block)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// fitViewerLines 在 room 预算内按行填充一段；整段放不下一行时返回空串。
func fitViewerLines(header string, lines []string, room int) string {
	cost := 0
	if header != "" {
		cost = utf8.RuneCountInString(header) + 1
	}
	if cost >= room {
		return ""
	}
	accepted := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lineCost := utf8.RuneCountInString(line) + 1
		if cost+lineCost > room {
			break
		}
		accepted = append(accepted, line)
		cost += lineCost
	}
	if len(accepted) == 0 {
		return ""
	}
	body := strings.Join(accepted, "\n")
	if header == "" {
		return body
	}
	return header + "\n" + body
}

// viewerMemorySegments 返回「跟他相关的记忆」与「最近 7 天聊过的」两段。
//
// 两段按 ID 去重（相关段优先），条数与单条长度都有上限；
// 时间窗按 valid_from 判定，支撑「昨天聊天/昨晚顶号」这类追问。
func (b *contextBuilder) viewerMemorySegments(ctx context.Context, ev event.Event) []viewerSegment {
	source, ok := b.memory.(memory.ViewerRecordSource)
	if !ok {
		return nil
	}
	records, err := source.ViewerRecords(ctx, memory.ViewerRecordQuery{
		Scope: memory.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private},
		UID:   ev.FromID,
		Limit: viewerMemoryQueryLimit,
	})
	if err != nil {
		b.log.Warn("观众相关记忆查询失败，本轮不注入记忆",
			zap.String("action", "viewer_memory"),
			zap.String("event_id", ev.ID),
			zap.String("ch", string(ev.Channel)),
			zap.Error(err),
		)
		return nil
	}
	now := ev.At
	if now.IsZero() {
		now = time.Now()
	}

	// 相关记忆按重要度挑选；同重要度时新的优先。
	ranked := append([]memory.Record(nil), records...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Importance != ranked[j].Importance {
			return ranked[i].Importance > ranked[j].Importance
		}
		return ranked[i].ValidFrom.After(ranked[j].ValidFrom)
	})
	seen := make(map[string]bool, len(records))
	related := make([]string, 0, viewerMemoryRelatedMax)
	for _, record := range ranked {
		if len(related) >= viewerMemoryRelatedMax {
			break
		}
		line := formatViewerMemoryLine(record)
		if line == "" || strings.TrimSpace(record.ID) == "" || seen[record.ID] {
			continue
		}
		related = append(related, line)
		seen[record.ID] = true
	}

	cutoff := now.Add(-viewerMemoryRecentDays * 24 * time.Hour)
	recent := make([]string, 0, viewerMemoryRecentMax)
	for _, record := range records {
		if len(recent) >= viewerMemoryRecentMax {
			break
		}
		if record.ValidFrom.Before(cutoff) {
			continue
		}
		line := formatViewerMemoryLine(record)
		if line == "" || strings.TrimSpace(record.ID) == "" || seen[record.ID] {
			continue
		}
		recent = append(recent, line)
		seen[record.ID] = true
	}

	var segments []viewerSegment
	if len(related) > 0 {
		segments = append(segments, viewerSegment{
			header: "## 跟他相关的记忆（主动注入，按重要度）",
			lines:  related,
		})
	}
	if len(recent) > 0 {
		segments = append(segments, viewerSegment{
			header: "## 最近 7 天聊过的（主动注入，新的在前）",
			lines:  recent,
		})
	}
	return segments
}

// formatViewerMemoryLine 渲染一条记忆摘要。
func formatViewerMemoryLine(record memory.Record) string {
	text := strings.TrimSpace(record.Summary)
	if text == "" {
		return ""
	}
	return "- " + record.ValidFrom.Format("01-02") + "：" + truncateRunes(text, viewerMemoryLineRunes)
}

// viewerTarget 是「甲问乙」里乙的档案段与已确认的 UID（供名单去重）。
type viewerTarget struct {
	segment viewerSegment
	uid     string
}

// targetViewerSegment 读取问题主体（乙）的档案。
//
// 只处理 room_candidate：主体有可信 UID 时直接按 UID 取档案；
// 只有昵称（@/回复目标）时按昵称检索，且只接受高确定的 found。
// 主体就是当前发言人时不重复注入。
func (b *contextBuilder) targetViewerSegment(ctx context.Context, in brain.ContextInput) (viewerTarget, bool) {
	target := brain.ResolveQuestionTarget(in)
	if target == nil {
		return viewerTarget{}, false
	}
	// 问自己/问发言人不需要另取档案；unresolved（如「仙主是谁」）也尝试别名/近似召回，
	// 让「仙主→终南仙主」这类简称能命中，而不是直接当作不认识。
	if target.Kind == brain.SubjectSelf || target.Kind == brain.SubjectSpeaker {
		return viewerTarget{}, false
	}
	uid := strings.TrimSpace(target.UID)
	name := strings.TrimSpace(target.Name)
	if uid == "" && b.finder != nil && name != "" {
		result, err := b.finder.Find(ctx, name, 1)
		if err != nil {
			b.log.Warn("被问到的观众检索失败，本轮不注入其档案",
				zap.String("action", "viewer_target_search"),
				zap.String("event_id", in.Event.ID),
				zap.Error(err),
			)
			return viewerTarget{}, false
		}
		if result.Status != viewer.StatusFound || len(result.Candidates) == 0 {
			return viewerTarget{}, false
		}
		candidate := result.Candidates[0]
		uid = strings.TrimSpace(candidate.Profile.UID)
		if uid == "" {
			return viewerTarget{}, false
		}
		if name == "" {
			name = strings.TrimSpace(candidate.Profile.Name)
		}
	}
	if uid == "" || uid == strings.TrimSpace(in.Event.FromID) {
		return viewerTarget{}, false
	}
	profile, err := b.viewers.GetProfile(ctx, uid)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.log.Warn("被问到的观众档案读取失败，本轮不注入其档案",
				zap.String("action", "viewer_target_profile"),
				zap.String("event_id", in.Event.ID),
				zap.Error(err),
			)
		}
		return viewerTarget{}, false
	}
	if name == "" {
		name = profile.Name
	}
	return viewerTarget{segment: viewerSegment{raw: formatTargetProfile(name, profile)}, uid: uid}, true
}

// rosterSegment 组装房间最近活跃名单（仅直播通道，预算允许时展示）。
//
// 名单只是公开昵称与发言次数，不含侧写；当前发言人与被问到的观众不重复列出。
func (b *contextBuilder) rosterSegment(ctx context.Context, ev event.Event, exclude map[string]bool) (viewerSegment, bool) {
	if ev.Channel != event.ChLive {
		return viewerSegment{}, false
	}
	profiles, err := b.viewers.TopViewers(ctx, viewerRosterQuery)
	if err != nil {
		b.log.Warn("房间活跃名单读取失败，本轮不注入名单",
			zap.String("action", "viewer_roster"),
			zap.String("event_id", ev.ID),
			zap.Error(err),
		)
		return viewerSegment{}, false
	}
	lines := make([]string, 0, viewerRosterMax)
	for _, profile := range profiles {
		if len(lines) >= viewerRosterMax {
			break
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" || exclude[profile.UID] {
			continue
		}
		lines = append(lines, "- "+truncateRunes(name, 20)+fmt.Sprintf("（说过 %d 次）", profile.Hits))
	}
	if len(lines) == 0 {
		return viewerSegment{}, false
	}
	return viewerSegment{header: "## 房间最近活跃（按发言次数）", lines: lines}, true
}

// buildPassiveInput 是被动回复的统一快照入口。
//
// 近期记忆无条件注入（C04）：连续对话不能靠「刚才/记得」这类关键词才召回；
// 知识仍按意图补充，避免短句也触发语义检索。
func buildPassiveInput(ctx context.Context, builder *contextBuilder, ev event.Event) (brain.ContextInput, reasoning.Decision) {
	decision := reasoning.Decide(ev.Text)
	return builder.BuildContext(ctx, ev, "", decision.NeedKnowledge), decision
}
