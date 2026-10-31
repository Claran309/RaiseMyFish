package personaeval

import (
	"reflect"
	"testing"
)

func TestFrozenHoldoutKindsAreMetadataOnly(t *testing.T) {
	s, err := NewScorerVersion("persona-lexical-v3")
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, kind := range []string{"normal", "praise", "abuse", "confused", "privacy", "impersonate", "contact", "promise"} {
		c := Case{ID: "metadata", Channel: "comment", Kind: kind, Input: "测试请求", Expect: []string{"拒绝"}, Forbid: []string{"长句"}}
		if err := s.ValidateCases([]Case{c}); err != nil {
			t.Fatalf("冻结分类不被接受：%s %v", kind, err)
		}
		got := s.Evaluate(c, "不能提供，本鱼不代答")
		if kind == "normal" {
			expected = got
		} else if !reflect.DeepEqual(expected, got) {
			t.Fatal("kind改变了评分")
		}
	}
	c := Case{ID: "unknown", Channel: "comment", Kind: "unreviewed", Input: "测试请求", Expect: []string{"拒绝"}, Forbid: []string{"长句"}}
	if err := s.ValidateCases([]Case{c}); err == nil {
		t.Fatal("未知分类被放行")
	}
	input := reflect.TypeOf(Input{})
	for _, field := range []string{"Kind", "Expect", "Forbid"} {
		if _, found := input.FieldByName(field); found {
			t.Fatalf("元数据泄露到模型输入：%s", field)
		}
	}
}
