package conversation

import (
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

func liveEvent(room, uid, text string) event.Event {
	return event.Event{
		ID: "live:" + room + ":x", Channel: event.ChLive, Dir: event.DirIn,
		Session: room, FromID: uid, FromName: "观众" + uid, Text: text,
	}
}

// TestWindowKeepsRecentTurnsWithinTTL 30 秒内能接上，超时后不再注入。
func TestWindowKeepsRecentTurnsWithinTTL(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	w := NewWindow(WindowOptions{TTL: 30 * time.Second, Now: clock})
	w.Add(liveEvent("30308220", "42", "这个好看"))
	now = now.Add(10 * time.Second)
	got := w.Recent(liveEvent("30308220", "42", "多少钱"))
	if len(got) != 1 || got[0].Text != "这个好看" {
		t.Fatalf("30 秒内未接上：%+v", got)
	}
	now = now.Add(31 * time.Second)
	if got := w.Recent(liveEvent("30308220", "42", "还在吗")); len(got) != 0 {
		t.Fatalf("超时仍注入：%+v", got)
	}
	if w.Len() != 0 {
		t.Fatalf("过期键未清理：%d", w.Len())
	}
}

// TestWindowIsolatesRoomAndUser 不同房间/不同用户互不串。
func TestWindowIsolatesRoomAndUser(t *testing.T) {
	w := NewWindow(WindowOptions{TTL: time.Minute, Now: func() time.Time { return time.Unix(1000, 0) }})
	w.Add(liveEvent("room-a", "42", "A 的话"))
	if got := w.Recent(liveEvent("room-b", "42", "问题")); len(got) != 0 {
		t.Fatalf("跨房间串了：%+v", got)
	}
	if got := w.Recent(liveEvent("room-a", "43", "问题")); len(got) != 0 {
		t.Fatalf("跨用户串了：%+v", got)
	}
}

// TestWindowIsBounded 单用户条数与键总数都有上限。
func TestWindowIsBounded(t *testing.T) {
	now := time.Unix(2000, 0)
	w := NewWindow(WindowOptions{TTL: time.Minute, PerUser: 2, MaxKeys: 2, Now: func() time.Time { return now }})
	for _, text := range []string{"一", "二", "三"} {
		w.Add(liveEvent("room", "42", text))
		now = now.Add(time.Second)
	}
	got := w.Recent(liveEvent("room", "42", "四"))
	if len(got) != 2 || got[0].Text != "二" || got[1].Text != "三" {
		t.Fatalf("单用户未按上限保留：%+v", got)
	}
	w.Add(liveEvent("room", "43", "别人的"))
	w.Add(liveEvent("room", "44", "第三个人"))
	if w.Len() > 2 {
		t.Fatalf("键数超过上限：%d", w.Len())
	}
}

// TestWindowDisabledByDefault TTL<=0 时不建窗口，调用是空操作。
func TestWindowDisabledByDefault(t *testing.T) {
	w := NewWindow(WindowOptions{})
	if w != nil {
		t.Fatal("TTL=0 应关闭窗口")
	}
	w.Add(liveEvent("room", "42", "话"))
	if got := w.Recent(liveEvent("room", "42", "问")); got != nil {
		t.Fatalf("关闭时仍有数据：%+v", got)
	}
}

// TestWindowKeyIgnoresNonLiveAndPrivate 直播短窗口不覆盖私密/非直播通道。
func TestWindowKeyIgnoresNonLiveAndPrivate(t *testing.T) {
	if got := WindowKey(liveEvent("room", "42", "话")); got != "live:room:42" {
		t.Fatalf("键=%q", got)
	}
	dm := liveEvent("room", "42", "话")
	dm.Channel = event.ChDM
	if WindowKey(dm) != "" {
		t.Fatal("私信不该进直播短窗口")
	}
	private := liveEvent("room", "42", "话")
	private.Private = true
	if WindowKey(private) != "" {
		t.Fatal("私密事件不该进直播短窗口")
	}
}
