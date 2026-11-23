package conversationeval

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/persona"
)

func testPack() persona.Pack {
	return persona.Pack{
		Meta: persona.Meta{Name: "test", Version: "v1", Channels: []string{"dm", "comment"}},
		Soul: "身份", Rules: "规则", Samples: "样本",
		Contexts: map[string]string{"dm": "私信场景", "comment": "评论场景"},
	}
}

func evalCase() Case {
	return Case{
		ID: "dm-followup", Kind: "dm_followup", Channel: "dm", Private: true,
		ConversationID: "dm:1001:2002", Session: "1001:2002",
		Turns: []Turn{
			{Dir: "in", From: "阿明", Text: "你记得我吗"},
			{Dir: "out", Text: "记得，你是常来钓鱼那位"},
		},
		Input: "那上次说的鱼呢", ExpectContext: []string{"常来钓鱼那位"},
	}
}

// TestRunResolvesFollowup 带会话时先行词进入 prompt，不带会话时不会。
func TestRunResolvesFollowup(t *testing.T) {
	report, err := Run(context.Background(), []Case{evalCase()}, Options{Pack: testPack()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Cases != 1 || report.Resolved != 1 || report.BaselineResolved != 0 {
		t.Fatalf("指标不符：%+v", report)
	}
	if report.DeltaPoints != 100 || report.CrossUserLeakRate != 0 || report.KeyCollisions != 0 {
		t.Fatalf("差值/泄漏不符：%+v", report)
	}
	if report.PromptTokenDeltaPct <= 0 {
		t.Fatalf("注入会话后 token 应增加：%v", report.PromptTokenDeltaPct)
	}
	// 延迟与成本离线不可测，必须是 nil 而不是 0。
	if report.P95LatencyDeltaMS != nil || report.CostPerReplyDeltaCNY != nil {
		t.Fatal("离线评测不应伪造延迟/成本")
	}
}

// TestRunDetectsMissingContext 先行词不在最近轮次里时必须判为未解决。
func TestRunDetectsMissingContext(t *testing.T) {
	c := evalCase()
	c.ExpectContext = []string{"从来没说过的内容"}
	report, err := Run(context.Background(), []Case{c}, Options{Pack: testPack()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Resolved != 0 || !strings.Contains(report.Detail[0].Reason, "缺少先行词") {
		t.Fatalf("漏报：%+v", report.Detail[0])
	}
}

// TestRunDetectsKeyCollision 不同用例共用会话键时直接判为泄漏。
func TestRunDetectsKeyCollision(t *testing.T) {
	a, b := evalCase(), evalCase()
	b.ID = "other"
	report, err := Run(context.Background(), []Case{a, b}, Options{Pack: testPack()})
	if err != nil {
		t.Fatal(err)
	}
	if report.KeyCollisions != 2 || report.CrossUserLeakRate != 1 {
		t.Fatalf("键冲突未识别：%+v", report)
	}
	for _, item := range report.Detail {
		if !item.Leaked || !strings.Contains(item.Reason, "会话键") {
			t.Fatalf("冲突用例未标记：%+v", item)
		}
	}
}

// TestRunKeepsOnlyRecentTurns 超过 recent_turns 的历史不注入，必须靠摘要。
func TestRunKeepsOnlyRecentTurns(t *testing.T) {
	c := Case{
		ID: "long", Channel: "dm", Private: true, ConversationID: "dm:1:2", Session: "1:2",
		Input: "还记得开头吗", ExpectContext: []string{"最早那句"},
	}
	// 关键事实放在最早一轮：只注入最近 N 轮时必然丢掉，必须靠摘要补。
	c.Turns = append(c.Turns, Turn{Dir: "in", From: "阿明", Text: "最早那句"})
	for i := 0; i < 12; i++ {
		c.Turns = append(c.Turns, Turn{Dir: "in", From: "阿明", Text: "第" + string(rune('0'+i)) + "轮"})
	}
	report, err := Run(context.Background(), []Case{c}, Options{Pack: testPack(), RecentTurns: 4})
	if err != nil {
		t.Fatal(err)
	}
	if report.Resolved != 0 || report.Detail[0].InjectedTurns != 4 {
		t.Fatalf("轮次上限未生效：%+v", report.Detail[0])
	}
	// 同一用例补上摘要后必须能解决：证明「摘要 + 最近 N 轮」两层装配有效。
	c.Summary = "最早那句说过的话"
	report, err = Run(context.Background(), []Case{c}, Options{Pack: testPack(), RecentTurns: 4})
	if err != nil {
		t.Fatal(err)
	}
	if report.Resolved != 1 {
		t.Fatalf("摘要未参与装配：%+v", report.Detail[0])
	}
}

// TestRunRejectsBadCases 缺字段的用例在加载阶段就报错。
func TestRunRejectsBadCases(t *testing.T) {
	path := t.TempDir() + "/cases.jsonl"
	if err := os.WriteFile(path, []byte(`{"id":"","channel":"dm","session":"1:2","turns":[{"dir":"in","text":"x"}],"input":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCases(path); err == nil {
		t.Fatal("缺 id 的用例被接受")
	}
}

// TestEvalTimeIsFixed 评测必须可复现：时间不能跟当前时钟走。
func TestEvalTimeIsFixed(t *testing.T) {
	if !evalTime().Equal(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("评测时间不固定")
	}
}
