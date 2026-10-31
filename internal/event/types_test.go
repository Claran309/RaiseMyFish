package event

import "testing"

func TestEnums(t *testing.T) {
	for _, ch := range []Channel{ChLive, ChComment, ChDM, ChQQ} {
		if !ch.Valid() {
			t.Fatalf("合法通道被拒绝：%s", ch)
		}
	}
	if Channel("other").Valid() || Dir("other").Valid() || Status("other").Valid() {
		t.Fatal("非法枚举被接受")
	}
	for _, d := range []Dir{DirIn, DirOut} {
		if !d.Valid() {
			t.Fatal("合法方向被拒绝")
		}
	}
	for _, s := range []Status{StatusPending, StatusSkipped, StatusQueued, StatusReplying, StatusSent, StatusDone, StatusRetry, StatusDead} {
		if !s.Valid() {
			t.Fatal("合法状态被拒绝")
		}
	}
}
