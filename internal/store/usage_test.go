package store

import (
	"RaiseMyFish/internal/event"
	"math"
	"testing"
	"time"
)

func TestUsageValidation(t *testing.T) {
	valid := Usage{At: time.Now(), Channel: event.ChLive, Model: "mock:model", Milliseconds: 1, OK: false}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Usage){func(u *Usage) { u.Model = "" }, func(u *Usage) { u.Channel = "bad" }, func(u *Usage) { u.At = time.Time{} }, func(u *Usage) { n := -1; u.InputTokens = &n }, func(u *Usage) { u.Milliseconds = -1 }, func(u *Usage) { n := math.NaN(); u.CostUSD = &n }, func(u *Usage) { n := -1.; u.CostUSD = &n }} {
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
