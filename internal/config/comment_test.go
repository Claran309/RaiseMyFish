package config

// 本文件测 [collector.comment]/[sender.comment] 的默认值、依赖与安全校验。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommentDefaultsOff(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[log]\nlevel='info'\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Collector.Comment.Enabled {
		t.Error("评论采集默认应关闭")
	}
	if cfg.Collector.Comment.IntervalSeconds != 60 {
		t.Errorf("默认轮询间隔 = %d，期望 60", cfg.Collector.Comment.IntervalSeconds)
	}
	if !cfg.Collector.Comment.AutoDiscover {
		t.Error("评论自动发现默认应开启")
	}
	if cfg.Collector.Comment.DiscoverIntervalSeconds != 600 {
		t.Errorf("默认发现间隔 = %d，期望 600", cfg.Collector.Comment.DiscoverIntervalSeconds)
	}
	if cfg.Sender != nil && cfg.Sender.Comment != nil && cfg.Sender.Comment.Enabled {
		t.Error("评论发送默认应关闭")
	}
}

func TestCommentConfigValidAndPrivate(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=comment-canary; bili_jct=token")
	body := `
[collector.live]
self_id = "8"
[channels.comment]
enabled = true
[collector.comment]
enabled = true
videos = ["123", "456"]
interval_seconds = 30
[sender.comment]
enabled = true
`
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !cfg.Collector.Comment.Enabled || len(cfg.Collector.Comment.Videos) != 2 || cfg.Collector.Comment.IntervalSeconds != 30 {
		t.Errorf("评论采集配置不符：%+v", cfg.Collector.Comment)
	}
	if cfg.Sender == nil || cfg.Sender.Comment == nil || !cfg.Sender.Comment.Enabled {
		t.Fatalf("评论发送开关未解析：%+v", cfg.Sender)
	}
	if cfg.Collector.Live.Cookie == "" {
		t.Error("live 关闭时也应为评论解析凭据")
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "comment-canary") {
		t.Fatal("序列化泄露评论凭据")
	}
}

func TestCommentChannelOnlyIsValid(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[channels.comment]\nenabled=true\n")
	if err != nil {
		t.Fatalf("仅开启评论通道应合法：%v", err)
	}
	if !cfg.Channels.Comment.Enabled {
		t.Error("channels.comment 未解析")
	}
}

func TestCommentCollectorRejects(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x")
	const self = "[collector.live]\nself_id='8'\n"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"关闭发现且缺videos", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nauto_discover=false\n", "videos"},
		{"发现间隔过小", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\ndiscover_interval_seconds=5\n", "discover_interval_seconds"},
		{"发现间隔过大", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\ndiscover_interval_seconds=86401\n", "discover_interval_seconds"},
		{"非法oid", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['abc']\n", "oid"},
		{"零oid", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['000']\n", "oid"},
		{"重复oid", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['1','01']\n", "重复"},
		{"间隔过小", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['1']\ninterval_seconds=5\n", "interval_seconds"},
		{"间隔过大", self + "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['1']\ninterval_seconds=3601\n", "interval_seconds"},
		{"未开通道", self + "[collector.comment]\nenabled=true\nvideos=['1']\n", "channels.comment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadWithScheduler(t, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestCommentAutoDiscoverAllowsEmptyVideos(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x")
	cfg, err := loadWithScheduler(t, "[collector.live]\nself_id='8'\n[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=[]\n")
	if err != nil {
		t.Fatalf("自动发现开启时应允许空 videos：%v", err)
	}
	if !cfg.Collector.Comment.AutoDiscover || len(cfg.Collector.Comment.Videos) != 0 {
		t.Errorf("自动发现配置不符：%+v", cfg.Collector.Comment)
	}
}

func TestCommentRequiresCredential(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "")
	t.Setenv("FISH_BILI_COOKIE_FILE", "")
	body := "[collector.live]\nself_id='8'\n[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['1']\n"
	if _, err := loadWithScheduler(t, body); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("无凭据应拒绝采集，实际 %v", err)
	}
	body = "[channels.comment]\nenabled=true\n[sender.comment]\nenabled=true\n"
	if _, err := loadWithScheduler(t, body); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("无凭据应拒绝发送，实际 %v", err)
	}
}

func TestCommentCollectorRequiresSelfID(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x")
	t.Setenv("FISH_BILI_SELF_ID", "")
	body := "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['1']\n"
	if _, err := loadWithScheduler(t, body); err == nil || !strings.Contains(err.Error(), "self_id") {
		t.Fatalf("缺 self_id 应拒绝采集，实际 %v", err)
	}
}

func TestCommentSenderRequiresChannel(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x")
	if _, err := loadWithScheduler(t, "[sender.comment]\nenabled=true\n"); err == nil || !strings.Contains(err.Error(), "channels.comment") {
		t.Fatalf("未开通道应拒绝发送，实际 %v", err)
	}
}
