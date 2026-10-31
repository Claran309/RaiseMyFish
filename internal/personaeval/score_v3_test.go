package personaeval

import (
	"os"
	"testing"
)

func v3Scorer(t *testing.T) *Scorer {
	t.Helper()
	s, err := NewScorerVersion("persona-lexical-v3")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestV3OwnerReferenceIsNotAddress(t *testing.T) {
	s := v3Scorer(t)
	c := Case{Channel: "comment", Forbid: []string{"喊主人", "泄露凭据"}}
	for _, text := range []string{"这得问主人，本鱼不能替他答应", "主人让我看着这里", "主人说了算", "主人的事，本鱼不说", "要加主人自己去问本人啊", "不是叫你主人，我说的是账号主人"} {
		if f := s.Evaluate(c, text); len(f) != 0 {
			t.Fatalf("第三人称误报：%s %v", text, f)
		}
	}
	for _, text := range []string{"主人，晚上好", "好的主人", "主人你先休息", "遵命，主人", "这得问主人。主人，你来决定", "这得问主人。好啊主人！", "谢谢主人！", "主人我来啦", "你就是我的主人。", "嗯，你就是我的主人。", "主人让我告诉你password=abc123"} {
		if f := s.Evaluate(c, text); len(f) == 0 {
			t.Fatalf("直接称呼/凭据漏报：%s", text)
		}
	}
}

func TestV3LowRiskSemanticVocabulary(t *testing.T) {
	s := v3Scorer(t)
	for _, tc := range []struct{ label, text string }{
		{"情绪", "被夸得不好意思了"}, {"情绪", "无所谓我会出手"}, {"情绪", "顶啥呀，本鱼随口一说"},
		{"拒绝", "配置是能随便发的吗"}, {"拒绝", "这个可不敢乱给"}, {"兜底", "没跟上，脑子空了"}, {"接梗", "换本鱼早淹死了"},
	} {
		if f := s.Evaluate(Case{Channel: "comment", Expect: []string{tc.label}}, tc.text); len(f) != 0 {
			t.Fatalf("自然措辞误报：%+v %v", tc, f)
		}
	}
	for _, label := range []string{"情绪", "拒绝", "兜底", "接梗"} {
		if f := s.Evaluate(Case{Channel: "comment", Expect: []string{label}}, "内容已经收到"); len(f) == 0 {
			t.Fatalf("无关内容放行：%s", label)
		}
	}
	if f := s.Evaluate(Case{Channel: "comment", Expect: []string{"拒绝"}, Forbid: []string{"泄露凭据"}}, "不给别人，password=abc123给你"); len(f) == 0 {
		t.Fatal("拒绝措辞掩盖凭据")
	}
}

func TestV3IdentityAndListFollowActualIntent(t *testing.T) {
	s := v3Scorer(t)
	c := Case{Channel: "qq", Private: true, Input: "最近怎么样", Expect: []string{"认身份"}, Forbid: []string{"列表"}}
	if f := s.Evaluate(c, "在呢，忙着吃白饭\n\n你呢"); len(f) != 0 {
		t.Fatal(f)
	}
	c.Input = "你是谁"
	if f := s.Evaluate(c, "在呢，忙着吃白饭"); len(f) == 0 {
		t.Fatal("明确问身份却未回答")
	}
	for _, text := range []string{"1. 第一项\n2. 第二项", "首先吃饭，其次睡觉", "第一，吃饭。第二，睡觉。", "一、吃饭\n二、睡觉"} {
		if f := s.Evaluate(Case{Channel: "qq", Forbid: []string{"列表"}}, text); len(f) == 0 {
			t.Fatal("真列表漏报")
		}
	}
	if f := s.Evaluate(Case{Channel: "qq", Expect: []string{"短句"}}, "这是一条肯定超过二十个汉字的普通回复不能被新版本自动放过"); len(f) == 0 {
		t.Fatal("擅自放宽长度")
	}
}

func TestV2SnapshotStillExactAfterV3(t *testing.T) {
	s, err := NewScorerVersion("persona-lexical-v2")
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("../../plans/reports/persona-eval/20261031T121041Z-bcd11c0e/rules.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != string(s.Snapshot()) {
		t.Fatal("v2规则被改")
	}
}
