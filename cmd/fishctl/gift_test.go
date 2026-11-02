package main

// 本文件测 0.14 的 fishctl gift send：参数、配置映射、dry-run 区分与限额。
// 全部用 fake sender，不启动 Node、不访问平台。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/gift"
)

type fakeGiftSender struct {
	result  gift.Result
	err     error
	calls   int
	gotRoom string
	gotGift string
	gotNum  int
}

func (f *fakeGiftSender) Send(_ context.Context, room, giftID string, num int) (gift.Result, error) {
	f.calls++
	f.gotRoom, f.gotGift, f.gotNum = room, giftID, num
	return f.result, f.err
}

func writeGiftConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseGiftArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want giftCommand
	}{
		{"默认礼物与数量", []string{"send", "--room", " 99 "}, giftCommand{Action: "send", Room: "99", Num: -1}},
		{"全参数", []string{"send", "--room", "99", "--gift-id", "31036", "--num", "2"}, giftCommand{Action: "send", Room: "99", GiftID: "31036", Num: 2}},
		{"help", []string{"send", "--help"}, giftCommand{Action: "help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGiftArgs(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got != tc.want {
				t.Fatalf("解析结果 = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

func TestParseGiftArgsRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"缺子命令":   nil,
		"未知子命令":  {"buy"},
		"缺房间":    {"send"},
		"空白房间":   {"send", "--room", "  "},
		"数量为零":   {"send", "--room", "99", "--num", "0"},
		"数量为负":   {"send", "--room", "99", "--num", "-2"},
		"多余参数":   {"send", "--room", "99", "extra"},
		"未知flag": {"send", "--room", "99", "--foo", "1"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGiftArgs(args, io.Discard); err == nil {
				t.Fatalf("参数 %v 未被拒绝", args)
			}
		})
	}
}

func TestGiftOptionsDryRunVsReal(t *testing.T) {
	base := config.Gift{
		Enabled: true, Real: false,
		NodePath: "node", ScriptPath: "tools/gift.mjs",
		TimeoutMS: 30000, DailyLimit: 2, MaxPrice: 100, MaxNum: 5,
		AllowedRooms: []string{"99"},
	}
	ledger := filepath.Join(t.TempDir(), "state", "gift-ledger.json")
	dry, err := giftOptions(base, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.DailyLimit != 2 || dry.MaxPrice != 100 || dry.MaxNum != 5 ||
		dry.NodePath != "node" || dry.ScriptPath != "tools/gift.mjs" {
		t.Fatalf("dry-run 映射不符：%+v", dry)
	}
	if dry.Quota == nil || len(dry.AllowedRooms) != 1 || dry.AllowedRooms[0] != "99" {
		t.Fatalf("额度仓储/白名单未映射：%+v", dry)
	}
	if dry.Cookie != "" || dry.CookieFile != "" {
		t.Fatal("dry-run 不应携带凭据")
	}

	real := base
	real.Real = true
	real.Cookie, real.CookieFile = "SESSDATA=x", "cookie.txt"
	actual, err := giftOptions(real, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if actual.DryRun || actual.Cookie != "SESSDATA=x" || actual.CookieFile != "cookie.txt" {
		t.Fatalf("真实配置映射不符：%+v", actual)
	}
}

func TestRunGiftDryRunOutput(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := writeGiftConfig(t, "[gift]\nenabled=true\nreal=false\ngift_id=\"31036\"\nnum=1\n")
	sender := &fakeGiftSender{result: gift.Result{Sent: true, GiftID: "31036", Num: 1}}
	var seenGift config.Gift
	var seenLedger string
	factory := func(cfg config.Gift, ledger string) (giftSender, error) {
		seenGift, seenLedger = cfg, ledger
		return sender, nil
	}
	var out bytes.Buffer
	if err := runGiftWith(context.Background(), path, "", []string{"send", "--room", "99"}, &out, io.Discard, factory); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	var got giftSendOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是合法 JSON：%v\n%s", err, out.String())
	}
	if !got.DryRun || got.Sent || got.Room != "99" || got.GiftID != "31036" || got.Num != 1 {
		t.Fatalf("dry-run 输出不符：%+v", got)
	}
	if !strings.Contains(got.Message, "未发送") {
		t.Errorf("dry-run 提示不明：%q", got.Message)
	}
	if sender.calls != 1 || sender.gotRoom != "99" || sender.gotGift != "31036" || sender.gotNum != 1 {
		t.Fatalf("sender 参数不符：%+v", sender)
	}
	if seenGift.Real {
		t.Fatal("dry-run 配置不应是 real")
	}
	if filepath.Base(seenLedger) != "gift-ledger.json" {
		t.Fatalf("账本路径不符：%q", seenLedger)
	}
}

func TestRunGiftRealOutput(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=fixture")
	path := writeGiftConfig(t, "[gift]\nenabled=true\nreal=true\n")
	sender := &fakeGiftSender{result: gift.Result{Sent: true, GiftID: "31036", Num: 1}}
	factory := func(config.Gift, string) (giftSender, error) { return sender, nil }
	var out bytes.Buffer
	if err := runGiftWith(context.Background(), path, "", []string{"send", "--room", "99", "--num", "1"}, &out, io.Discard, factory); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	var got giftSendOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DryRun || !got.Sent || got.Message != "已发送" {
		t.Fatalf("真实输出不符：%+v", got)
	}
}

func TestRunGiftUsesConfiguredDefaults(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := writeGiftConfig(t, "[gift]\nenabled=true\ngift_id=\"42\"\nnum=3\n")
	sender := &fakeGiftSender{result: gift.Result{Sent: true}}
	factory := func(config.Gift, string) (giftSender, error) { return sender, nil }
	if err := runGiftWith(context.Background(), path, "", []string{"send", "--room", "99"}, io.Discard, io.Discard, factory); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if sender.gotGift != "42" || sender.gotNum != 3 {
		t.Fatalf("未使用 [gift] 默认值：gift=%q num=%d", sender.gotGift, sender.gotNum)
	}
}

func TestRunGiftSendFailureKeepsJSON(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := writeGiftConfig(t, "[gift]\nenabled=true\n")
	boom := &gift.Error{Kind: gift.KindFatal, Message: "礼物价格超过配置上限"}
	factory := func(config.Gift, string) (giftSender, error) {
		return &fakeGiftSender{err: boom}, nil
	}
	var out bytes.Buffer
	err := runGiftWith(context.Background(), path, "", []string{"send", "--room", "99"}, &out, io.Discard, factory)
	if !errors.Is(err, boom) {
		t.Fatalf("错误未透传：%v", err)
	}
	var got giftSendOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("失败时也应输出 JSON：%v\n%s", err, out.String())
	}
	if got.Sent || got.Error != "礼物价格超过配置上限" {
		t.Fatalf("失败输出不符：%+v", got)
	}
}

func TestRunGiftDisabledRefusesBeforeFactory(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := writeGiftConfig(t, "[gift]\nenabled=false\n")
	called := false
	factory := func(config.Gift, string) (giftSender, error) {
		called = true
		return &fakeGiftSender{}, nil
	}
	err := runGiftWith(context.Background(), path, "", []string{"send", "--room", "99"}, io.Discard, io.Discard, factory)
	if err == nil || !strings.Contains(err.Error(), "送礼未启用") {
		t.Fatalf("未启用未被拒绝：%v", err)
	}
	if called {
		t.Fatal("未启用时不应装配 sender")
	}
}

// TestGiftSenderWhitelistBeforeNode 白名单在 Go 侧拦截，不启动 Node。
func TestGiftSenderWhitelistBeforeNode(t *testing.T) {
	cfg := config.Gift{
		Enabled: true, Real: false,
		NodePath: "node", ScriptPath: "tools/gift.mjs",
		TimeoutMS: 30000, DailyLimit: 1, MaxPrice: 100, MaxNum: 10,
		AllowedRooms: []string{"99"},
	}
	sender, err := newGiftSender(cfg, filepath.Join(t.TempDir(), "gift-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := sender.Send(context.Background(), "100", "31036", 1)
	var typed *gift.Error
	if !errors.As(err, &typed) || typed.Message != "房间不在送礼白名单" || result.Error == "" {
		t.Fatalf("白名单未拦截：%+v %v", result, err)
	}
}

func TestGiftLedgerPathFollowsStateFile(t *testing.T) {
	cfg := config.Config{Runtime: config.Runtime{StateFile: filepath.Join("data", "fishd-status.json")}}
	want := filepath.Join("data", "gift-ledger.json")
	if got := giftLedgerPath(cfg); got != want {
		t.Fatalf("账本路径 = %q，期望 %q", got, want)
	}
}

func TestRunRoutesGiftCommand(t *testing.T) {
	err := run(context.Background(), []string{"gift", "send"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "room") {
		t.Fatalf("gift 路由未生效：%v", err)
	}
}
