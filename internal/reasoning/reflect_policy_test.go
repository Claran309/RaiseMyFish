package reasoning

import "testing"

func TestReflectPolicyChannelsAndReasons(t *testing.T) {
	policy := ReflectPolicy{Live: true, Comment: false, DM: false, MinRunes: 60, OnTools: true}
	if ok, reason := policy.ShouldReflect("comment", "一段足够长的草稿", 0, "问"); ok || reason != "channel_off" {
		t.Fatalf("评论默认应关闭：%v %s", ok, reason)
	}
	if ok, reason := policy.ShouldReflect("live", "短句", 0, "早"); ok || reason != "short" {
		t.Fatalf("寒暄不应触发：%v %s", ok, reason)
	}
	if ok, reason := policy.ShouldReflect("live", "短句", 2, "早"); !ok || reason != "tools" {
		t.Fatalf("用过工具应触发：%v %s", ok, reason)
	}
	if ok, reason := policy.ShouldReflect("live", "短句", 0, "你记得我吗"); !ok || reason != "question" {
		t.Fatalf("问答应触发：%v %s", ok, reason)
	}
	if ok, _ := policy.ShouldReflect("live", string(make([]rune, 61)), 0, ""); !ok {
		t.Fatal("长草稿应触发")
	}
}

func TestReflectPolicyCommentAndDMEnabled(t *testing.T) {
	policy := ReflectPolicy{Live: true, Comment: true, DM: true, MinRunes: 60, OnTools: true}
	for _, channel := range []string{"comment", "dm"} {
		if ok, _ := policy.ShouldReflect(channel, "短", 0, "在吗"); !ok {
			t.Fatalf("%s 显式开启后问答应触发", channel)
		}
		if ok, reason := policy.ShouldReflect(channel, "短", 0, "早"); ok || reason != "short" {
			t.Fatalf("%s 寒暄不应触发：%v %s", channel, ok, reason)
		}
	}
}
