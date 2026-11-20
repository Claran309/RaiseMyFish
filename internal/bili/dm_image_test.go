package bili

// 私信图片消息（msg_type=2）解析：URL 支持字符串与数组两种形态，
// 纯图片消息不再被当成「非文本」丢弃。

import (
	"context"
	"net/http"
	"testing"
)

func TestDMSessionMessagesParsesImages(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"messages":[
			{"msg_key":"9101","msg_type":2,"sender_uid":"2002","content":"{\"url\":\"https://i0.hdslb.com/bfs/a.jpg\",\"width\":100,\"height\":80}","timestamp":1700000000},
			{"msg_key":"9102","msg_type":"2","sender_uid":"2002","content":"{\"url\":[\"https://i0.hdslb.com/bfs/b.jpg\",\"https://i0.hdslb.com/bfs/c.jpg\"]}","timestamp":1700000001},
			{"msg_key":"9103","msg_type":1,"sender_uid":"2002","content":"{\"content\":\"文字消息\"}","timestamp":1700000002},
			{"msg_key":"9104","msg_type":2,"sender_uid":"2002","content":"not-json","timestamp":1700000003}
		]}}`))
	})

	got, err := c.DMSessionMessages(context.Background(), "2002", 50)
	if err != nil {
		t.Fatalf("拉详情失败：%v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应保留 2 条图片 + 1 条文本，实际 %d：%+v", len(got), got)
	}
	if got[0].MsgKey != "9101" || len(got[0].Images) != 1 || got[0].Images[0] != "https://i0.hdslb.com/bfs/a.jpg" {
		t.Errorf("单图消息解析不符：%+v", got[0])
	}
	if got[0].Content != "" {
		t.Errorf("图片消息正文应为空：%q", got[0].Content)
	}
	if got[1].MsgKey != "9102" || len(got[1].Images) != 2 {
		t.Errorf("多图消息解析不符：%+v", got[1])
	}
	if got[2].Content != "文字消息" || len(got[2].Images) != 0 {
		t.Errorf("文本消息解析不符：%+v", got[2])
	}
}
