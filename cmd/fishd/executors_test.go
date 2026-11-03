package main

// 本文件测工具执行器装配：无凭据保持 nil（不报错），有凭据才装配。

import (
	"strings"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func execConfig(cookie string) config.Config {
	cfg := config.Config{}
	cfg.Collector.Live.RoomID = "30308220"
	cfg.Collector.Live.Cookie = cookie
	cfg.Gift.GiftID = "31036"
	cfg.Gift.MaxNum = 1
	cfg.Gift.DailyLimit = 1
	cfg.Gift.AllowedRooms = []string{"30308220"}
	cfg.Runtime.StateFile = "data/fishd-status.json"
	cfg.Watch.LLMRoute = "default"
	cfg.Scheduler = &config.Scheduler{Channels: map[event.Channel]config.ChannelPolicy{
		event.ChLive: {OwnerIDs: []string{"313591854"}},
	}}
	cfg.Sender = &config.Sender{SelfID: "3747574135655903"}
	return cfg
}

// TestBuildToolExecutorsWithoutCookie 无凭据：全部 nil，只记日志，不报错。
func TestBuildToolExecutorsWithoutCookie(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	execs := buildToolExecutors(execConfig(""), zap.New(core))

	if execs.Live != nil || execs.Social != nil || execs.Frame != nil || execs.FrameRead != nil || execs.Search != nil {
		t.Errorf("无凭据时不应装配任何执行器：%+v", execs)
	}
	entries := logs.FilterMessage("工具执行器未装配：未配置凭据")
	if entries.Len() != 1 {
		t.Fatalf("应记录一条「未配置凭据」日志，实际 %d", entries.Len())
	}
	if got := entries.All()[0].ContextMap()["reason"]; got != "no_cookie" {
		t.Errorf("reason = %v", got)
	}
}

// TestBuildToolExecutorsWithCookie 有凭据：只读/写执行器装配，且日志不含凭据。
func TestBuildToolExecutorsWithCookie(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	cookie := "SESSDATA=secret-sess; bili_jct=csrf-token-123"
	execs := buildToolExecutors(execConfig(cookie), zap.New(core))

	if execs.Live == nil {
		t.Error("有 cookie 应装配直播间状态执行器")
	}
	if execs.Social == nil {
		t.Error("有 bili_jct 应装配社交执行器")
	}
	for _, entry := range logs.All() {
		for _, field := range entry.Context {
			if strings.Contains(field.String, "secret-sess") || strings.Contains(field.String, "csrf-token-123") {
				t.Fatalf("日志泄漏凭据：%s=%s", field.Key, field.String)
			}
		}
	}
}

// TestBuildToolExecutorsWithoutCSRF 只有 SESSDATA：只读可用，写操作不装配。
func TestBuildToolExecutorsWithoutCSRF(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	execs := buildToolExecutors(execConfig("SESSDATA=only"), zap.New(core))

	if execs.Live == nil {
		t.Error("只读执行器应装配")
	}
	if execs.Social != nil {
		t.Error("缺 bili_jct 时不应装配社交执行器")
	}
	if logs.FilterMessage("工具执行器缺少 bili_jct，写操作不可用").Len() != 1 {
		t.Error("应记录缺少 csrf 的告警")
	}
}

// TestBuildToolExecutorsSearchWithoutCookie 搜索不依赖B站凭据：无 cookie 也装配，密钥不进日志。
func TestBuildToolExecutorsSearchWithoutCookie(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	cfg := execConfig("")
	cfg.Web.SearchURL = "https://search.example.com/api"
	cfg.Web.SearchKey = "search-secret"
	execs := buildToolExecutors(cfg, zap.New(core))

	if execs.Search == nil {
		t.Fatal("配置了 search_url 时应装配搜索执行器")
	}
	if execs.Live != nil || execs.Social != nil || execs.Frame != nil {
		t.Errorf("无 cookie 时不应装配B站执行器：%+v", execs)
	}
	entries := logs.FilterMessage("工具执行器未装配：未配置凭据")
	if entries.Len() != 1 {
		t.Fatalf("应记录一条未配置凭据日志，实际 %d", entries.Len())
	}
	if got, ok := entries.All()[0].ContextMap()["web_search"].(bool); !ok || !got {
		t.Errorf("日志 web_search = %v，期望 true", entries.All()[0].ContextMap()["web_search"])
	}
	for _, entry := range logs.All() {
		for _, field := range entry.Context {
			if strings.Contains(field.String, "search-secret") {
				t.Fatalf("日志泄漏搜索密钥：%s=%s", field.Key, field.String)
			}
		}
	}
}

// TestBuildToolExecutorsBadSearchURL 坏地址只降级搜索，不影响其它执行器。
func TestBuildToolExecutorsBadSearchURL(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	cfg := execConfig("SESSDATA=only")
	cfg.Web.SearchURL = "ftp://search.example.com/api"
	execs := buildToolExecutors(cfg, zap.New(core))

	if execs.Search != nil {
		t.Error("坏地址不应装配搜索执行器")
	}
	if execs.Live == nil {
		t.Error("B站只读执行器仍应装配")
	}
	if logs.FilterMessage("搜索执行器未装配").Len() != 1 {
		t.Error("应记录搜索降级告警")
	}
}
