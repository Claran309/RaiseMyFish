package store

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/llmmeta"
	"math"
	"testing"
	"time"
)

func TestUsageValidation(t *testing.T) {
	valid := Usage{At: time.Now(), Channel: event.ChLive, Purpose: string(llmmeta.PurposeReply), Model: "mock:model", Milliseconds: 1, OK: false}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Usage){
		func(u *Usage) { u.Model = "" },
		func(u *Usage) { u.Channel = "bad" },
		func(u *Usage) { u.At = time.Time{} },
		func(u *Usage) { n := -1; u.InputTokens = &n },
		func(u *Usage) { u.Milliseconds = -1 },
		func(u *Usage) { n := math.NaN(); u.CostUSD = &n },
		func(u *Usage) { n := -1.; u.CostUSD = &n },
		func(u *Usage) { u.Purpose = "" },
		func(u *Usage) { u.Purpose = "not-a-purpose" },
		func(u *Usage) { u.CorrelationID = "有中文" },
		func(u *Usage) { u.CorrelationID = "line\nbreak" },
		func(u *Usage) { n := -1; u.CachedInputTokens = &n },
	} {
		u := valid
		mutate(&u)
		if err := u.Validate(); err == nil {
			t.Fatal("无效用量被接受")
		}
	}
	n := 0
	free := 0.
	valid.InputTokens = &n
	valid.OutputTokens = &n
	valid.CostUSD = &free
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestUsageChannelRules 通道规则按用途区分：现场调用必须带通道，后台/embedding 允许 NULL。
func TestUsageChannelRules(t *testing.T) {
	base := Usage{At: time.Now(), Model: "m", Milliseconds: 1, OK: true}
	for _, purpose := range []llmmeta.Purpose{llmmeta.PurposeReply, llmmeta.PurposeReact, llmmeta.PurposeVision} {
		u := base
		u.Purpose = string(purpose)
		if err := u.Validate(); err == nil {
			t.Fatalf("用途 %s 缺通道被接受", purpose)
		}
		u.Channel = event.ChLive
		if err := u.Validate(); err != nil {
			t.Fatalf("用途 %s 带通道仍被拒绝：%v", purpose, err)
		}
	}
	for _, purpose := range []llmmeta.Purpose{llmmeta.PurposeMemoryExtract, llmmeta.PurposeMemeLearn, llmmeta.PurposeEmbedIndex, llmmeta.PurposeEmbedQuery, llmmeta.PurposeUnknown} {
		u := base
		u.Purpose = string(purpose)
		if err := u.Validate(); err != nil {
			t.Fatalf("后台用途 %s 不应要求通道：%v", purpose, err)
		}
	}
}

// TestUsageCachedTokensBound 缓存命中不能超过输入；只有提供方给了值才写。
func TestUsageCachedTokensBound(t *testing.T) {
	in, cached := 10, 11
	u := Usage{At: time.Now(), Channel: event.ChLive, Purpose: string(llmmeta.PurposeReply), Model: "m", Milliseconds: 1, InputTokens: &in, CachedInputTokens: &cached}
	if err := u.Validate(); err == nil {
		t.Fatal("缓存命中超过输入被接受")
	}
	cached = 4
	if err := u.Validate(); err != nil {
		t.Fatalf("合法缓存命中被拒绝：%v", err)
	}
}

// TestUsageCorrelationIDRules 关联标识只接受可打印 ASCII，空值允许。
func TestUsageCorrelationIDRules(t *testing.T) {
	u := Usage{At: time.Now(), Channel: event.ChLive, Purpose: string(llmmeta.PurposeReply), Model: "m", Milliseconds: 1, OK: true}
	u.CorrelationID = "live:30308220:123456"
	if err := u.Validate(); err != nil {
		t.Fatalf("合法关联标识被拒绝：%v", err)
	}
	u.CorrelationID = ""
	if err := u.Validate(); err != nil {
		t.Fatalf("空关联标识应允许：%v", err)
	}
}
