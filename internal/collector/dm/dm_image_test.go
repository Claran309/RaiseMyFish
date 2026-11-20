package dm

// 纯图片私信：正文用「[图片]」占位，图片 URL 进 Meta，事件不丢。

import (
	"encoding/json"
	"testing"
	"time"
)

func TestToEventImageOnlyMessage(t *testing.T) {
	c := &Collector{seen: make(map[string]struct{})}
	ev, id, ok := c.toEvent(Message{
		ID:     "9201",
		PeerID: "2002",
		Text:   "",
		Images: []string{"https://i0.hdslb.com/bfs/a.jpg", "http://evil.example/x.jpg"},
	}, time.Unix(1700000000, 0), "1001")
	if !ok {
		t.Fatalf("纯图片消息应产出事件")
	}
	if ev.Text != "[图片]" {
		t.Errorf("正文占位不符：%q", ev.Text)
	}
	if id == "" || ev.Channel != "dm" || ev.FromID != "2002" {
		t.Errorf("事件字段不符：%+v", ev)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(ev.Meta, &meta); err != nil {
		t.Fatalf("Meta 解析失败：%v", err)
	}
	// 采集层把图片数组序列化成「JSON 字符串」放进 Meta（map[string]string）。
	var encoded string
	if err := json.Unmarshal(meta["images"], &encoded); err != nil {
		t.Fatalf("images 字段应为字符串：%v", err)
	}
	var urls []string
	if err := json.Unmarshal([]byte(encoded), &urls); err != nil {
		t.Fatalf("images 解析失败：%v", err)
	}
	if len(urls) != 1 || urls[0] != "https://i0.hdslb.com/bfs/a.jpg" {
		t.Errorf("只应保留 https 图片：%+v", urls)
	}
}

func TestToEventTextMessageHasNoImages(t *testing.T) {
	c := &Collector{seen: make(map[string]struct{})}
	ev, _, ok := c.toEvent(Message{ID: "9202", PeerID: "2002", Text: "你好"}, time.Unix(1700000000, 0), "1001")
	if !ok || ev.Text != "你好" {
		t.Fatalf("文本消息事件不符：ok=%v ev=%+v", ok, ev)
	}
	var meta map[string]json.RawMessage
	_ = json.Unmarshal(ev.Meta, &meta)
	if _, exists := meta["images"]; exists {
		t.Errorf("文本消息不应带 images：%s", ev.Meta)
	}
}
