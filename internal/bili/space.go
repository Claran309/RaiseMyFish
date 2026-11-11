package bili

// 本文件是「自己账号内容发现」接口：视频列表与动态列表。
//
// 视频列表需要 WBI 签名（mid + wts + w_rid）；动态列表走 web-dynamic feed。
// 与包内其余接口一致：只发一次请求、不重试、错误不带 cookie/正文。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const wbiKeyTTL = time.Hour

// mixinKeyEncTab 是 B站 WBI 固定的 64 位置换表。
var mixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61,
	26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36,
	20, 34, 44, 52,
}

// SpaceVideo 是空间视频列表的一集。
type SpaceVideo struct {
	AID     string
	BVID    string
	Title   string
	Created int64
}

// SpaceDynamic 是空间动态列表的一条。
type SpaceDynamic struct {
	ID   string
	Type string
	// CommentID 是该动态评论区的 oid（平台 basic.comment_id_str），
	// 视频/专栏动态可能与动态 ID 不同；缺失时调用方回落到 ID。
	CommentID string
	// CommentType 是评论区类型（basic.comment_type）：11=图文动态、17=普通动态，
	// 视频/专栏动态会是 1/12。0 表示平台未给出。
	CommentType int
}

// SpaceVideos 拉当前账号最近投稿（按发布时间倒序）。
//
// mid 为空时用客户端记录的本账号 UID；limit 上限 50。
func (c *Client) SpaceVideos(ctx context.Context, mid string, limit int) ([]SpaceVideo, error) {
	if c == nil {
		return nil, ErrNoCredential
	}
	mid = strings.TrimSpace(mid)
	if mid == "" {
		cred, err := c.resolve(ctx)
		if err != nil {
			return nil, wrapResolveError("space_videos", err)
		}
		mid = cred.selfMID
	}
	if !isDigits(mid) {
		return nil, errors.New("mid 必须是纯数字")
	}
	if limit <= 0 || limit > 50 {
		limit = 30
	}
	params := url.Values{}
	params.Set("mid", mid)
	params.Set("ps", strconv.Itoa(limit))
	params.Set("pn", "1")
	params.Set("order", "pubdate")
	params.Set("platform", "web")
	endpoint, err := c.signWBI(ctx, "space_videos", c.api+"/x/space/wbi/arc/search", params)
	if err != nil {
		return nil, err
	}
	var data struct {
		List struct {
			VList []struct {
				AID     json.RawMessage `json:"aid"`
				BVID    string          `json:"bvid"`
				Title   string          `json:"title"`
				Created int64           `json:"created"`
			} `json:"vlist"`
		} `json:"list"`
	}
	if err := c.do(ctx, "space_videos", http.MethodGet, endpoint, nil, &data); err != nil {
		return nil, err
	}
	out := make([]SpaceVideo, 0, len(data.List.VList))
	for _, v := range data.List.VList {
		aid := rawJSONID(v.AID)
		if !isDigits(aid) || strings.Trim(aid, "0") == "" {
			continue
		}
		out = append(out, SpaceVideo{AID: aid, BVID: strings.TrimSpace(v.BVID), Title: strings.TrimSpace(v.Title), Created: v.Created})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// SpaceDynamics 拉当前账号的动态列表（含转发/图文）。
func (c *Client) SpaceDynamics(ctx context.Context, mid string, limit int) ([]SpaceDynamic, error) {
	if c == nil {
		return nil, ErrNoCredential
	}
	mid = strings.TrimSpace(mid)
	if mid == "" {
		cred, err := c.resolve(ctx)
		if err != nil {
			return nil, wrapResolveError("space_dynamics", err)
		}
		mid = cred.selfMID
	}
	if !isDigits(mid) {
		return nil, errors.New("mid 必须是纯数字")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	params := url.Values{}
	params.Set("host_mid", mid)
	params.Set("offset", "")
	params.Set("features", "itemOpusStyle")
	params.Set("platform", "web")
	params.Set("timezone_offset", "-480")
	endpoint := c.api + "/x/polymer/web-dynamic/v1/feed/space?" + params.Encode()
	var data struct {
		Items []struct {
			IDStr json.RawMessage `json:"id_str"`
			Type  string          `json:"type"`
			Basic struct {
				CommentIDStr json.RawMessage `json:"comment_id_str"`
				CommentType  int             `json:"comment_type"`
			} `json:"basic"`
		} `json:"items"`
	}
	if err := c.do(ctx, "space_dynamics", http.MethodGet, endpoint, nil, &data); err != nil {
		return nil, err
	}
	out := make([]SpaceDynamic, 0, len(data.Items))
	for _, item := range data.Items {
		id := rawJSONID(item.IDStr)
		if !isDigits(id) || strings.Trim(id, "0") == "" {
			continue
		}
		commentID := rawJSONID(item.Basic.CommentIDStr)
		if !isDigits(commentID) || strings.Trim(commentID, "0") == "" {
			commentID = ""
		}
		out = append(out, SpaceDynamic{
			ID:          id,
			Type:        strings.TrimSpace(item.Type),
			CommentID:   commentID,
			CommentType: item.Basic.CommentType,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// signWBI 取缓存的 mixin key 并给 params 追加 wts/w_rid，返回完整 URL。
func (c *Client) signWBI(ctx context.Context, op, endpoint string, params url.Values) (string, error) {
	mixin, err := c.wbiKey(ctx)
	if err != nil {
		return "", err
	}
	params.Set("wts", strconv.FormatInt(time.Now().Unix(), 10))
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var query strings.Builder
	for i, key := range keys {
		if i > 0 {
			query.WriteByte('&')
		}
		query.WriteString(url.QueryEscape(key))
		query.WriteByte('=')
		query.WriteString(url.QueryEscape(filterWBIValue(params.Get(key))))
	}
	sum := md5.Sum([]byte(query.String() + mixin))
	params.Set("w_rid", hex.EncodeToString(sum[:]))
	return endpoint + "?" + params.Encode(), nil
}

// wbiKey 返回缓存的 mixin key；缓存过期时从 nav 的 wbi_img 重新计算。
func (c *Client) wbiKey(ctx context.Context) (string, error) {
	c.wbiMu.Lock()
	defer c.wbiMu.Unlock()
	if c.wbiMixin != "" && time.Now().Before(c.wbiUntil) {
		return c.wbiMixin, nil
	}
	var data struct {
		WbiImg struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	}
	if err := c.do(ctx, "wbi_keys", http.MethodGet, c.api+"/x/web-interface/nav", nil, &data); err != nil {
		return "", err
	}
	mixin := wbiMixinKey(data.WbiImg.ImgURL, data.WbiImg.SubURL)
	if mixin == "" {
		return "", &Error{Op: "wbi_keys", Cause: errors.New("nav 未返回有效 WBI 密钥")}
	}
	c.wbiMixin = mixin
	c.wbiUntil = time.Now().Add(wbiKeyTTL)
	return mixin, nil
}

// wbiMixinKey 从 img_url/sub_url 的文件名按置换表拼出 32 位 key。
func wbiMixinKey(imgURL, subURL string) string {
	raw := wbiFileName(imgURL) + wbiFileName(subURL)
	if len(raw) < 64 {
		return ""
	}
	var b strings.Builder
	b.Grow(32)
	for _, index := range mixinKeyEncTab {
		if index >= len(raw) {
			continue
		}
		b.WriteByte(raw[index])
		if b.Len() == 32 {
			break
		}
	}
	return b.String()
}

func wbiFileName(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if ext := path.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

// filterWBIValue 按 WBI 规则去掉值里的 !'()* 字符。
func filterWBIValue(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '!', '\'', '(', ')', '*':
			return -1
		}
		return r
	}, value)
}
