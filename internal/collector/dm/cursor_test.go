package dm

// 本文件测私信水位存储：文件原子读写、账号隔离、坏文件降级、非法条目过滤。

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestFileCursorStoreRoundTrip 跨账号读写互不覆盖。
func TestFileCursorStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dm-cursors.json")
	store, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatalf("构造存储失败：%v", err)
	}
	a := map[string]Cursor{"2002": {Watermark: "10", Frontier: "20", PendingFrom: "15"}}
	b := map[string]Cursor{"3003": {Watermark: "1"}}
	if err := store.Save("acc-a", a); err != nil {
		t.Fatalf("保存 acc-a 失败：%v", err)
	}
	if err := store.Save("acc-b", b); err != nil {
		t.Fatalf("保存 acc-b 失败：%v", err)
	}

	// 重新构造（模拟重启）后仍能读回。
	reopened, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for account, want := range map[string]map[string]Cursor{"acc-a": a, "acc-b": b} {
		got, err := reopened.Load(account)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", account, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s 水位不符：%+v，期望 %+v", account, got, want)
		}
	}
	if got, err := reopened.Load("missing"); err != nil || len(got) != 0 {
		t.Errorf("未知账号应为空：%+v / %v", got, err)
	}

	// 覆盖 acc-a 不得丢掉 acc-b。
	if err := reopened.Save("acc-a", map[string]Cursor{"2002": {Watermark: "30"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := reopened.Load("acc-b"); !reflect.DeepEqual(got, b) {
		t.Errorf("覆盖其他账号后 acc-b 丢失：%+v", got)
	}
}

// TestFileCursorStoreCorruptFile 坏文件按「没有水位」处理，且能重新写入。
func TestFileCursorStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-cursors.json")
	if err := os.WriteFile(path, []byte(`{"accounts":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := NewFileCursorStore(path)
	got, err := store.Load("acc")
	if err != nil || len(got) != 0 {
		t.Fatalf("坏文件应降级为空表：%+v / %v", got, err)
	}
	want := map[string]Cursor{"2002": {Watermark: "5"}}
	if err := store.Save("acc", want); err != nil {
		t.Fatalf("坏文件后应能重建：%v", err)
	}
	if got, _ := store.Load("acc"); !reflect.DeepEqual(got, want) {
		t.Errorf("重建后读取不符：%+v", got)
	}
}

// TestFileCursorStoreFiltersInvalid 非法 peer/游标不进入水位，避免卡死采集。
func TestFileCursorStoreFiltersInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-cursors.json")
	body := `{"version":1,"accounts":{"acc":{
		"2002":{"watermark":"10","frontier":"20"},
		"3003":{"watermark":"abc"},
		"4004":{"watermark":"1","pending_from":"-1"},
		"":{"watermark":"1"}
	}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := NewFileCursorStore(path)
	got, err := store.Load("acc")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Cursor{"2002": {Watermark: "10", Frontier: "20"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("应只保留合法条目：%+v", got)
	}
}

// TestFileCursorStoreDefaultPath 空路径落到 state/dm-cursors.json。
func TestFileCursorStoreDefaultPath(t *testing.T) {
	store, err := NewFileCursorStore("  ")
	if err != nil {
		t.Fatal(err)
	}
	if store.path != defaultCursorPath {
		t.Errorf("默认路径 = %q，期望 %q", store.path, defaultCursorPath)
	}
}

// TestFileCursorStoreRejectsBadPath 路径带控制字符直接拒绝。
func TestFileCursorStoreRejectsBadPath(t *testing.T) {
	for _, path := range []string{"a\x00b", "a\nb"} {
		if _, err := NewFileCursorStore(path); err == nil || !strings.Contains(err.Error(), "非法") {
			t.Errorf("路径 %q 应拒绝，实际 %v", path, err)
		}
	}
}

// TestMemoryCursorStoreIsolation 内存实现存取都做副本，调用方改动不串扰。
func TestMemoryCursorStoreIsolation(t *testing.T) {
	store := NewMemoryCursorStore()
	src := map[string]Cursor{"2002": {Watermark: "1"}}
	if err := store.Save("acc", src); err != nil {
		t.Fatal(err)
	}
	src["2002"] = Cursor{Watermark: "9"}

	got, err := store.Load("acc")
	if err != nil {
		t.Fatal(err)
	}
	if got["2002"].Watermark != "1" {
		t.Fatalf("保存后被调用方改动不应影响存储：%+v", got)
	}
	got["2002"] = Cursor{Watermark: "7"}
	again, _ := store.Load("acc")
	if again["2002"].Watermark != "1" {
		t.Fatalf("返回值改动不应影响存储：%+v", again)
	}
}
