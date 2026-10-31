package personaeval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareUsesExactCaseSnapshotAndRealPack(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) != 35 || len(f.Files) != 6 || f.Pack.Meta.Version != "v1.1" {
		t.Fatal("快照或真实用例不完整")
	}
	for _, file := range f.Files {
		if file.SHA256 == "" || file.Bytes == 0 {
			t.Fatal("缺少源文件哈希")
		}
	}
	if err := f.VerifySources(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsUnknownLabelsBeforeCallingGenerator(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cases.jsonl")
	if err := os.WriteFile(p, []byte(`{"id":"x","channel":"live","kind":"normal","input":"x","expect":["unknown"],"forbid":["长句"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), "../../personas/dafeiyu", p, s); err == nil {
		t.Fatal("未知标签没有在请求前拒绝")
	}
}
