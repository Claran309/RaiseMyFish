package bili

// 本文件是账号消息接口：别人回复我的评论 / 在评论里 @我。
//
// 与包内其余接口一致：只发一次请求、不重试；错误不带 cookie/正文。
// msgfeed 不需要 WBI，但需要登录 Cookie 与消息页 Referer。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	msgfeedReplyPath = "/x/msgfeed/reply"
	msgfeedAtPath    = "/x/msgfeed/at"
)

// MsgfeedItem 是一条账号消息（规整后）。
type MsgfeedItem struct {
	// ID 是通知本身的 id，用于翻页与同秒去重。
	ID string
	// ItemType 是 item.type：评论相关为 reply。
	ItemType string
	// BusinessID 是评论区类型（1 视频 / 11 图文 / 12 专栏 / 17 动态）。
	BusinessID int
	// SubjectID 是评论区所在对象的 oid。
	SubjectID string
	// SourceID 是「回复我的那条评论」的 rpid，也是回复锚点（parent）。
	SourceID string
	// RootID 是楼中楼线程根；顶层评论为空。
	RootID string
	// UserMID / UserName 是发消息的人。
	UserMID  string
	UserName string
	// Text 是回复/@ 的正文。
	Text string
	// Time 是消息时间（秒）。
	Time int64
}

// MsgfeedPage 是一页消息；Cursor 用于继续向更旧的方向翻页。
type MsgfeedPage struct {
	Items      []MsgfeedItem
	CursorID   string
	CursorTime int64
	IsEnd      bool
	// LastViewAt 是账号上次查看消息页的时间（秒），首次接入用它划基线。
	LastViewAt int64
}

// MsgfeedReplies 拉「回复我的」；lastID/lastTime 非空时从该游标继续向旧翻页。
func (c *Client) MsgfeedReplies(ctx context.Context, lastID string, lastTime int64) (MsgfeedPage, error) {
	return c.msgfeed(ctx, "msgfeed_reply", msgfeedReplyPath, "reply_time", lastID, lastTime)
}

// MsgfeedAts 拉「@我的」。
func (c *Client) MsgfeedAts(ctx context.Context, lastID string, lastTime int64) (MsgfeedPage, error) {
	return c.msgfeed(ctx, "msgfeed_at", msgfeedAtPath, "at_time", lastID, lastTime)
}

func (c *Client) msgfeed(ctx context.Context, op, path, timeParam, lastID string, lastTime int64) (MsgfeedPage, error) {
	params := url.Values{}
	params.Set("build", "0")
	params.Set("mobi_app", "web")
	lastID = strings.TrimSpace(lastID)
	if lastID != "" && isDigits(lastID) {
		params.Set("id", lastID)
	}
	if lastTime > 0 {
		params.Set(timeParam, strconv.FormatInt(lastTime, 10))
	}
	var data struct {
		Items []struct {
			ID   json.RawMessage `json:"id"`
			User struct {
				MID      json.RawMessage `json:"mid"`
				Nickname string          `json:"nickname"`
			} `json:"user"`
			Item struct {
				Type          string          `json:"type"`
				BusinessID    json.RawMessage `json:"business_id"`
				SubjectID     json.RawMessage `json:"subject_id"`
				SourceID      json.RawMessage `json:"source_id"`
				RootID        json.RawMessage `json:"root_id"`
				SourceContent string          `json:"source_content"`
			} `json:"item"`
			ReplyTime int64 `json:"reply_time"`
			AtTime    int64 `json:"at_time"`
		} `json:"items"`
		Cursor struct {
			IsEnd bool            `json:"is_end"`
			ID    json.RawMessage `json:"id"`
			Time  int64           `json:"time"`
		} `json:"cursor"`
		LastViewAt int64 `json:"last_view_at"`
	}
	if err := c.do(ctx, op, http.MethodGet, c.api+path+"?"+params.Encode(), nil, &data); err != nil {
		return MsgfeedPage{}, err
	}
	page := MsgfeedPage{
		Items:      make([]MsgfeedItem, 0, len(data.Items)),
		CursorID:   msgfeedID(data.Cursor.ID),
		CursorTime: data.Cursor.Time,
		IsEnd:      data.Cursor.IsEnd,
		LastViewAt: data.LastViewAt,
	}
	for _, raw := range data.Items {
		at := raw.ReplyTime
		if at <= 0 {
			at = raw.AtTime
		}
		page.Items = append(page.Items, MsgfeedItem{
			ID:         msgfeedID(raw.ID),
			ItemType:   strings.TrimSpace(raw.Item.Type),
			BusinessID: parseBusinessID(raw.Item.BusinessID),
			SubjectID:  msgfeedID(raw.Item.SubjectID),
			SourceID:   msgfeedID(raw.Item.SourceID),
			RootID:     msgfeedID(raw.Item.RootID),
			UserMID:    msgfeedID(raw.User.MID),
			UserName:   strings.TrimSpace(raw.User.Nickname),
			Text:       raw.Item.SourceContent,
			Time:       at,
		})
	}
	return page, nil
}

// msgfeedID 把数字/字符串 id 统一成字符串；平台用 0 表示「没有」。
func msgfeedID(raw json.RawMessage) string {
	id := rawJSONID(raw)
	if id == "0" {
		return ""
	}
	return id
}

// parseBusinessID 兼容平台把 business_id 写成数字或数字字符串。
func parseBusinessID(raw json.RawMessage) int {
	id := rawJSONID(raw)
	if id == "" {
		return 0
	}
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// SelfMID 返回当前凭据对应的账号 UID（只解析本地凭据，不联网）。
func (c *Client) SelfMID(ctx context.Context) (string, error) {
	if c == nil {
		return "", ErrNoCredential
	}
	cred, err := c.resolve(ctx)
	if err != nil {
		return "", wrapResolveError("self_mid", err)
	}
	mid := strings.TrimSpace(cred.selfMID)
	if !isDigits(mid) || mid == "0" {
		return "", errors.New("缺少本账号UID")
	}
	return mid, nil
}
