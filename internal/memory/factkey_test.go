package memory

import "testing"

// TestFactKeyStableAndNormalized 同主体同谓词稳定；标点/空白/大小写不产生新版本。
func TestFactKeyStableAndNormalized(t *testing.T) {
	base := FactKey("public", "2002", "喜欢：吃白饭")
	if base == "" {
		t.Fatal("fact_key 为空")
	}
	for _, variant := range []string{"喜欢：吃白饭", "喜欢: 吃白饭", "喜欢 吃白饭", "喜欢：吃白饭。"} {
		if got := FactKey("public", "2002", variant); got != base {
			t.Errorf("归一失败：%q → %q，期望 %q", variant, got, base)
		}
	}
	if FactKey("public", "3003", "喜欢：吃白饭") == base {
		t.Error("不同主体共享 fact_key")
	}
	if FactKey("private:dm:dm-a", "2002", "喜欢：吃白饭") == base {
		t.Error("私密域与公开域共享 fact_key")
	}
	if FactKey("public", "2002", "讨厌：吃白饭") == base {
		t.Error("相反谓词被错误合并")
	}
	if FactKey("", "2002", "x") != "" || FactKey("public", "", "x") != "" {
		t.Error("空主体/域应返回空键")
	}
}
