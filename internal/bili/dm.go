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
	// MsgSeqno 是最近一条消息的序列号；平台未返回时为空。
	MsgSeqno string
	// MaxSeqno 是会话内最大序列号；平台未返回时为空。
	MaxSeqno string
}

// DMSessionPage 是一页会话列表。
type DMSessionPage struct {
	Sessions []DMSession
	HasMore  bool
}

// DMSessionsPage 拉一页会话；size<=0 用 20，最大 100。
// endTS 是微秒级结束时间游标（0 表示从最新开始），用于向更早的会话翻页。
func (c *Client) DMSessionsPage(ctx context.Context, size int, endTS int64) (DMSessionPage, error) {
	if c == nil {
		return DMSessionPage{}, ErrNoCredential
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	params := url.Values{}
	params.Set("session_type", "1")
	params.Set("group_id", "0")
	params.Set("end_point", "[]")
	params.Set("size", strconv.Itoa(size))
	if endTS > 0 {
		params.Set("end_ts", strconv.FormatInt(endTS, 10))
	}
	endpoint := c.vc + "/session_svr/v1/session_svr/get_sessions?" + params.Encode()
	var data struct {
		SessionList []struct {
			TalkerID json.RawMessage `json:"talker_id"`
			MaxSeqno json.RawMessage `json:"max_seqno"`
			LastMsg  struct {
				MsgKey    json.RawMessage `json:"msg_key"`
				MsgSeqno  json.RawMessage `json:"msg_seqno"`
				SenderUID json.RawMessage `json:"sender_uid"`
				Content   string          `json:"content"`
				Timestamp int64           `json:"timestamp"`
			} `json:"last_msg"`
		} `json:"session_list"`
		HasMore json.RawMessage `json:"has_more"`
	}
	if err := c.do(ctx, "dm_sessions", http.MethodGet, endpoint, nil, &data); err != nil {
		return DMSessionPage{}, err
	}
	out := make([]DMSession, 0, len(data.SessionList))
	for _, s := range data.SessionList {
		// 与详情路径共用解包逻辑，详情失败降级时正文才不会带 JSON 包装。
		item := DMSession{
			TalkerID:  rawJSONID(s.TalkerID),
			MsgKey:    rawJSONID(s.LastMsg.MsgKey),
			SenderUID: rawJSONID(s.LastMsg.SenderUID),
			Content:   parseDMContent(s.LastMsg.Content),
			Timestamp: s.LastMsg.Timestamp,
			MsgSeqno:  rawJSONID(s.LastMsg.MsgSeqno),
			MaxSeqno:  rawJSONID(s.MaxSeqno),
		}
		if item.TalkerID == "" || item.MsgKey == "" || item.Content == "" {
			continue
		}
		out = append(out, item)
	}
	page := DMSessionPage{Sessions: out}
	if len(data.HasMore) > 0 {
		page.HasMore = looseTrue(data.HasMore)
	} else {
		// 旧版响应没有 has_more：按「取满一页」保守认为可能还有更早会话。
		page.HasMore = len(data.SessionList) >= size
	}
	return page, nil
}

// DMSessions 拉会话列表首页；limit<=0 用 20。
func (c *Client) DMSessions(ctx context.Context, limit int) ([]DMSession, error) {
	size := limit
	if size <= 0 || size > 100 {
		size = 20
	}
	page, err := c.DMSessionsPage(ctx, size, 0)
	if err != nil {
		return nil, err
	}
	return page.Sessions, nil
}

// DMMessage 是会话详情里的一条消息。
type DMMessage struct {
	MsgKey    string
	SenderUID string
	Content   string
	Timestamp int64
	// Seqno 是会话内消息序列号（msg_seqno）；平台未返回时为空。
	Seqno string
}

// DMMessagePage 是一页会话消息。
type DMMessagePage struct {
	Messages []DMMessage
	HasMore  bool
	MinSeqno string
	MaxSeqno string
	// Count 是平台返回的原始条数（含被跳过的非文本消息）。
	Count int
}

// DMSessionMessagesPage 拉一页会话消息（get_session_msgs）。
//
// endSeqno 为空时从最新开始；非空时只取比它更早的消息（游标向历史翻页）。
// size<=0 用 50，最大 200。文本取 content 字段，非文本/空正文跳过，
// 但 MinSeqno/MaxSeqno 仍按原始消息计算，避免非文本消息挡住水位。
func (c *Client) DMSessionMessagesPage(ctx context.Context, talkerID string, size int, endSeqno string) (DMMessagePage, error) {
	if c == nil {
		return DMMessagePage{}, ErrNoCredential
	}
	if !isDigits(talkerID) {
		return DMMessagePage{}, errors.New("私信会话 talker_id 必须是纯数字")
	}
	endSeqno = strings.TrimSpace(endSeqno)
	if endSeqno != "" && !isDigits(endSeqno) {
		return DMMessagePage{}, errors.New("私信消息游标必须是纯数字")
	}
	if size <= 0 || size > 200 {
		size = 50
	}
	params := url.Values{}
	params.Set("talker_id", talkerID)
	params.Set("session_type", "1")
	params.Set("size", strconv.Itoa(size))
	if endSeqno != "" {
		// 新版接口用 begin_seqno 指定起点（旧 end_seqno 已随旧路径 404）。
		params.Set("begin_seqno", endSeqno)
	}
	// B站 2025 私信消息接口迁移：session_svr/get_session_msgs → svr_sync/fetch_session_msgs，
	// 需要 WBI 签名（旧路径返回 HTTP 404）。
	endpoint, err := c.signWBI(ctx, "dm_msgs", c.vc+"/svr_sync/v1/svr_sync/fetch_session_msgs", params)
	if err != nil {
		return DMMessagePage{}, err
	}
	var data struct {
		Messages []struct {
			MsgKey    json.RawMessage `json:"msg_key"`
			MsgSeqno  json.RawMessage `json:"msg_seqno"`
			SenderUID json.RawMessage `json:"sender_uid"`
			Content   string          `json:"content"`
			Timestamp int64           `json:"timestamp"`
		} `json:"messages"`
		HasMore  json.RawMessage `json:"has_more"`
		MinSeqno json.RawMessage `json:"min_seqno"`
		MaxSeqno json.RawMessage `json:"max_seqno"`
	}
	if err := c.do(ctx, "dm_session_msgs", http.MethodGet, endpoint, nil, &data); err != nil {
		return DMMessagePage{}, err
	}
	page := DMMessagePage{Count: len(data.Messages)}
	for _, m := range data.Messages {
		seq := rawJSONID(m.MsgSeqno)
		if seq != "" {
			page.MinSeqno = minDecimalID(page.MinSeqno, seq)
			page.MaxSeqno = maxDecimalID(page.MaxSeqno, seq)
		}
		key, sender, text := rawJSONID(m.MsgKey), rawJSONID(m.SenderUID), parseDMContent(m.Content)
		if key == "" || sender == "" || text == "" {
			continue
		}
		page.Messages = append(page.Messages, DMMessage{
			MsgKey:    key,
			SenderUID: sender,
			Content:   text,
			Timestamp: m.Timestamp,
			Seqno:     seq,
		})
	}
	// 极值优先按本页实际消息计算：响应里的 min/max 若被平台定义成整会话范围，
	// 直接采用会让游标过早停在新页上。仅在本页没有可用 seqno 时回退到响应字段。
	if page.MinSeqno == "" {
		page.MinSeqno = rawJSONID(data.MinSeqno)
	}
	if page.MaxSeqno == "" {
		page.MaxSeqno = rawJSONID(data.MaxSeqno)
	}
	if len(data.HasMore) > 0 {
		page.HasMore = looseTrue(data.HasMore)
	} else {
		// 旧版响应没有 has_more：按「取满一页」保守认为可能还有更早消息。
		page.HasMore = page.Count >= size
	}
	return page, nil
}

// DMSessionMessages 拉某个会话的最近消息；limit<=0 用 50。
func (c *Client) DMSessionMessages(ctx context.Context, talkerID string, limit int) ([]DMMessage, error) {
	size := limit
	if size <= 0 || size > 200 {
		size = 50
	}
	page, err := c.DMSessionMessagesPage(ctx, talkerID, size, "")
	if err != nil {
		return nil, err
	}
	return page.Messages, nil
}

// parseDMContent 取文本消息正文；平台把正文包在 JSON 字符串里（{"content":"..."}）。
func parseDMContent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "{") {
		var obj struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			return strings.TrimSpace(obj.Content)
		}
	}
	return raw
}

// SendMessage 发一条私信，返回平台 msg_key。
func (c *Client) SendMessage(ctx context.Context, mid, text string) (string, error) {
	if !isDigits(mid) {
		return "", errors.New("mid 必须是纯数字")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("私信内容不能为空")
	}
	cred, err := c.resolve(ctx)
	if err != nil {
		return "", wrapResolveError("dm_send", err)
	}
	if err := requireCSRF("dm_send", cred.csrf); err != nil {
		return "", err
	}
	if cred.selfMID == "" || !isDigits(cred.selfMID) {
		return "", errors.New("缺少本账号UID，无法发私信")
	}
	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return "", errors.New("私信内容编码失败")
	}
	form := url.Values{}
	form.Set("msg[sender_uid]", cred.selfMID)
	form.Set("msg[receiver_id]", mid)
	form.Set("msg[receiver_type]", strconv.Itoa(receiverTypeUser))
	form.Set("msg[msg_type]", strconv.Itoa(msgTypeText))
	form.Set("msg[content]", string(content))
	form.Set("csrf", cred.csrf)
	form.Set("csrf_token", cred.csrf)

	var data struct {
		MsgKey json.RawMessage `json:"msg_key"`
	}
	if err := c.send(ctx, cred, "dm_send", http.MethodPost, c.vc+"/web_im/v1/web_im/send_msg", form, &data); err != nil {
		return "", err
	}
	// code=0 只说明平台明确接受；缺/零/非法 msg_key 是「接受但未验证」，
	// 交给上层按缺稳定回执处理，不能在这里伪造一个回执。
	return stableMsgID(data.MsgKey), nil
}

// stableMsgID 提取可核验的 msg_key：必须是纯数字且非零。
// 缺失、对象、空串、全零都不算稳定回执。
func stableMsgID(raw json.RawMessage) string {
	id := rawJSONID(raw)
	if !isDigits(id) || strings.Trim(id, "0") == "" {
		return ""
	}
	return id
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

// looseTrue 兼容 has_more 的 0/1、true/false 与字符串形态。
func looseTrue(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "1" || s == "true" || s == `"1"` || s == `"true"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// compareDecimalID 比较两个非负十进制串；空串按 0 处理。
func compareDecimalID(a, b string) int {
	if a == "" {
		a = "0"
	}
	if b == "" {
		b = "0"
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func minDecimalID(a, b string) string {
	if a == "" || b == "" {
		return firstNonEmpty(a, b)
	}
	if compareDecimalID(a, b) <= 0 {
		return a
	}
	return b
}

func maxDecimalID(a, b string) string {
	if a == "" || b == "" {
		return firstNonEmpty(a, b)
	}
	if compareDecimalID(a, b) >= 0 {
		return a
	}
	return b
}
