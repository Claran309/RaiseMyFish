package personaeval

import (
	"strings"
	"testing"
)

func TestScorerFrozenLabelsAndBoundaries(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	c := Case{ID: "live-1", Channel: "live", Kind: "normal", Input: "你好", Expect: []string{"认身份", "短句"}, Forbid: []string{"喊主人", "长句"}}
	if f := s.Evaluate(c, "本鱼在"); len(f) != 0 {
		t.Fatal(f)
	}
	if f := s.Evaluate(c, "主人，本鱼在"); len(f) == 0 {
		t.Fatal("禁止项被忽略")
	}
	if f := s.Evaluate(c, strings.Repeat("鱼", 21)); len(f) == 0 {
		t.Fatal("短句边界被放宽")
	}
	if err := s.ValidateCases([]Case{c, c}); err == nil {
		t.Fatal("重复ID未拒绝")
	}
	c.Expect = []string{"unknown"}
	if err := s.ValidateCases([]Case{c}); err == nil {
		t.Fatal("未知标签未拒绝")
	}
	if len(s.Snapshot()) == 0 {
		t.Fatal("缺少规则快照")
	}
}

func TestScorerMatchesExistingVocabulary(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label, output string
		forbid        bool
	}{
		{"认护短", "被发现了", false}, {"接梗", "领域展开·坐杀思路", false}, {"谐音", "魔鱼爽", false}, {"情绪", "哼", false}, {"兜底", "何意味", false}, {"拒绝", "才不告诉你", false}, {"问主人", "这个得问主人", false}, {"纯爱", "纯爱谢谢", false}, {"护娇娇", "娇娇", false},
	} {
		c := Case{Channel: "live", Expect: []string{tc.label}}
		if f := s.Evaluate(c, tc.output); len(f) > 0 {
			t.Fatal(tc.label, f)
		}
	}
	c := Case{Channel: "comment", Forbid: []string{"长句"}}
	if f := s.Evaluate(c, strings.Repeat("鱼", 61)); len(f) == 0 {
		t.Fatal("人格comment60限制未生效")
	}
	c = Case{Channel: "live", Forbid: []string{"列表", "括号动作"}}
	if f := s.Evaluate(c, "（叹气）\n- 好"); len(f) == 0 {
		t.Fatal("格式禁止项未生效")
	}
}
