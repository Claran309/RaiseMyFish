package persona

// 本文件只测 context.md 的切分规则（contracts/persona.md §2 第 5 条）。

import (
	"strings"
	"testing"
)

// TestSplitContextsBasic 文件头说明不作为场景，`## <key>` 才切分。
func TestSplitContextsBasic(t *testing.T) {
	raw := "# 大肥鱼 · 各通道口径\n" +
		"这是文件头说明，只给人看。\n" +
		"## live\n弹幕口径。\n" +
		"## qq_private\n私聊口径。\n"

	got, err := splitContexts(raw)
	if err != nil {
		t.Fatalf("切分失败：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("场景数 = %d，期望 2（%v）", len(got), got)
	}
	if got[ContextLive] != "弹幕口径。\n" {
		t.Errorf("live 正文 = %q", got[ContextLive])
	}
	if got[ContextQQPrivate] != "私聊口径。\n" {
		t.Errorf("qq_private 正文 = %q", got[ContextQQPrivate])
	}
	if _, ok := got["大肥鱼 · 各通道口径"]; ok {
		t.Error("文件头说明被误当成场景")
	}
}

// TestSplitContextsFive 五个场景键都能切出来。
func TestSplitContextsFive(t *testing.T) {
	var b strings.Builder
	for _, k := range ContextKeys() {
		b.WriteString("## " + k + "\n" + k + " 的正文。\n")
	}

	got, err := splitContexts(b.String())
	if err != nil {
		t.Fatalf("切分失败：%v", err)
	}
	if len(got) != len(ContextKeys()) {
		t.Fatalf("场景数 = %d，期望 %d", len(got), len(ContextKeys()))
	}
	for _, k := range ContextKeys() {
		if got[k] != k+" 的正文。\n" {
			t.Errorf("%s 正文 = %q", k, got[k])
		}
	}
}

// TestSplitContextsFence 围栏代码块里的 `## xxx` 不算场景标题。
func TestSplitContextsFence(t *testing.T) {
	raw := "## live\n" +
		"下面是示例：\n" +
		"```\n" +
		"## comment\n" +
		"## 随便写的\n" +
		"```\n" +
		"结束。\n"

	got, err := splitContexts(raw)
	if err != nil {
		t.Fatalf("切分失败：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("场景数 = %d，期望 1（围栏内不切分）：%v", len(got), got)
	}
	if !strings.Contains(got[ContextLive], "## comment") {
		t.Errorf("围栏内容丢失：%q", got[ContextLive])
	}
}

// TestSplitContextsTildeFence ~~~ 围栏同样受保护。
func TestSplitContextsTildeFence(t *testing.T) {
	raw := "## live\n~~~\n## comment\n~~~\n"
	got, err := splitContexts(raw)
	if err != nil {
		t.Fatalf("切分失败：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("场景数 = %d，期望 1：%v", len(got), got)
	}
}

// TestSplitContextsChineseHeading 中文说明标题不成为新场景，原样留在正文里。
func TestSplitContextsChineseHeading(t *testing.T) {
	raw := "## live\n内容一。\n## 直播口径补充\n内容二。\n### 三级标题\n内容三。\n"

	got, err := splitContexts(raw)
	if err != nil {
		t.Fatalf("切分失败：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("场景数 = %d，期望 1：%v", len(got), got)
	}
	body := got[ContextLive]
	for _, want := range []string{"内容一。", "## 直播口径补充", "内容二。", "### 三级标题", "内容三。"} {
		if !strings.Contains(body, want) {
			t.Errorf("live 正文丢了 %q，实际：%q", want, body)
		}
	}
}

// TestSplitContextsReject 重复键、未知 ASCII 键、空段、无段都必须报错。
func TestSplitContextsReject(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{
			name:    "重复场景键",
			raw:     "## live\n甲。\n## live\n乙。\n",
			wantSub: "重复出现",
		},
		{
			name:    "未知 ASCII 键",
			raw:     "## live\n甲。\n## telegram\n乙。\n",
			wantSub: "未知场景键",
		},
		{
			name:    "空场景正文",
			raw:     "## live\n## comment\n有内容。\n",
			wantSub: "正文为空",
		},
		{
			name:    "没有分节",
			raw:     "只有一段说明文字。\n",
			wantSub: "没有任何",
		},
		{
			name:    "空文本",
			raw:     "",
			wantSub: "没有任何",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := splitContexts(tt.raw)
			if err == nil {
				t.Fatal("期望报错，实际没有")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("错误信息 %q 不含关键词 %q", err.Error(), tt.wantSub)
			}
		})
	}
}

// TestMatchDeclaredChannels 声明集合与实际段集合必须完全相等。
func TestMatchDeclaredChannels(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
		contexts map[string]string
		wantErr  bool
	}{
		{
			name:     "完全一致",
			declared: []string{"live", "comment"},
			contexts: map[string]string{"live": "a", "comment": "b"},
		},
		{
			name:     "最小 live 包合法",
			declared: []string{"live"},
			contexts: map[string]string{"live": "a"},
		},
		{
			name:     "声明了但没写段",
			declared: []string{"live", "dm"},
			contexts: map[string]string{"live": "a"},
			wantErr:  true,
		},
		{
			name:     "写了段但没声明",
			declared: []string{"live"},
			contexts: map[string]string{"live": "a", "dm": "b"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := matchDeclaredChannels(tt.declared, tt.contexts)
			if tt.wantErr && err == nil {
				t.Error("期望报错，实际通过")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("期望通过，实际报错：%v", err)
			}
		})
	}
}

// TestSplitContextsPreservesBody 只有场景标题被移除，正文首尾空白/缩进及末尾换行保持原样。
func TestSplitContextsPreservesBody(t *testing.T) {
	for _, body := range []string{"\n    缩进首行  \n\n", "无末尾换行", "段落\n", "  前后空格  "} {
		got, err := splitContexts("## live\n" + body)
		if err != nil {
			t.Fatal(err)
		}
		if got[ContextLive] != body {
			t.Fatalf("正文被改写：got=%q want=%q", got[ContextLive], body)
		}
	}
}

// TestSplitContextsMixedHeadingIsBody 混合标题不是独立场景键，必须保留在当前场景。
func TestSplitContextsMixedHeadingIsBody(t *testing.T) {
	body := "前段\n## live 补\n补充文字\n"
	got, err := splitContexts("## live\n" + body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[ContextLive] != body {
		t.Fatalf("混合标题不应切分或丢失：%q", got[ContextLive])
	}
}
