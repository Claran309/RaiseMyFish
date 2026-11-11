package msgfeed

// 本文件测消息监听水位的文件持久化与账号隔离。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileCursorStorePersistsPerAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "msgfeed-cursors.json")
	store, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("8", Cursor{Initialized: true, ReplyTime: 100, ReplyID: "10", AtTime: 101, AtID: "11", LastViewAt: 150}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("9", Cursor{Initialized: true, ReplyTime: 200}); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := reopened.Load("8")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Initialized || first.ReplyTime != 100 || first.ReplyID != "10" || first.AtTime != 101 || first.AtID != "11" {
		t.Fatalf("账号 8 水位不符：%+v", first)
	}
	if first.LastViewAt != 150 {
		t.Fatalf("查看水位未持久化：%+v", first)
	}
	second, err := reopened.Load("9")
	if err != nil || !second.Initialized || second.ReplyTime != 200 {
		t.Fatalf("账号 9 水位不符：%+v %v", second, err)
	}
}

func TestFileCursorStoreBadFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "msgfeed-cursors.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.Load("8")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Initialized || cursor.ReplyTime != 0 {
		t.Fatalf("坏文件应按空水位处理：%+v", cursor)
	}
}

func TestFileCursorStoreRejectsNegativeLastViewAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "msgfeed-cursors.json")
	store, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("8", Cursor{Initialized: true, LastViewAt: -1}); err == nil {
		t.Fatal("负的查看水位应拒绝保存")
	}
}

func TestNewFileCursorStoreRejectsBadPath(t *testing.T) {
	if _, err := NewFileCursorStore("bad\x00path"); err == nil {
		t.Fatal("非法路径应拒绝")
	}
}
