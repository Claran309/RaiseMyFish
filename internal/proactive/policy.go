package proactive

// 本文件实现主动发言的「值得说才说」策略：
//
//   - 四态：comment / ask / react / stay_silent；
//   - 观察新颖性：画面与用户对话都没有变化时保持安静；
//   - 让路：用户正在追问或纠正时，主动发言暂停；
//   - 语义去重：同一观察/观点不换措辞重发，失败尝试不算说过。
//
// 所有判断都在本地完成，不额外调用模型；语义只是轻量字符指纹，
// 用来挡住「换个形容词继续说同一件事」，不冒充完整语义理解。

import (
	"sort"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// Action 是主动发言的显式动作；stay_silent 是正常结果，不是失败。
type Action string

const (
	ActionComment    Action = "comment"
	ActionAsk        Action = "ask"
	ActionReact      Action = "react"
	ActionStaySilent Action = "stay_silent"
)

// 保持安静的原因，写入日志字段 reason。
const (
	ReasonNoNewFact       = "no_new_fact"
	ReasonYieldFollowUp   = "yield_follow_up"
	ReasonYieldCorrection = "yield_correction"
	ReasonSemanticView    = "semantic_view"
)

const (
	// observationSimilarityThreshold：画面描述相似到这个程度，视为「同一画面」。
	observationSimilarityThreshold = 0.85
	// viewpointSimilarityThreshold：不同措辞但共享足够多的画面片段，视为同一观点。
	viewpointSimilarityThreshold = 0.65
	// textSemanticThreshold：已确认发言的语义近似阈值（字符二元组重合度）。
	textSemanticThreshold = 0.6
	// yieldWindow：只对最近这段时间内尚未回答的追问/纠正让路。
	yieldWindow = 2 * time.Minute
	// viewpointWindow：语义观点去重只看最近这段时间。
	viewpointWindow = 10 * time.Minute
	viewpointLimit  = 20
	attemptLimit    = 20
)

// observation 是一轮主动判断的触发证据；ID/时间会随 Result 与日志一起返回。
type observation struct {
	ID              string
	RoomID          string
	Source          string
	CapturedAt      time.Time
	Visual          string
	ConversationKey string
}

// conversationView 是从统一上下文里提取的用户侧对话状态。
// key 只取最新入向消息：鱼自己的出站消息或被动回复不算「新事实」。
type conversationView struct {
	key     string
	pending []memory.Message
}

// viewpointRecord 是已处理观察的语义指纹，用来挡住同一画面/观点换措辞重来。
// 它与「已确认发言」分开：失败尝试也会留下观察记录，但不代表已经说过。
type viewpointRecord struct {
	ObservationID string
	Visual        string
	At            time.Time
}

// attemptRecord 是失败尝试的审计记录，不参与「已经说过」的文本去重。
type attemptRecord struct {
	ObservationID string
	ObservedAt    time.Time
	Action        Action
	Text          string
	Outcome       string
	At            time.Time
}

// policyState 保存观察新颖性与语义去重所需的短窗口状态。
type policyState struct {
	mu         sync.Mutex
	last       *observation
	viewpoints []viewpointRecord
	attempts   []attemptRecord
}

func newPolicyState() *policyState { return &policyState{} }

func (s *policyState) lastObservation() (observation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return observation{}, false
	}
	return *s.last, true
}

// commitObservation 记录本轮已经完整判断过的观察（含保持安静），
// 生成失败不会调用这里，下一轮仍可重试。
func (s *policyState) commitObservation(obs observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := obs
	s.last = &copied
}

// viewpointSimilar 判断新画面是否只是最近已处理观察的另一种说法。
func (s *policyState) viewpointSimilar(obs observation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, previous := range s.viewpoints {
		if obs.CapturedAt.Sub(previous.At) > viewpointWindow {
			continue
		}
		if shingleOverlap(previous.Visual, obs.Visual) >= viewpointSimilarityThreshold {
			return true
		}
	}
	return false
}

func (s *policyState) addViewpoint(obs observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewpoints = append(s.viewpoints, viewpointRecord{
		ObservationID: obs.ID,
		Visual:        obs.Visual,
		At:            obs.CapturedAt,
	})
	if len(s.viewpoints) > viewpointLimit {
		s.viewpoints = s.viewpoints[len(s.viewpoints)-viewpointLimit:]
	}
}

func (s *policyState) recordAttempt(record attemptRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, record)
	if len(s.attempts) > attemptLimit {
		s.attempts = s.attempts[len(s.attempts)-attemptLimit:]
	}
}

// 语义去重用的字符二元组；标点与空白不参与，避免只改标点就绕过。
func shingles(text string) map[string]struct{} {
	runes := normalize(text)
	if len(runes) < 2 {
		return nil
	}
	set := make(map[string]struct{}, len(runes)-1)
	for i := 0; i+1 < len(runes); i++ {
		set[string(runes[i:i+2])] = struct{}{}
	}
	return set
}

// shingleOverlap 返回二元组交集相对较短文本的重合度（0~1）。
// 用重合度而不是 Jaccard，避免长描述因为长度差被稀释。
func shingleOverlap(a, b string) float64 {
	first, second := shingles(a), shingles(b)
	if len(first) == 0 || len(second) == 0 {
		return 0
	}
	common := 0
	for shingle := range first {
		if _, ok := second[shingle]; ok {
			common++
		}
	}
	smaller := min(len(first), len(second))
	return float64(common) / float64(smaller)
}

// conversationFrom 提取最新入向消息与尚未回答的追问/纠正。
//
// Recent 与 Focus 可能重复同一条消息；按 EventID 去重后统一按时间排序。
func conversationFrom(ctx memory.Context) conversationView {
	messages := dedupMessages(ctx.Recent, ctx.Focus)
	var latest *memory.Message
	var outbound []memory.Message
	for i := range messages {
		message := messages[i]
		if message.At.IsZero() {
			continue
		}
		if message.Dir == event.DirOut {
			outbound = append(outbound, message)
			continue
		}
		if message.Dir != event.DirIn {
			continue
		}
		if latest == nil || message.At.After(latest.At) ||
			(message.At.Equal(latest.At) && message.EventID > latest.EventID) {
			copied := message
			latest = &copied
		}
	}
	view := conversationView{}
	if latest != nil {
		view.key = latest.EventID + "@" + latest.At.UTC().Format(time.RFC3339Nano)
	}
	for _, message := range messages {
		if message.Dir != event.DirIn || message.At.IsZero() {
			continue
		}
		if answered(message, outbound) {
			continue
		}
		view.pending = append(view.pending, message)
	}
	return view
}

// dedupMessages 合并 Recent/Focus，按时间稳定排序并去掉重复 EventID。
func dedupMessages(groups ...[]memory.Message) []memory.Message {
	seen := make(map[string]struct{})
	var messages []memory.Message
	for _, group := range groups {
		for _, message := range group {
			key := message.EventID
			if key == "" {
				key = string(message.Channel) + "|" + message.Session + "|" + message.At.String() + "|" + message.Text
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			messages = append(messages, message)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].At.Equal(messages[j].At) {
			return messages[i].EventID < messages[j].EventID
		}
		return messages[i].At.Before(messages[j].At)
	})
	return messages
}

// answered 只认 ReplyTo 配对证据：出站写的是别的消息（或没有引用）时，
// 不能证明这条追问已经被回答，宁可让路也不能假设已处理。
func answered(inbound memory.Message, outbound []memory.Message) bool {
	for _, reply := range outbound {
		if reply.ReplyTo == inbound.EventID {
			return true
		}
	}
	return false
}

// yieldReason 判断用户是否正在追问/纠正；只让路给最近尚未回答的消息。
func (v conversationView) yieldReason(now time.Time) string {
	for _, message := range v.pending {
		if message.At.After(now) || now.Sub(message.At) > yieldWindow {
			continue
		}
		if isQuestionText(message.Text) {
			return ReasonYieldFollowUp
		}
		if isCorrectionText(message.Text) {
			return ReasonYieldCorrection
		}
	}
	return ""
}

func isQuestionText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "?？") {
		return true
	}
	for _, suffix := range []string{"吗", "么", "嘛"} {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	for _, cue := range []string{
		"怎么", "为什么", "为啥", "啥", "谁", "哪", "多少", "几点",
		"是不是", "能不能", "可不可以", "然后呢", "所以呢", "你还没",
		"没回答", "接着说", "继续", "追问",
	} {
		if strings.Contains(trimmed, cue) {
			return true
		}
	}
	return false
}

func isCorrectionText(text string) bool {
	for _, cue := range []string{
		"不对", "不是", "错了", "说错", "搞错", "纠正",
		"我刚才说的是", "我说的是", "你听错", "认错", "不是那样", "不是这个",
	} {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

// classifyAction 把已生成文本映射到具体动作：
// 提问→ask；画面有变化→react；否则是对对话的 comment。
func classifyAction(text string, visualChanged bool) Action {
	if isQuestionText(text) {
		return ActionAsk
	}
	if visualChanged {
		return ActionReact
	}
	return ActionComment
}
