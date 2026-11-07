package dm

// 本文件测「当日收信人」落库：重启后读回、同日不同人拒绝、跨天放行、同人放行。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"RaiseMyFish/internal/sender"
)

func newPersistSender(t *testing.T, path string, clock *time.Time) (*Sender, *fakeTransport) {
	t.Helper()
	tr := &fakeTransport{}
	rel := &fakeRelation{attribute: mutualAttribute}
	s, err := New(tr, rel, Options{
		AllowedMIDs:   []string{"313591854", "777"},
		RateLimit:     10 * time.Minute,
		Now:           func() time.Time { return *clock },
		RecipientPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, tr
}

// TestRecipientPersistsAcrossRestart 重启（新建 Sender）后仍记得当天收信人。
func TestRecipientPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-recipient.json")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	// 第一次运行：发给主人
	first, _ := newPersistSender(t, path, &now)
	if _, err := first.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "主人好"}); err != nil {
		t.Fatalf("首次发送失败：%v", err)
	}
	// 文件应已写入
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("状态文件未写入：%v", err)
	}
	var st recipientState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("状态文件不是合法JSON：%v", err)
	}
	if st.Date != "2026-11-04" || st.Peer != "313591854" {
		t.Fatalf("落盘内容不符：%+v", st)
	}

	// 模拟重启：新建 Sender，读回文件 → 同日换人应拒绝
	restarted, tr2 := newPersistSender(t, path, &now)
	_, err = restarted.Send(context.Background(), claimFor("777"), sender.Reply{Text: "换个收信人"})
	if err == nil {
		t.Fatal("重启后同日换收信人应拒绝")
	}
	if tr2.sent != 0 {
		t.Errorf("拒绝时不应真正发送：%d", tr2.sent)
	}

	// 同一个人仍可发（限额/限频另算）
	now = now.Add(11 * time.Minute)
	if _, err := restarted.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "再聊一句"}); err != nil {
		t.Fatalf("同收信人应放行：%v", err)
	}
}

// TestRecipientCrossDayAllowed 跨天后允许换人并覆盖文件。
func TestRecipientCrossDayAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-recipient.json")
	now := time.Date(2026, 11, 4, 23, 50, 0, 0, time.UTC)

	first, _ := newPersistSender(t, path, &now)
	if _, err := first.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "今天最后一句"}); err != nil {
		t.Fatalf("首次发送失败：%v", err)
	}

	// 跨到第二天（UTC）
	now = time.Date(2026, 11, 5, 0, 10, 0, 0, time.UTC)
	next, _ := newPersistSender(t, path, &now)
	if _, err := next.Send(context.Background(), claimFor("777"), sender.Reply{Text: "新的一天"}); err != nil {
		t.Fatalf("跨天应允许换人：%v", err)
	}
	st, err := NewRecipientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-11-05" || got.Peer != "777" {
		t.Fatalf("跨天未覆盖：%+v", got)
	}
}

// TestRecipientNotWrittenOnFailure 明确未发送的拒绝（鉴权）释放当天名额。
//
// 结果未知的失败必须保留预占，见 TestRecipientUnknownKeepsReservation。
func TestRecipientNotWrittenOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-recipient.json")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr := newPersistSender(t, path, &now)
	tr.err = errors.New("403 forbidden")

	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "会失败"}); err == nil {
		t.Fatal("传输失败应报错")
	}
	store, _ := NewRecipientStore(path)
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "" {
		t.Fatalf("失败不应写入收信人：%+v", got)
	}
	// 换人仍可尝试（恢复传输）
	tr.err = nil
	if _, err := s.Send(context.Background(), claimFor("777"), sender.Reply{Text: "换人"}); err != nil {
		t.Fatalf("失败后换人应允许：%v", err)
	}
}

// TestRecipientStoreRoundTrip 存储读写与坏文件容忍。
func TestRecipientStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dm-recipient.json")
	store, err := NewRecipientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got.Peer != "" {
		t.Fatalf("缺失文件应返回零值：%+v / %v", got, err)
	}
	if err := store.Save(recipientState{Date: "2026-11-04", Peer: "42"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := store.Load()
	if err != nil || got.Peer != "42" {
		t.Fatalf("读回不符：%+v / %v", got, err)
	}
	// 坏 JSON 当作没有记录
	if err := os.WriteFile(path, []byte("{坏的"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got.Peer != "" {
		t.Fatalf("坏文件应降级为空：%+v / %v", got, err)
	}
}

// TestRecipientStoreRejectsEmptyPath 路径为空直接拒绝。
func TestRecipientStoreRejectsEmptyPath(t *testing.T) {
	if _, err := NewRecipientStore("  "); err == nil {
		t.Error("空路径应报错")
	}
}
