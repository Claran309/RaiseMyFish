package knowledge

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFirstBatchLoadsAndRecallsEveryTitle(t *testing.T) {
	r, err := NewFileReader(context.Background(), filepath.Join("..", "..", "personas", "dafeiyu", "knowledge"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.entries) < 500 {
		t.Fatal("已审核首批500条缺失")
	}
	for _, e := range r.entries {
		got := recallIDs(t, r, publicScope(), e.title)
		found := false
		for _, id := range got {
			if id == e.item.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("原词未召回自己的条目：%s", e.item.ID)
		}
	}
}
