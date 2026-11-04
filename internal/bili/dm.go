package bili

// 本文件是私信相关接口：会话列表与发消息。
//
// 边界与 bili 包其余部分一致：只发一次请求、不重试、错误不带 cookie。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DMSession 是一条「会话 + 最近消息」。
type DMSession struct {
	// TalkerID 是对端 uid。
	TalkerID string
	// MsgKey 是最近一条消息 id（去重键）。
	MsgKey string
	// SenderUID 是最近一条消息的发送者。
	SenderUID string
	// Content 是正文。
	Content string
	// Timestamp 是平台秒级时间戳。
	Timestamp int64
}

// DMSessions 拉会话列表；limit<=0 用 20。
func (c *Client) DMSessions(ctx context.Context, limit int) ([]DMSession, error) {
	if c == nil {
		return nil, ErrNoCredential
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	endpoint := c.vc + "/session_svr/v1/session_svr/get_sessions?session_type=1&group_id=0&end_point=" + url.QueryEscape("[]")
	var data struct {
		SessionList []struct {
			TalkerID json.RawMessage `json:"talker_id"`
			LastMsg  struct {
				MsgKey    json.RawMessage `json:"msg_key"`
				SenderUID json.RawMessage `json:"sender_uid"`
				Content   string          `json:"content"`
				Timestamp int64           `json:"timestamp"`
			} `json:"last_msg"`
		} `json:"session_list"`
	}
	if err := c.do(ctx, "dm_sessions", http.MethodGet, endpoint, nil, &data); err != nil {
		return nil, err
	}
	out := make([]DMSession, 0, len(data.SessionList))
	for _, s := range data.SessionList {
		if len(out) >= limit {
			break
		}
		item := DMSession{
			TalkerID:  rawJSONID(s.TalkerID),
			MsgKey:    rawJSONID(s.LastMsg.MsgKey),
			SenderUID: rawJSONID(s.LastMsg.SenderUID),
			Content:   strings.TrimSpace(s.LastMsg.Content),
			Timestamp: s.LastMsg.Timestamp,
		}
		if item.TalkerID == "" || item.MsgKey == "" || item.Content == "" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// SendMessage 发一条私信，返回平台 msg_key。
func (c *Client) SendMessage(ctx context.Context, mid, text string) (string, error) {
	if err := c.writable("dm_send"); err != nil {
		return "", err
	}
	if !isDigits(mid) {
		return "", errors.New("mid 必须是纯数字")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("私信内容不能为空")
	}
	if c.selfMID == "" || !isDigits(c.selfMID) {
		return "", errors.New("缺少本账号UID，无法发私信")
	}
	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return "", errors.New("私信内容编码失败")
	}
	form := url.Values{}
	form.Set("msg[sender_uid]", c.selfMID)
	form.Set("msg[receiver_id]", mid)
	form.Set("msg[receiver_type]", strconv.Itoa(receiverTypeUser))
	form.Set("msg[msg_type]", strconv.Itoa(msgTypeText))
	form.Set("msg[content]", string(content))
	form.Set("csrf", c.csrf)
	form.Set("csrf_token", c.csrf)

	var data struct {
		MsgKey json.RawMessage `json:"msg_key"`
	}
	if err := c.do(ctx, "dm_send", http.MethodPost, c.vc+"/web_im/v1/web_im/send_msg", form, &data); err != nil {
		return "", err
	}
	return rawJSONID(data.MsgKey), nil
}

// RelationAttribute 查关注关系：6=互关，2=已关注，0=未关注。
func (c *Client) RelationAttribute(ctx context.Context, mid string) (int, error) {
	if c == nil {
		return 0, ErrNoCredential
	}
	if !isDigits(mid) {
		return 0, errors.New("mid 必须是纯数字")
	}
	endpoint := c.api + "/x/relation?fid=" + url.QueryEscape(mid)
	var data struct {
		Attribute int `json:"attribute"`
	}
	if err := c.do(ctx, "relation_query", http.MethodGet, endpoint, nil, &data); err != nil {
		return 0, err
	}
	return data.Attribute, nil
}

// rawJSONID 兼容 "123"/123 两种形态。
func rawJSONID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}
