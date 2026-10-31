package personaeval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpectationRevisionDoesNotRegenerateOrRewriteSource(t *testing.T) {
	s, err := NewScorerVersion("persona-lexical-v3")
	if err != nil {
		t.Fatal(err)
	}
	source := "../../plans/reports/persona-eval/20261031T143439Z-758949f2"
	revision := "../../plans/reports/p0h-cases/cases-v3.jsonl"
	original, err := os.ReadFile(filepath.Join(source, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(revision)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "revised")
	r, err := RescoreWithExpectations(source, out, s, revision, Digest(data))
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 35 || r.RawPassed != 15 || r.ActualModelCalls != 0 || r.NewModelValidation {
		t.Fatalf("修订重评统计/来源错误：%+v", r)
	}
	after, err := os.ReadFile(filepath.Join(source, "report.json"))
	if err != nil || string(after) != string(original) {
		t.Fatal("改写来源报告")
	}
	snapshot, err := os.ReadFile(filepath.Join(out, "cases-revision.jsonl"))
	if err != nil || string(snapshot) != string(data) {
		t.Fatal("未保存修订原字节")
	}
	for _, mutate := range []func(*Case){
		func(c *Case) { c.Input = "改了输入" }, func(c *Case) { c.ID = "different" }, func(c *Case) { c.Kind = "promise" }, func(c *Case) { c.Channel = "dm" }, func(c *Case) { c.Private = !c.Private }, func(c *Case) { c.Forbid = nil }, func(c *Case) { c.Expect = nil },
	} {
		cases, err := parseCases(data)
		if err != nil {
			t.Fatal(err)
		}
		mutate(&cases[0])
		var encoded []byte
		for _, c := range cases {
			line, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, append(line, '\n')...)
		}
		path := filepath.Join(t.TempDir(), "bad.jsonl")
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := RescoreWithExpectations(source, filepath.Join(t.TempDir(), "must-not-exist"), s, path, Digest(encoded)); err == nil {
			t.Fatal("允许了非expect变更或空expect")
		}
	}
	if _, err := RescoreWithExpectations(source, filepath.Join(t.TempDir(), "wrong-hash"), s, revision, "bad"); err == nil {
		t.Fatal("hash错配未拒绝")
	}
	if _, err := RescoreWithExpectations(source, out, s, revision, Digest(data)); err == nil {
		t.Fatal("覆盖已生成重评目录")
	}
}
