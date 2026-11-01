package memory

// 本文件测 0.9a 的四条提炼规则与过滤边界（plans/phase-0.9-workbuddy-dispatch.md §验收标准）。
// 全部用例都是纯函数测试：不读库、不连 PG、不调用模型。

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// assertLimits 统一校验候选的长度与评分区间，避免每条用例重复写。
func assertLimits(t *testing.T, c Candidate) {
	t.Helper()

	if n := utf8.RuneCountInString(c.Title); n == 0 || n > maxTitleRunes {
		t.Errorf("Title 长度 %d，期望 1..%d：%q", n, maxTitleRunes, c.Title)
	}
	if n := utf8.RuneCountInString(c.Summary); n == 0 || n > maxSummaryRunes {
		t.Errorf("Summary 长度 %d，期望 1..%d：%q", n, maxSummaryRunes, c.Summary)
	}
	if !utf8.ValidString(c.Title) || !utf8.ValidString(c.Summary) {
		t.Errorf("标题/摘要不是合法 UTF-8：%q / %q", c.Title, c.Summary)
	}
	// 切坏的多字节字符会变成 U+FFFD；按 rune 截断不应出现
	if strings.ContainsRune(c.Title, utf8.RuneError) || strings.ContainsRune(c.Summary, utf8.RuneError) {
		t.Errorf("出现替换符 U+FFFD，说明截断切坏了字符：%q", c.Title)
	}
	if c.Importance < 0 || c.Importance > 1 {
		t.Errorf("Importance = %v，越界 [0,1]", c.Importance)
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		t.Errorf("Confidence = %v，越界 [0,1]", c.Confidence)
	}
}

// kindsOf 提取候选种类序列，用于断言顺序。
func kindsOf(cands []Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Kind)
	}
	return out
}

// TestExtractPromise 承诺规则：四个触发词都是正例，普通叙述是反例。
func TestExtractPromise(t *testing.T) {
	positives := []string{
		"我答应明天帮你带话",
		"我保证不会说出去",
		"一定给你留一碗白饭",
		"这事包在我身上",
	}
	for _, text := range positives {
		t.Run("正例/"+text, func(t *testing.T) {
			got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{})
			if len(got) != 1 {
				t.Fatalf("候选数 = %d，期望 1：%+v", len(got), got)
			}
			c := got[0]
			if c.Kind != KindPromise {
				t.Errorf("Kind = %q，期望 %q", c.Kind, KindPromise)
			}
			if c.Importance != promiseImportance {
				t.Errorf("Importance = %v，期望 %v", c.Importance, promiseImportance)
			}
			if c.Confidence != promiseConfidence {
				t.Errorf("Confidence = %v，期望 %v", c.Confidence, promiseConfidence)
			}
			assertLimits(t, c)
		})
	}

	// 反例：普通叙述不命中
	for _, text := range []string{"今天天气不错", "随便聊聊", "答应给的白饭呢"} {
		t.Run("反例/"+text, func(t *testing.T) {
			if got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{}); len(got) != 0 {
				t.Errorf("期望不提炼，实际 %+v", got)
			}
		})
	}
}

// TestExtractOwnerByText 文本提到主人 → preference。
func TestExtractOwnerByText(t *testing.T) {
	got := Extract(Event{Text: "主人今天又充了 token", FromName: "路人"}, ExtractOptions{})
	if len(got) != 1 {
		t.Fatalf("候选数 = %d，期望 1：%+v", len(got), got)
	}
	c := got[0]
	if c.Kind != KindPreference {
		t.Errorf("Kind = %q，期望 %q", c.Kind, KindPreference)
	}
	if c.Importance != preferenceImportance {
		t.Errorf("Importance = %v，期望 %v", c.Importance, preferenceImportance)
	}
	assertLimits(t, c)

	// 反例：同一条文本，把「主人」换成别的词就不再命中
	if got := Extract(Event{Text: "今天又充了 token", FromName: "路人"}, ExtractOptions{}); len(got) != 0 {
		t.Errorf("反例被误提炼：%+v", got)
	}
}

// TestExtractOwnerByName 发言人昵称等于 OwnerName → preference。
func TestExtractOwnerByName(t *testing.T) {
	opts := ExtractOptions{OwnerName: "Claran"}

	got := Extract(Event{Text: "晚安", FromName: "Claran"}, opts)
	if len(got) != 1 || got[0].Kind != KindPreference {
		t.Fatalf("期望 1 条 preference，实际 %+v", got)
	}
	assertLimits(t, got[0])

	// 反例 1：昵称不完全相等
	if got := Extract(Event{Text: "晚安", FromName: "Claran2"}, opts); len(got) != 0 {
		t.Errorf("昵称不等却命中了：%+v", got)
	}
	// 反例 2：OwnerName 为空时不做昵称判定，避免空串匹配所有人
	if got := Extract(Event{Text: "晚安", FromName: "Claran"}, ExtractOptions{}); len(got) != 0 {
		t.Errorf("OwnerName 为空却按昵称命中了：%+v", got)
	}
}

// TestExtractFirstSeen 首次出现：依赖调用方传入的 IsFirstNick 与昵称。
func TestExtractFirstSeen(t *testing.T) {
	got := Extract(Event{Text: "第一次来", FromName: "小鱼"}, ExtractOptions{IsFirstNick: true})
	if len(got) != 1 {
		t.Fatalf("候选数 = %d，期望 1：%+v", len(got), got)
	}
	c := got[0]
	if c.Kind != KindFirstSeen {
		t.Errorf("Kind = %q，期望 %q", c.Kind, KindFirstSeen)
	}
	if c.Importance != firstSeenImportance {
		t.Errorf("Importance = %v，期望 %v", c.Importance, firstSeenImportance)
	}
	assertLimits(t, c)
	// 摘要要带上昵称，worker 落库时才认得出是谁
	if !strings.Contains(c.Summary, "小鱼") {
		t.Errorf("Summary 未包含昵称：%q", c.Summary)
	}

	// 反例 1：不是首次出现
	if got := Extract(Event{Text: "第一次来", FromName: "小鱼"}, ExtractOptions{IsFirstNick: false}); len(got) != 0 {
		t.Errorf("IsFirstNick=false 却命中了：%+v", got)
	}
	// 反例 2：昵称为空，无法作为「首次出现的昵称」
	if got := Extract(Event{Text: "第一次来", FromName: "  "}, ExtractOptions{IsFirstNick: true}); len(got) != 0 {
		t.Errorf("昵称为空却命中了：%+v", got)
	}
}

// TestExtractDefaultEmpty 默认不提炼：四条规则都不匹配就返回空。
func TestExtractDefaultEmpty(t *testing.T) {
	cases := []Event{
		{Text: "今天天气不错", FromName: "路人"},
		{Text: "哈哈哈哈", FromName: "路人"},
		{Text: "666", FromName: "路人"},
	}
	for _, ev := range cases {
		if got := Extract(ev, ExtractOptions{}); len(got) != 0 {
			t.Errorf("%q 不应提炼，实际 %+v", ev.Text, got)
		}
	}
}

// TestExtractFilters 空文本、纯符号、私密事件都必须返回空。
func TestExtractFilters(t *testing.T) {
	t.Run("空文本", func(t *testing.T) {
		for _, text := range []string{"", "   ", "\n\t  \r\n"} {
			if got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{}); len(got) != 0 {
				t.Errorf("%q 应返回空，实际 %+v", text, got)
			}
		}
	})

	t.Run("纯符号", func(t *testing.T) {
		// 这些文本即使带上承诺上下文也必须被过滤，所以用 IsFirstNick=true 一起验证
		symbols := []string{"？", "？？？", "。。。", "——", "…", "!!!", "「」", "🙂🙂", "（ ）"}
		for _, text := range symbols {
			if got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{IsFirstNick: true}); len(got) != 0 {
				t.Errorf("%q 是纯符号，应返回空，实际 %+v", text, got)
			}
		}
	})

	t.Run("含数字不算纯符号", func(t *testing.T) {
		if got := Extract(Event{Text: "666", FromName: "路人"}, ExtractOptions{IsFirstNick: true}); len(got) != 1 {
			t.Errorf("含数字应可提炼，实际 %+v", got)
		}
	})

	t.Run("私密事件不提炼", func(t *testing.T) {
		ev := Event{Text: "我答应明天陪你", FromName: "主人", Private: true}
		if got := Extract(ev, ExtractOptions{OwnerName: "主人", IsFirstNick: true}); len(got) != 0 {
			t.Errorf("私密事件应 fail-closed 返回空，实际 %+v", got)
		}
	})
}

// TestExtractTooLong 超过 4000 字（按 rune）不提炼；正好 4000 字仍提炼。
func TestExtractTooLong(t *testing.T) {
	makeText := func(total int) string {
		head := "我答应"
		return head + strings.Repeat("鱼", total-utf8.RuneCountInString(head))
	}

	t.Run("正好4000字可提炼", func(t *testing.T) {
		text := makeText(maxExtractRunes)
		if n := utf8.RuneCountInString(text); n != maxExtractRunes {
			t.Fatalf("构造文本长度 %d，期望 %d", n, maxExtractRunes)
		}
		got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{})
		if len(got) != 1 {
			t.Fatalf("期望 1 条，实际 %d", len(got))
		}
		assertLimits(t, got[0])
	})

	t.Run("超过4000字不提炼", func(t *testing.T) {
		text := makeText(maxExtractRunes + 1)
		if got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{}); len(got) != 0 {
			t.Errorf("超长文本应返回空，实际 %d 条", len(got))
		}
	})
}

// TestExtractChineseTruncation 中文截断按 rune：不切坏字符、不超长、以省略号收尾。
func TestExtractChineseTruncation(t *testing.T) {
	text := "我答应" + strings.Repeat("白饭", 200) // 403 个字，必然触发两处截断

	got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{})
	if len(got) != 1 {
		t.Fatalf("候选数 = %d，期望 1：%+v", len(got), got)
	}
	c := got[0]
	assertLimits(t, c)

	if n := utf8.RuneCountInString(c.Summary); n != maxSummaryRunes {
		t.Errorf("Summary 长度 %d，期望正好 %d", n, maxSummaryRunes)
	}
	if n := utf8.RuneCountInString(c.Title); n != maxTitleRunes {
		t.Errorf("Title 长度 %d，期望正好 %d", n, maxTitleRunes)
	}
	if !strings.HasSuffix(c.Summary, "…") {
		t.Errorf("超长 Summary 应以省略号收尾：%q", c.Summary)
	}
	if !strings.HasSuffix(c.Title, "…") {
		t.Errorf("超长 Title 应以省略号收尾：%q", c.Title)
	}

	// 关键断言：截断后的内容必须是原文的「字符前缀」，证明没有在字符中间切断
	summaryBody := strings.TrimSuffix(c.Summary, "…")
	if !strings.HasPrefix(text, summaryBody) {
		t.Errorf("Summary 不是原文的字符前缀，说明切坏了：%q", summaryBody)
	}
	titleBody := strings.TrimSuffix(strings.TrimPrefix(c.Title, "承诺："), "…")
	if !strings.HasPrefix(text, titleBody) {
		t.Errorf("Title 不是原文的字符前缀，说明切坏了：%q", titleBody)
	}
}

// TestExtractShortTextNotTruncated 不超长时不加省略号，摘要与原文一致。
func TestExtractShortTextNotTruncated(t *testing.T) {
	const text = "我保证明天早点睡"
	got := Extract(Event{Text: text, FromName: "路人"}, ExtractOptions{})
	if len(got) != 1 {
		t.Fatalf("候选数 = %d，期望 1", len(got))
	}
	if got[0].Summary != text {
		t.Errorf("Summary = %q，期望与原文一致 %q", got[0].Summary, text)
	}
	if strings.HasSuffix(got[0].Summary, "…") {
		t.Error("未超长的摘要不应出现省略号")
	}
	if got[0].Title != "承诺："+text {
		t.Errorf("Title = %q，期望 %q", got[0].Title, "承诺："+text)
	}
}

// TestExtractMultiRuleOrder 多条规则同时命中时按优先级返回：承诺 → 主人 → 首次出现。
func TestExtractMultiRuleOrder(t *testing.T) {
	ev := Event{Text: "我答应主人明天带话", FromName: "路人甲"}
	got := Extract(ev, ExtractOptions{IsFirstNick: true})

	want := []string{KindPromise, KindPreference, KindFirstSeen}
	if gotKinds := kindsOf(got); !slices.Equal(gotKinds, want) {
		t.Fatalf("顺序 = %v，期望 %v", gotKinds, want)
	}
	for _, c := range got {
		assertLimits(t, c)
	}

	// 两条规则命中时不应多出第三条
	ev2 := Event{Text: "我答应明天带话", FromName: "路人甲"}
	if gotKinds := kindsOf(Extract(ev2, ExtractOptions{})); !slices.Equal(gotKinds, []string{KindPromise}) {
		t.Errorf("只应命中承诺，实际 %v", gotKinds)
	}
}

// TestExtractScores 规则权重必须严格等于派发单规定的数值。
func TestExtractScores(t *testing.T) {
	cases := []struct {
		name       string
		ev         Event
		opts       ExtractOptions
		wantKind   string
		wantImport float64
	}{

		{"承诺", Event{Text: "我保证"}, ExtractOptions{}, KindPromise, 0.9}, {"主人", Event{Text: "主人好"}, ExtractOptions{}, KindPreference, 0.8},
		{"首次", Event{Text: "来了", FromName: "小鱼"}, ExtractOptions{IsFirstNick: true}, KindFirstSeen, 0.4},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.ev, tt.opts)
			if len(got) != 1 {
				t.Fatalf("候选数 = %d，期望 1：%+v", len(got), got)
			}
			c := got[0]
			if c.Kind != tt.wantKind {
				t.Errorf("Kind = %q，期望 %q", c.Kind, tt.wantKind)
			}
			if c.Importance != tt.wantImport {
				t.Errorf("Importance = %v，期望 %v", c.Importance, tt.wantImport)
			}
			// Confidence 必须是有限且落在 [0,1] 的合理值
			if c.Confidence <= 0 || c.Confidence > 1 {
				t.Errorf("Confidence = %v，期望 (0,1]", c.Confidence)
			}
			assertLimits(t, c)
		})
	}
}

// TestExtractDoesNotMutateInput 纯函数不改入参（值语义 + 不做原地处理）。
func TestExtractDoesNotMutateInput(t *testing.T) {
	ev := Event{Text: "我答应明天带话", FromName: "路人"}
	before := ev
	_ = Extract(ev, ExtractOptions{IsFirstNick: true})
	if ev.Text != before.Text || ev.FromName != before.FromName || ev.Private != before.Private {
		t.Errorf("Extract 修改了入参：%+v", ev)
	}
}
