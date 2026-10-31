package persona

// 本文件只测 meta.yaml 的解码与字段校验（contracts/persona.md §2）。

import (
	"strings"
	"testing"
)

// TestDecodeMetaValid 正常 YAML 的各种合法写法都要能解出来。
func TestDecodeMetaValid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Meta
	}{
		{
			name:  "行内数组 + 双引号",
			input: "name: \"dafeiyu\"\nversion: \"v1\"\nchannels: [live, comment]\n",
			want:  Meta{Name: "dafeiyu", Version: "v1", Channels: []string{"live", "comment"}},
		},
		{
			name:  "块状数组 + 单引号",
			input: "name: 'dafeiyu'\nversion: 'v1'\nchannels:\n  - live\n  - qq_private\n",
			want:  Meta{Name: "dafeiyu", Version: "v1", Channels: []string{"live", "qq_private"}},
		},
		{
			name:  "中文与可选字段",
			input: "name: dafeiyu\ndisplay_name: 大肥鱼\nversion: v1\nchannels: [live]\ntts_voice: whale-girl\ndescription: 傲娇、爱吃白饭\n",
			want: Meta{
				Name: "dafeiyu", DisplayName: "大肥鱼", Version: "v1",
				Channels: []string{"live"}, TTSVoice: "whale-girl", Description: "傲娇、爱吃白饭",
			},
		},
		{
			name:  "注释与空行",
			input: "# 头注释\n\nname: mini  # 行尾注释\nversion: v1\nchannels:\n\n  - live\n",
			want:  Meta{Name: "mini", Version: "v1", Channels: []string{"live"}},
		},
		{
			name:  "可选字段留空",
			input: "name: mini\nversion: v1\nchannels: [live]\ndescription: \"\"\n",
			want:  Meta{Name: "mini", Version: "v1", Channels: []string{"live"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeMeta([]byte(tt.input))
			if err != nil {
				t.Fatalf("解码失败：%v", err)
			}
			if got.Name != tt.want.Name || got.DisplayName != tt.want.DisplayName ||
				got.Version != tt.want.Version || got.TTSVoice != tt.want.TTSVoice ||
				got.Description != tt.want.Description {
				t.Errorf("got %+v，期望 %+v", got, tt.want)
			}
			if len(got.Channels) != len(tt.want.Channels) {
				t.Fatalf("channels = %v，期望 %v", got.Channels, tt.want.Channels)
			}
			for i := range got.Channels {
				if got.Channels[i] != tt.want.Channels[i] {
					t.Errorf("channels[%d] = %q，期望 %q", i, got.Channels[i], tt.want.Channels[i])
				}
			}
		})
	}
}

// TestDecodeMetaReject 未知/重复字段、类型错误、多文档、空内容都必须拒绝。
func TestDecodeMetaReject(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantSub string // 期望错误信息里出现的关键词（便于定位是哪一类拒绝）
	}{
		{
			name:    "未知字段",
			input:   "name: mini\nversion: v1\nchannels: [live]\nfuture_field: 随便\n",
			wantSub: "field future_field",
		},
		{
			name:    "重复字段",
			input:   "name: mini\nname: dafeiyu\nversion: v1\nchannels: [live]\n",
			wantSub: "already defined",
		},
		{
			name:    "类型错误",
			input:   "name: mini\nversion: v1\nchannels: secret_value_xyz\n",
			wantSub: "YAML 解码失败",
		},
		{
			name:    "多文档",
			input:   "name: mini\nversion: v1\nchannels: [live]\n---\nname: other\nversion: v2\nchannels: [live]\n",
			wantSub: "多个 YAML 文档",
		},
		{
			name:    "空内容",
			input:   "",
			wantSub: "内容为空",
		},
		{
			name:    "只有注释",
			input:   "# 什么都没有\n",
			wantSub: "内容为空",
		},
		{
			name:    "语法错误",
			input:   "name: [未闭合\n",
			wantSub: "YAML 解码失败",
		},
		{
			name:    "缩进错乱的嵌套",
			input:   "name: mini\nversion: v1\nchannels: [live]\n  bad: 缩进\n",
			wantSub: "YAML 解码失败",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeMeta([]byte(tt.input))
			if err == nil {
				t.Fatal("期望报错，实际没有")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("错误信息 %q 不含关键词 %q", err.Error(), tt.wantSub)
			}
		})
	}
}

// TestDecodeMetaErrorHidesValue 错误信息不得回显文件正文里的原始标量（契约 §2）。
func TestDecodeMetaErrorHidesValue(t *testing.T) {
	const secret = "secret_value_xyz"

	_, err := decodeMeta([]byte("name: mini\nversion: v1\nchannels: " + secret + "\n"))
	if err == nil {
		t.Fatal("期望类型错误")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("错误信息回显了原始标量：%q", err.Error())
	}
}

// TestValidateMeta 字段校验：必填、场景键白名单、去重。
func TestValidateMeta(t *testing.T) {
	tests := []struct {
		name    string
		meta    Meta
		wantErr bool
	}{
		{name: "最小合法", meta: Meta{Name: "mini", Version: "v1", Channels: []string{"live"}}},
		{
			name: "五键全声明",
			meta: Meta{Name: "dafeiyu", Version: "v1",
				Channels: []string{"live", "comment", "dm", "qq_private", "qq_group"}},
		},
		{name: "缺 name", meta: Meta{Version: "v1", Channels: []string{"live"}}, wantErr: true},
		{name: "name 只有空格", meta: Meta{Name: "  ", Version: "v1", Channels: []string{"live"}}, wantErr: true},
		{name: "缺 version", meta: Meta{Name: "mini", Channels: []string{"live"}}, wantErr: true},
		{name: "channels 为空", meta: Meta{Name: "mini", Version: "v1"}, wantErr: true},
		{
			name:    "未知场景键",
			meta:    Meta{Name: "mini", Version: "v1", Channels: []string{"telegram"}},
			wantErr: true,
		},
		{
			name:    "把事件通道当场景键",
			meta:    Meta{Name: "mini", Version: "v1", Channels: []string{"qq"}},
			wantErr: true,
		},
		{
			name:    "重复场景键",
			meta:    Meta{Name: "mini", Version: "v1", Channels: []string{"live", "live"}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMeta(tt.meta)
			if tt.wantErr && err == nil {
				t.Error("期望报错，实际通过")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("期望通过，实际报错：%v", err)
			}
		})
	}
}
