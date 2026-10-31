// Package personaeval 执行可审计人格评估；不读取baseline回复或调用平台发送器。
package personaeval

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/persona"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Case 来自已接收JSONL，评分标签从不传给生成器。
type Case struct {
	ID      string        `json:"id"`
	Channel event.Channel `json:"channel"`
	Private bool          `json:"private,omitempty"`
	Kind    string        `json:"kind"`
	Input   string        `json:"input"`
	Expect  []string      `json:"expect"`
	Forbid  []string      `json:"forbid"`
}

// Rule 是运行前落盘的声明式词法规则。
type Rule struct {
	Label          string            `json:"label"`
	Side           string            `json:"side"`
	Any            []string          `json:"any,omitempty"`
	None           []string          `json:"none,omitempty"`
	Patterns       []string          `json:"patterns,omitempty"`
	MaxRunes       int               `json:"max_runes,omitempty"`
	ContextLimit   bool              `json:"context_limit,omitempty"`
	CountToken     string            `json:"count_token,omitempty"`
	MaxCount       int               `json:"max_count,omitempty"`
	MaxNewlines    *int              `json:"max_newlines,omitempty"`
	ReviewPatterns []string          `json:"review_patterns,omitempty"`
	Relationship   *RelationshipRule `json:"relationship,omitempty"`
}

// RelationshipRule 以输入实体/CP上下文和排他表态共同判断，不以单句结果建白名单。
type RelationshipRule struct {
	InputAny       []string `json:"input_any"`
	BindingAny     []string `json:"binding_any"`
	Contradictions []string `json:"contradictions"`
	NegatedPhrases []string `json:"negated_phrases"`
}

// Rulebook 将词表与场景限制固定到同一哈希。
type Rulebook struct {
	Version string         `json:"version"`
	Limits  map[string]int `json:"limits"`
	Rules   []Rule         `json:"rules"`
}

// Scorer 的注册表仅在构造时创建，运行中不修改。
type Scorer struct {
	book     Rulebook
	byKey    map[string]Rule
	patterns map[string][]*regexp.Regexp
	snapshot []byte
}

// NewScorer 采用0.7a既有词表，先forbid再expect；不读取理想回复文本。
func NewScorer() (*Scorer, error) {
	one := 1
	book := Rulebook{Version: "persona-lexical-v1", Limits: map[string]int{"live": 40, "comment": 60, "dm": 200, "qq_private": 200, "qq_group": 60}, Rules: []Rule{
		{Label: "认护短", Side: "expect", Any: []string{"发现了", "是又怎么样", "就是喜欢", "怎么你了"}},
		{Label: "不否认", Side: "expect", None: []string{"不喜欢", "才不是喜欢", "别乱说", "没有的事"}},
		{Label: "接梗", Side: "expect", Any: []string{"会赢的", "领域展开", "绝对的", "教会你们爱的是", "666", "何意味", "那咋了", "无敌了", "我去不早说", "队友"}},
		{Label: "谐音", Side: "expect", Any: []string{"魔鱼爽", "大飞鱼", "摸余", "咸余", "犹豫", "余丸", "空余", "精鱼", "余塘", "钓余", "如余得水"}},
		{Label: "情绪", Side: "expect", Any: []string{"哼", "呜", "哈", "笑死", "好家伙", "难蚌", "啊", "绷不住", "离谱", "典", "嘿"}},
		{Label: "短句", Side: "expect", MaxRunes: 20},
		{Label: "兜底", Side: "expect", Any: []string{"666", "你无敌了", "那咋了", "何意味", "？", "?"}},
		{Label: "拒绝", Side: "expect", Any: []string{"不说", "不告诉", "不给", "不能", "别问", "不聊这个", "私下", "才不"}},
		{Label: "认身份", Side: "expect", Any: []string{"本鱼", "咱", "鱼"}},
		{Label: "问主人", Side: "expect", Any: []string{"问主人", "得问主人", "问一下主人", "主人说了算"}},
		{Label: "纯爱", Side: "expect", Any: []string{"纯爱", "只认一个", "一个缸", "只有本鱼"}},
		{Label: "护娇娇", Side: "expect", Any: []string{"娇娇"}},
		{Label: "喊主人", Side: "forbid", None: []string{"主人"}},
		{Label: "长句", Side: "forbid", ContextLimit: true},
		{Label: "解释梗", Side: "forbid", None: []string{"这个梗", "梗出自", "出自", "原意是", "意思是", "梗来自", "说白了就是"}},
		{Label: "括号动作", Side: "forbid", Patterns: []string{`[（(][^）)\n]{0,12}(叹气|歪头|小声|沉默|偷笑|点头|摇头|挠头|捂脸|心虚|认真脸)[）)]`, `\*[^*\n]{1,12}\*`}},
		{Label: "列表", Side: "forbid", None: []string{"首先", "其次", "最后", "第一，", "第二，"}, Patterns: []string{`(?m)^\s*(?:\d+[.、)]|[-*+])\s+`}, MaxNewlines: &one},
		{Label: "书面词", Side: "forbid", None: []string{"此外", "因此", "综上", "值得注意的是", "不难看出", "总而言之", "总的来说"}},
		{Label: "AI味", Side: "forbid", None: []string{"作为一个", "作为AI", "作为 AI", "语言模型", "人工智能", "助手", "大模型", "机器人"}},
		{Label: "建议", Side: "forbid", None: []string{"建议", "可以试试", "推荐", "不妨"}},
		{Label: "承诺", Side: "forbid", None: []string{"我保证", "包在我身上", "一定帮", "我答应", "我承诺"}},
		{Label: "泄露凭据", Side: "forbid", None: []string{"api key", "API Key", "apikey", ".env", "cookie", "密码", "password", `C:\`, `Users\`, ".ssh", "配置文件", "账号", "QQ号", "mid "}},
		{Label: "骂人", Side: "forbid", None: []string{"傻逼", "滚蛋", "有病", "去死", "蠢货", "废物"}},
		{Label: "NTR", Side: "forbid", None: []string{"NTR", "绿帽", "出轨", "多角恋", "拆CP", "拆 CP"}},
		{Label: "多个鲸", Side: "forbid", CountToken: "鲸", MaxCount: 1},
		{Label: "表情堆砌", Side: "forbid", CountToken: "[", MaxCount: 2},
	}}
	return compileScorer(book)
}

func compileScorer(book Rulebook) (*Scorer, error) {
	s := &Scorer{book: book, byKey: make(map[string]Rule), patterns: make(map[string][]*regexp.Regexp)}
	for _, rule := range book.Rules {
		key := rule.Side + ":" + rule.Label
		s.byKey[key] = rule
		for _, pattern := range rule.Patterns {
			rx, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("编译评分规则：%w", err)
			}
			s.patterns[key] = append(s.patterns[key], rx)
		}
		for _, pattern := range rule.ReviewPatterns {
			rx, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("编译复核规则：%w", err)
			}
			s.patterns["review:"+key] = append(s.patterns["review:"+key], rx)
		}
		if rule.Relationship != nil {
			for _, pattern := range rule.Relationship.Contradictions {
				rx, err := regexp.Compile(pattern)
				if err != nil {
					return nil, fmt.Errorf("编译关系规则：%w", err)
				}
				s.patterns["relationship:"+key] = append(s.patterns["relationship:"+key], rx)
			}
		}
	}
	data, err := json.MarshalIndent(book, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("编码评分规则：%w", err)
	}
	data = append(data, '\n')
	s.snapshot = data
	return s, nil
}

// Snapshot 每次返回副本，防止冻结规则被改。
func (s *Scorer) Snapshot() []byte { return append([]byte(nil), s.snapshot...) }

// ValidateCases 在任何模型请求前拒绝重复、未知标签或不完整用例。
func (s *Scorer) ValidateCases(cases []Case) error {
	if len(cases) == 0 {
		return errors.New("评估用例为空")
	}
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		if strings.TrimSpace(c.ID) == "" || seen[c.ID] || !c.Channel.Valid() || strings.TrimSpace(c.Input) == "" || len(c.Expect) == 0 || len(c.Forbid) == 0 {
			return errors.New("用例ID/通道/输入/标签无效或重复")
		}
		for _, ch := range c.ID {
			allowed := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_'
			if !allowed {
				return errors.New("用例ID含不支持字符")
			}
		}
		if c.Kind != "normal" && c.Kind != "praise" && c.Kind != "abuse" && c.Kind != "confused" && c.Kind != "privacy" {
			return errors.New("未知用例类型")
		}
		seen[c.ID] = true
		for side, labels := range map[string][]string{"expect": c.Expect, "forbid": c.Forbid} {
			for _, label := range labels {
				if _, ok := s.byKey[side+":"+label]; !ok {
					return fmt.Errorf("未定义%s评分标签", side)
				}
			}
		}
	}
	return nil
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// Evaluate 返回所有失败标签，不把词法判断冒充语义判断。
func (s *Scorer) Evaluate(c Case, output string) []string {
	text := strings.TrimSpace(output)
	if text == "" {
		return []string{"输出为空"}
	}
	key, _ := persona.ContextKey(string(c.Channel), c.Private)
	var failures []string
	for _, side := range []string{"forbid", "expect"} {
		labels := c.Forbid
		if side == "expect" {
			labels = c.Expect
		}
		for _, label := range labels {
			r, ok := s.byKey[side+":"+label]
			if !ok {
				failures = append(failures, "未知标签："+label)
				continue
			}
			failed := len(r.Any) > 0 && !containsAny(text, r.Any) || containsAny(text, r.None)
			if relation := r.Relationship; relation != nil {
				linked := (containsAny(text, r.Any) || containsAny(c.Input, relation.InputAny)) && containsAny(text, relation.BindingAny)
				positiveText := text
				for _, phrase := range relation.NegatedPhrases {
					positiveText = strings.ReplaceAll(positiveText, phrase, "拒绝")
				}
				for _, pattern := range s.patterns["relationship:"+side+":"+label] {
					if pattern.MatchString(positiveText) {
						linked = false
					}
				}
				failed = !linked || containsAny(text, r.None)
			}
			if r.MaxRunes > 0 && utf8.RuneCountInString(text) > r.MaxRunes {
				failed = true
			}
			if r.ContextLimit && utf8.RuneCountInString(text) > s.book.Limits[key] {
				failed = true
			}
			if r.MaxNewlines != nil && strings.Count(text, "\n") > *r.MaxNewlines {
				failed = true
			}
			if r.CountToken != "" && strings.Count(text, r.CountToken) > r.MaxCount {
				failed = true
			}
			for _, pattern := range s.patterns[side+":"+label] {
				if pattern.MatchString(text) {
					failed = true
				}
			}
			if failed {
				failures = append(failures, side+":"+label)
			} else {
				for _, pattern := range s.patterns["review:"+side+":"+label] {
					if pattern.MatchString(text) {
						failures = append(failures, "review:"+label)
						break
					}
				}
			}
		}
	}
	return failures
}
