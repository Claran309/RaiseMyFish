package main

// 私信图片：Meta 解析与「评论视觉复用」的边界。

import (
	"encoding/json"
	"testing"

	"RaiseMyFish/internal/event"
)

func TestDMPicsParsesEncodedArray(t *testing.T) {
	// 采集层写入的是「JSON 字符串里套 JSON 数组」。
	meta := json.RawMessage(`{"images":"[\"https://i0.hdslb.com/bfs/a.jpg\",\"https://i0.hdslb.com/bfs/b.jpg\"]"}`)
	got := dmPics(meta)
	if len(got) != 2 || got[0] != "https://i0.hdslb.com/bfs/a.jpg" {
		t.Fatalf("解析不符：%+v", got)
	}
}

func TestDMPicsAcceptsRawArray(t *testing.T) {
	meta := json.RawMessage(`{"images":["https://i0.hdslb.com/bfs/a.jpg"]}`)
	got := dmPics(meta)
	if len(got) != 1 {
		t.Fatalf("数组形态解析不符：%+v", got)
	}
}

func TestDMPicsEmptyAndBroken(t *testing.T) {
	cases := []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"images":"not-json"}`),
		json.RawMessage(`not-json`),
	}
	for _, meta := range cases {
		if got := dmPics(meta); len(got) != 0 {
			t.Errorf("坏 Meta 应返回空：%s -> %+v", meta, got)
		}
	}
}

func TestVisionActionDistinguishesChannels(t *testing.T) {
	if got := visionAction(event.Event{Channel: event.ChDM}); got != "dm_vision" {
		t.Errorf("私信 action 不符：%s", got)
	}
	if got := visionAction(event.Event{Channel: event.ChComment}); got != "comment_vision" {
		t.Errorf("评论 action 不符：%s", got)
	}
}
