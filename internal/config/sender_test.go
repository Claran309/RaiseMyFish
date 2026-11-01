package config

// 本文件测 0.11 修复批次的 `[sender]` 段：默认值、校验、整段缺失。

import (
	"strings"
	"testing"
)

// TestSenderDefaultsFilled 只写 script_path 时补齐 node_path 与超时。
func TestSenderDefaultsFilled(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[sender]\nscript_path = \"tools/bili-whale/danmaku.mjs\"\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	s := cfg.Sender
	if s == nil {
		t.Fatal("带 [sender] 段时不应为 nil")
	}
	if s.ScriptPath != "tools/bili-whale/danmaku.mjs" {
		t.Errorf("script_path = %q", s.ScriptPath)
	}
	if s.NodePath != "node" {
		t.Errorf("node_path 默认值应为 node，实际 %q", s.NodePath)
	}
	if s.HTTPTimeoutMS != 35000 {
		t.Errorf("http_timeout_ms 默认值应为 35000，实际 %d", s.HTTPTimeoutMS)
	}
}

// TestSenderAbsentIsNil 没有 [sender] 段时保持 nil（不启用发送）。
func TestSenderAbsentIsNil(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[log]\nlevel='info'\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Sender != nil {
		t.Errorf("缺少 [sender] 段时应为 nil，实际 %+v", cfg.Sender)
	}
}

// TestSenderExplicitValuesKept 显式值不被默认值覆盖，且与采集配置互不影响。
func TestSenderExplicitValuesKept(t *testing.T) {
	body := "[collector.live]\nscript_path = \"tools/bili-whale/danmaku-await.mjs\"\n" +
		"[sender]\nnode_path = \"/opt/node\"\nscript_path = \"tools/bili-whale/danmaku.mjs\"\nself_id = \"123456\"\nhttp_timeout_ms = 12000\n"
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	s := cfg.Sender
	if s.NodePath != "/opt/node" || s.ScriptPath != "tools/bili-whale/danmaku.mjs" {
		t.Errorf("路径被覆盖：%+v", s)
	}
	if s.SelfID != "123456" || s.HTTPTimeoutMS != 12000 {
		t.Errorf("显式值不符：%+v", s)
	}
	if cfg.Collector.Live.ScriptPath != "tools/bili-whale/danmaku-await.mjs" {
		t.Errorf("采集脚本被发送配置污染：%q", cfg.Collector.Live.ScriptPath)
	}
}

// TestSenderValidationRejects 非法 sender 配置在 Load 阶段被拒。
func TestSenderValidationRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "超时过小",
			body: "[sender]\nscript_path = \"a.mjs\"\nhttp_timeout_ms = 10\n",
			want: "http_timeout_ms",
		},
		{
			name: "超时过大",
			body: "[sender]\nscript_path = \"a.mjs\"\nhttp_timeout_ms = 900000\n",
			want: "http_timeout_ms",
		},
		{
			name: "self_id 非数字",
			body: "[sender]\nscript_path = \"a.mjs\"\nself_id = \"abc\"\n",
			want: "self_id",
		},
		{
			name: "self_id 全零",
			body: "[sender]\nscript_path = \"a.mjs\"\nself_id = \"000\"\n",
			want: "self_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithScheduler(t, tc.body)
			if err == nil {
				t.Fatal("非法配置未被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息 %q 不含 %q", err.Error(), tc.want)
			}
		})
	}
}

// TestSenderValidateDirect 直接调 Validate 覆盖「段已存在但字段为空」的分支。
func TestSenderValidateDirect(t *testing.T) {
	var nilSender *Sender
	if err := nilSender.Validate(); err != nil {
		t.Errorf("nil 应通过：%v", err)
	}
	nilSender.ApplyDefaults() // nil 不能 panic

	empty := &Sender{NodePath: "node", HTTPTimeoutMS: 35000}
	if err := empty.Validate(); err == nil || !strings.Contains(err.Error(), "script_path") {
		t.Errorf("script_path 为空应报错，实际 %v", err)
	}

	noNode := &Sender{ScriptPath: "a.mjs", HTTPTimeoutMS: 35000}
	if err := noNode.Validate(); err == nil || !strings.Contains(err.Error(), "node_path") {
		t.Errorf("node_path 为空应报错，实际 %v", err)
	}

	ctrl := &Sender{NodePath: "no\nde", ScriptPath: "a.mjs", HTTPTimeoutMS: 35000}
	if err := ctrl.Validate(); err == nil {
		t.Error("路径含控制字符应报错")
	}

	// self_id 留空是合法的：装配时回落到采集侧同一账号
	ok := &Sender{NodePath: "node", ScriptPath: "a.mjs", HTTPTimeoutMS: 35000}
	if err := ok.Validate(); err != nil {
		t.Errorf("合法配置被拒：%v", err)
	}
}
