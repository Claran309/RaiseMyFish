package comment

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFileCursorStoreRoundTrip 跨重启读写水位，键按 targetKey 隔离。
func TestFileCursorStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "comment-cursors.json")
	store, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cursors := map[string]Cursor{
		"1:1700001": {At: 1760000000, ID: "555", Covered: true},
		"11:abc":    {At: 1760000001, ID: "9"},
	}
	if err := store.Save(cursors); err != nil {
		t.Fatal(err)
	}
	again, err := NewFileCursorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := again.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded["1:1700001"].At != 1760000000 || loaded["1:1700001"].ID != "555" || !loaded["1:1700001"].Covered {
		t.Fatalf("水位不回读：%+v", loaded)
	}
	// 原子写：不留下 .tmp。
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("残留临时文件")
	}
}

// TestFileCursorStoreMissingAndCorrupt 文件不存在返回空表；损坏时不 panic。
func TestFileCursorStoreMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCursorStore(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded) != 0 {
		t.Fatalf("缺文件应返回空表：%+v %v", loaded, err)
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileCursorStore(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("损坏文件应报错")
	}
}

// TestStateForSeedsCursor 重启后新 targetState 直接继承水位。
func TestStateForSeedsCursor(t *testing.T) {
	c := &Collector{
		options:    Options{Concurrency: 1},
		targets:    map[string]*targetState{},
		cursorSeed: map[string]Cursor{"1:1700001": {At: 1760000000, ID: "555", Covered: true}},
	}
	st := c.stateFor(Target{OID: "1700001", Type: TypeVideo})
	if st.frontierAt != 1760000000 || st.frontierID != "555" || !st.covered {
		t.Fatalf("水位未灌入：%+v", st)
	}
}

// TestPersistCursorsOnlyAdvances 只写入有 frontier 的目标，且保留种子。
func TestPersistCursorsOnlyAdvances(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCursorStore(filepath.Join(dir, "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Collector{
		options:    Options{Concurrency: 1},
		cursors:    store,
		targets:    map[string]*targetState{},
		cursorSeed: map[string]Cursor{"1:old": {At: 1, ID: "1", Covered: true}},
	}
	c.targets["1:new"] = &targetState{frontierAt: 2, frontierID: "2", covered: true}
	c.targets["1:empty"] = &targetState{}
	c.persistCursors()
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded["1:old"].At != 1 || loaded["1:new"].At != 2 {
		t.Fatalf("落盘内容错误：%+v", loaded)
	}
}
