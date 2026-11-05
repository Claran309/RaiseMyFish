package reasoning

import "testing"

// TestJSONToolArgs MCP 传入的 JSON 对象应被旧参数解析器正确读取（正文可含空格与引号）。
func TestJSONToolArgs(t *testing.T) {
	mid, text, err := parseDMArgs(`{"mid":"12345","text":"你好 \"世界\" 今天吃了吗"}`)
	if err != nil {
		t.Fatalf("私信 JSON 参数解析失败：%v", err)
	}
	if mid != "12345" || text != `你好 "世界" 今天吃了吗` {
		t.Fatalf("私信参数不符：mid=%q text=%q", mid, text)
	}

	req, err := parseGiftArgs(`{"gift_id":"31036","num":2}`, "room1", "1")
	if err != nil {
		t.Fatalf("送礼 JSON 参数解析失败：%v", err)
	}
	if req.GiftID != "31036" || req.Num != 2 || req.Room != "room1" {
		t.Fatalf("送礼参数不符：%+v", req)
	}

	mids, action, err := parseFollowArgs(`{"mid":["1","2"],"action":"unfollow"}`)
	if err != nil {
		t.Fatalf("关注 JSON 参数解析失败：%v", err)
	}
	if action != "unfollow" || len(mids) != 2 || mids[0] != "1" || mids[1] != "2" {
		t.Fatalf("关注参数不符：%v %s", mids, action)
	}

	out, err := parseTextArg(`{"text":"带 \"引号\" 和换行\n正文"}`, 200)
	if err != nil || out != "带 \"引号\" 和换行\n正文" {
		t.Fatalf("正文 JSON 参数解析失败：%q %v", out, err)
	}
}

// TestParseLimitJSON history 工具接受 {"limit":n}。
func TestParseLimitJSON(t *testing.T) {
	if got := parseLimit(`{"limit":5}`, 10, 20); got != 5 {
		t.Fatalf("JSON limit 解析失败：%d", got)
	}
	if got := parseLimit(`{"limit":99}`, 10, 20); got != 10 {
		t.Fatalf("越界 limit 应回落默认：%d", got)
	}
	if got := parseLimit("7", 10, 20); got != 7 {
		t.Fatalf("旧格式 limit 解析失败：%d", got)
	}
}
