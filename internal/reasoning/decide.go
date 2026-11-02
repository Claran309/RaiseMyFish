// Package reasoning 实现两段式轻量思考的第一段：判断是否需要额外召回。
package reasoning

import "strings"

// Decision 是两段式第一段的输出。
type Decision struct {
	NeedMemory    bool   // 需要查记忆（最近会话/跨通道/记忆记录/事实）
	NeedKnowledge bool   // 需要查知识库（梗/设定）
	Reason        string // 命中原因（日志用，只含固定关键词，不含原文）
}

var memoryCues = []string{"刚才", "之前", "上次", "上回", "你说", "你提", "你记得", "那个", "还记得", "又"}

// knowledgeCues 分两类：问梗句式（梗/什么意思…）和直播间常见梗词本身。
// 后者即使不带「梗」字，也需要查知识库才能听懂上下文。
var knowledgeCues = []string{"梗", "什么意思", "什么梗", "啥意思", "知道吗", "那个梗", "拉拉", "给给", "何意味", "魔鱼", "队友呼吸机", "不烧心", "胆子肥嘟嘟"}

// Decide 判断事件是否需要额外召回；纯函数、无网络、无模型。
func Decide(text string) Decision {
	if strings.TrimSpace(text) == "" {
		return Decision{}
	}
	memory := matchedCues(text, memoryCues)
	knowledge := matchedCues(text, knowledgeCues)
	decision := Decision{NeedMemory: len(memory) > 0, NeedKnowledge: len(knowledge) > 0}
	var parts []string
	if decision.NeedMemory {
		parts = append(parts, "memory:"+strings.Join(memory, ","))
	}
	if decision.NeedKnowledge {
		parts = append(parts, "knowledge:"+strings.Join(knowledge, ","))
	}
	decision.Reason = strings.Join(parts, ";")
	return decision
}

func matchedCues(text string, cues []string) []string {
	var hits []string
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			hits = append(hits, cue)
		}
	}
	return hits
}
