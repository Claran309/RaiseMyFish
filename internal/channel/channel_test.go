package channel

import (
	"context"
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
)

type fakeChannel struct {
	name event.Channel
}

func (f *fakeChannel) Name() event.Channel { return f.name }

func (f *fakeChannel) Collect(context.Context, event.Bus) (func(), error) {
	return func() {}, nil
}

func (f *fakeChannel) Send(context.Context, event.Claim, Reply) (Result, error) {
	return Result{}, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	var registry Registry // 零值可用
	live := &fakeChannel{name: event.ChLive}
	if err := registry.Register(live); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	got, ok := registry.Get(event.ChLive)
	if !ok || got != live {
		t.Fatalf("查找结果不符：ok=%v got=%v", ok, got)
	}
	if _, ok := registry.Get(event.ChDM); ok {
		t.Fatal("未注册通道不应查到")
	}
}

func TestRegistryRejectsInvalid(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(nil); err == nil {
		t.Fatal("nil 通道应被拒绝")
	}
	if err := registry.Register(&fakeChannel{}); err == nil {
		t.Fatal("空名通道应被拒绝")
	}
	first := &fakeChannel{name: event.ChLive}
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	second := &fakeChannel{name: event.ChLive}
	if err := registry.Register(second); err == nil {
		t.Fatal("重复注册应被拒绝")
	}
	got, ok := registry.Get(event.ChLive)
	if !ok || got != first {
		t.Fatal("重复注册不应覆盖已有实例")
	}
}

func TestRegistryAllSortedSnapshot(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []event.Channel{event.ChQQ, event.ChLive, event.ChDM} {
		if err := registry.Register(&fakeChannel{name: name}); err != nil {
			t.Fatal(err)
		}
	}
	all := registry.All()
	if len(all) != 3 {
		t.Fatalf("通道数量不符：%d", len(all))
	}
	names := make([]string, 0, len(all))
	for _, ch := range all {
		names = append(names, string(ch.Name()))
	}
	want := []string{string(event.ChDM), string(event.ChLive), string(event.ChQQ)}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("排序不符：%v", names)
	}
	// 快照：改切片不影响注册表
	all[0] = nil
	if got := registry.All()[0]; got == nil {
		t.Fatal("All 返回的切片与注册表共享存储")
	}
}
