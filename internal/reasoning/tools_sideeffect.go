package reasoning

// 本文件实现 5 个副作用工具：送礼、关注、点赞、私信、发动态。
//
// 统一护栏（派发单）：
//   - 总开关 `[reasoning] allow_side_effect_tools`（默认 false）：关着时一律返回「未授权」，不执行任何动作；
//   - 每次调用记 `action="tool_side_effect"`（工具/参数摘要/结果/额度剩余）；
//   - 失败不重试——避免重复送礼、重复关注；
//   - 平台执行器未配置时明确返回「未配置」，不假装成功。

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// 副作用工具名。
const (
	ToolGift        = "gift"
	ToolFollow      = "follow"
	ToolLike        = "like"
	ToolDM          = "dm"
	ToolPostDynamic = "post_dynamic"
)

// 默认限频间隔。
const (
	defaultFollowInterval = time.Minute
	defaultLikeInterval   = time.Minute
	defaultDMInterval     = time.Minute
	defaultPostInterval   = 10 * time.Minute
	maxFollowBatch        = 5
	maxDMRunes            = 200
	maxDynamicRunes       = 200
)

// SideEffectLogAction 是副作用调用的统一日志 action 值。
const SideEffectLogAction = "tool_side_effect"

// GiftRequest 是一次送礼请求。
type GiftRequest struct {
	Room   string
	GiftID string
	Num    int
}

// GiftExecutor 真正执行送礼（实现方负责签名/风控）。
type GiftExecutor interface {
	SendGift(ctx context.Context, req GiftRequest) (string, error)
}

// SocialExecutor 执行关注/点赞/私信/动态。
type SocialExecutor interface {
	Follow(ctx context.Context, mid string) error
	Unfollow(ctx context.Context, mid string) error
	Like(ctx context.Context, room string, count int) error
	SendDM(ctx context.Context, mid, text string) error
	PostDynamic(ctx context.Context, text string) error
}

// QuotaReserver 是送礼额度（与 gift.QuotaStore 同形，便于直接适配）。
type QuotaReserver interface {
	Reserve(day string, num, limit int) error
	Release(day string, num int)
}

// SideEffectDeps 是副作用工具的全部依赖与护栏参数。
type SideEffectDeps struct {
	// Enabled 对应 `[reasoning] allow_side_effect_tools`；false 时所有副作用工具拒绝执行。
	Enabled bool

	Gift      GiftExecutor
	Social    SocialExecutor
	Quota     QuotaReserver
	QuotaView QuotaSource
	// GiftEnabled 是送礼开关的静态值；GiftEnabledFunc 非 nil 时以它为准（面板热切换）。
	GiftEnabled     bool
	GiftEnabledFunc func() bool
	GiftReal        bool
	GiftID          string
	MaxNum          int
	DailyLimit      int
	AllowedRooms    []string
	// AllowPostDynamic 是发动态的独立开关（默认关）。
	AllowPostDynamic bool
	// OwnerMID 是主人；永不取关。
	OwnerMID string
	// SelfMID 是本鱼自己的 mid；永不取关。
	SelfMID string
	// AllowedDMMIDs 是允许私信的 mid 白名单（主人始终允许）。
	AllowedDMMIDs []string
	// Room 是本会话房间号。
	Room string

	FollowInterval time.Duration
	LikeInterval   time.Duration
	DMInterval     time.Duration
	PostInterval   time.Duration

	Now func() time.Time
	Log *zap.Logger
}

// sideEffects 是副作用工具的执行状态（限频表）。
type sideEffects struct {
	deps SideEffectDeps
	mu   sync.Mutex
	last map[string]time.Time
}

// BuildSideEffectTools 装配 5 个副作用工具；全部已带护栏。
func BuildSideEffectTools(d SideEffectDeps) (*Toolbox, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = zap.NewNop()
	}
	d.FollowInterval = orDefault(d.FollowInterval, defaultFollowInterval)
	d.LikeInterval = orDefault(d.LikeInterval, defaultLikeInterval)
	d.DMInterval = orDefault(d.DMInterval, defaultDMInterval)
	d.PostInterval = orDefault(d.PostInterval, defaultPostInterval)
	if strings.TrimSpace(d.Room) == "" {
		d.Room = ""
	}
	s := &sideEffects{deps: d, last: make(map[string]time.Time)}

	tools := []Tool{
		{
			Name: ToolGift,
			Desc: "给本房间送礼物；args 传 {\"gift_id\":\"数字\",\"num\":1}（受额度/白名单/开关约束）",
			Run:  s.gift,
		},
		{
			Name: ToolFollow,
			Desc: "关注或取关用户；args 传 {\"mid\":\"数字或逗号分隔\",\"action\":\"follow|unfollow\"}（每次≤5个）",
			Run:  s.follow,
		},
		{
			Name: ToolLike,
			Desc: "给本房间点赞；args 传 {\"count\":1-3}",
			Run:  s.like,
		},
		{
			Name: ToolDM,
			Desc: "给主人或白名单发私信；args 传 {\"mid\":\"数字\",\"text\":\"≤200字\"}",
			Run:  s.dm,
		},
		{
			Name: ToolPostDynamic,
			Desc: "发一条动态；args 传 {\"text\":\"≤200字\"}（默认关闭）",
			Run:  s.postDynamic,
		},
	}
	return NewToolbox(tools...)
}

// gift 送礼：开关 → 白名单 → 数量 → 额度 → dry-run → 执行。
func (s *sideEffects) gift(ctx context.Context, args string) (string, error) {
	d := s.deps
	if err := s.authorized(); err != nil {
		return "", err
	}
	// 每次执行都读当前开关：面板关闭后，尚未投递的调用立即拒绝。
	// 已进入执行器的在途请求不受影响，仍按成功/失败核验记账。
	if !s.giftEnabled() {
		day := d.Now().Format("2006-01-02")
		s.log("gift", "gate=off", "disabled", s.quotaLeft(day))
		return "", fmt.Errorf("送礼开关已关闭，工具拒绝执行")
	}
	if d.Gift == nil {
		return "未配置送礼执行器", nil
	}
	req, err := parseGiftArgs(args, d.Room, d.GiftID)
	if err != nil {
		return "", err
	}
	if !roomAllowed(req.Room, d.AllowedRooms) {
		return "", fmt.Errorf("房间不在送礼白名单：%s", req.Room)
	}
	if d.MaxNum > 0 && req.Num > d.MaxNum {
		return "", fmt.Errorf("单次最多送 %d 个", d.MaxNum)
	}
	day := d.Now().Format("2006-01-02")
	if d.Quota != nil && d.DailyLimit > 0 {
		if err := d.Quota.Reserve(day, req.Num, d.DailyLimit); err != nil {
			s.log("gift", fmt.Sprintf("room=%s num=%d", req.Room, req.Num), "quota_denied", 0)
			return "", fmt.Errorf("额度不足：%w", err)
		}
	}
	// 非真实：只说明不执行，并退还刚占的额度
	if !d.GiftReal {
		if d.Quota != nil && d.DailyLimit > 0 {
			d.Quota.Release(day, req.Num)
		}
		out := fmt.Sprintf("dry-run：未真实送礼（real=%v），本轮不消耗额度", d.GiftReal)
		s.log("gift", fmt.Sprintf("room=%s num=%d", req.Room, req.Num), "dry_run", s.quotaLeft(day))
		return out, nil
	}
	out, err := d.Gift.SendGift(ctx, req)
	if err != nil {
		// 失败不重试，但把额度退回去，避免失败也算钱
		if d.Quota != nil && d.DailyLimit > 0 {
			d.Quota.Release(day, req.Num)
		}
		s.log("gift", fmt.Sprintf("room=%s num=%d", req.Room, req.Num), "failed", s.quotaLeft(day))
		return "", err
	}
	s.log("gift", fmt.Sprintf("room=%s num=%d", req.Room, req.Num), "ok", s.quotaLeft(day))
	return "已送出：" + out, nil
}

// follow 关注/取关：开关 → 参数 → 目标护栏 → 限频 → 执行。
func (s *sideEffects) follow(ctx context.Context, args string) (string, error) {
	d := s.deps
	if err := s.authorized(); err != nil {
		return "", err
	}
	if d.Social == nil {
		return "未配置关注执行器", nil
	}
	mids, action, err := parseFollowArgs(args)
	if err != nil {
		return "", err
	}
	if !s.allow("follow", d.FollowInterval) {
		return "", fmt.Errorf("关注操作太频繁，请稍后再试")
	}

	var done, skipped []string
	for _, mid := range mids {
		// 永不取关主人、永不取关自己
		if action == "unfollow" && (mid == strings.TrimSpace(d.OwnerMID) || mid == strings.TrimSpace(d.SelfMID)) {
			skipped = append(skipped, mid)
			continue
		}
		var err error
		if action == "unfollow" {
			err = d.Social.Unfollow(ctx, mid)
		} else {
			err = d.Social.Follow(ctx, mid)
		}
		if err != nil {
			// 失败不重试：整批中止，已成功的照实报告
			s.log("follow", fmt.Sprintf("%s %v", action, mids), "failed", 0)
			return "", fmt.Errorf("%s %s 失败：%w", action, mid, err)
		}
		done = append(done, mid)
	}
	result := fmt.Sprintf("%s 完成 %d 个", action, len(done))
	if len(skipped) > 0 {
		result += fmt.Sprintf("；跳过 %d 个（主人/自己不可取关）", len(skipped))
	}
	s.log("follow", fmt.Sprintf("%s %v", action, mids), "ok", 0)
	return result, nil
}

// like 点赞：开关 → 数量 → 限频。
func (s *sideEffects) like(ctx context.Context, args string) (string, error) {
	d := s.deps
	if err := s.authorized(); err != nil {
		return "", err
	}
	if d.Social == nil {
		return "未配置点赞执行器", nil
	}
	count, err := parseCountArg(args, 1, 3)
	if err != nil {
		return "", err
	}
	if !s.allow("like", d.LikeInterval) {
		return "", fmt.Errorf("点赞太频繁，请稍后再试")
	}
	if err := d.Social.Like(ctx, d.Room, count); err != nil {
		s.log("like", fmt.Sprintf("count=%d", count), "failed", 0)
		return "", err
	}
	s.log("like", fmt.Sprintf("count=%d", count), "ok", 0)
	return fmt.Sprintf("点了 %d 个赞", count), nil
}

// dm 私信：开关 → 白名单 → 长度 → 限频。
func (s *sideEffects) dm(ctx context.Context, args string) (string, error) {
	d := s.deps
	if err := s.authorized(); err != nil {
		return "", err
	}
	if d.Social == nil {
		return "未配置私信执行器", nil
	}
	mid, text, err := parseDMArgs(args)
	if err != nil {
		return "", err
	}
	if !dmAllowed(mid, d.OwnerMID, d.AllowedDMMIDs) {
		return "", fmt.Errorf("只允许私信主人或白名单用户")
	}
	if !s.allow("dm", d.DMInterval) {
		return "", fmt.Errorf("私信太频繁，请稍后再试")
	}
	if err := d.Social.SendDM(ctx, mid, text); err != nil {
		s.log("dm", "mid="+mid, "failed", 0)
		return "", err
	}
	s.log("dm", "mid="+mid, "ok", 0)
	return "私信已发送", nil
}

// postDynamic 发动态：开关 → 独立开关 → 长度 → 限频（10 分钟）。
func (s *sideEffects) postDynamic(ctx context.Context, args string) (string, error) {
	d := s.deps
	if err := s.authorized(); err != nil {
		return "", err
	}
	if !d.AllowPostDynamic {
		return "", fmt.Errorf("发动态未启用（默认关闭）")
	}
	if d.Social == nil {
		return "未配置动态执行器", nil
	}
	text, err := parseTextArg(args, maxDynamicRunes)
	if err != nil {
		return "", err
	}
	if !s.allow("post_dynamic", d.PostInterval) {
		return "", fmt.Errorf("发动态太频繁，请稍后再试")
	}
	if err := d.Social.PostDynamic(ctx, text); err != nil {
		s.log("post_dynamic", "runes="+strconv.Itoa(len([]rune(text))), "failed", 0)
		return "", err
	}
	s.log("post_dynamic", "runes="+strconv.Itoa(len([]rune(text))), "ok", 0)
	return "动态已发布", nil
}

// authorized 是总开关：关着时任何副作用都不执行。
func (s *sideEffects) authorized() error {
	if !s.deps.Enabled {
		return fmt.Errorf("未授权：副作用工具未启用（allow_side_effect_tools=false）")
	}
	return nil
}

// giftEnabled 读取当前送礼开关；面板热切换通过 GiftEnabledFunc 注入。
func (s *sideEffects) giftEnabled() bool {
	if s.deps.GiftEnabledFunc != nil {
		return s.deps.GiftEnabledFunc()
	}
	return s.deps.GiftEnabled
}

// allow 是限频：同一工具两次调用之间至少隔 interval。
func (s *sideEffects) allow(key string, interval time.Duration) bool {
	if interval <= 0 {
		return true
	}
	now := s.deps.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.last[key]; ok && now.Sub(last) < interval {
		return false
	}
	s.last[key] = now
	return true
}

// log 统一副作用日志（不含正文全文）。
func (s *sideEffects) log(tool, args, result string, quotaLeft int) {
	s.deps.Log.Info("副作用工具调用",
		zap.String("action", SideEffectLogAction),
		zap.String("tool", tool),
		zap.String("args", clip(args, 80)),
		zap.String("result", result),
		zap.Int("quota_left", quotaLeft),
	)
}

// quotaLeft 返回当天剩余额度；没配额度源时返回 -1（未知）。
func (s *sideEffects) quotaLeft(day string) int {
	if s.deps.QuotaView == nil || s.deps.DailyLimit <= 0 {
		return -1
	}
	used, err := s.deps.QuotaView.Used(day)
	if err != nil {
		return -1
	}
	if left := s.deps.DailyLimit - used; left > 0 {
		return left
	}
	return 0
}

func roomAllowed(room string, allowed []string) bool {
	room = strings.TrimSpace(room)
	if room == "" {
		return false
	}
	for _, a := range allowed {
		if strings.TrimSpace(a) == room {
			return true
		}
	}
	return false
}

func dmAllowed(mid, owner string, allowed []string) bool {
	mid = strings.TrimSpace(mid)
	if mid == "" {
		return false
	}
	if owner = strings.TrimSpace(owner); owner != "" && mid == owner {
		return true
	}
	for _, a := range allowed {
		if strings.TrimSpace(a) == mid {
			return true
		}
	}
	return false
}

func orDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

// parseGiftArgs 解析 {"gift_id":"31","num":1}；gift_id 缺省用配置值，num 缺省 1。
func parseGiftArgs(args, room, defaultGift string) (GiftRequest, error) {
	req := GiftRequest{Room: room, GiftID: defaultGift, Num: 1}
	fields := splitArgs(args)
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "gift_id":
			if i+1 < len(fields) {
				req.GiftID = fields[i+1]
				i++
			}
		case "num":
			if i+1 < len(fields) {
				n, err := strconv.Atoi(fields[i+1])
				if err != nil || n < 1 {
					return GiftRequest{}, fmt.Errorf("num 需为正整数")
				}
				req.Num = n
				i++
			}
		case "room":
			if i+1 < len(fields) {
				req.Room = fields[i+1]
				i++
			}
		}
	}
	if !isDigits(req.GiftID) {
		return GiftRequest{}, fmt.Errorf("gift_id 必须是纯数字")
	}
	return req, nil
}

// parseFollowArgs 解析 {"mid":"1,2,3","action":"follow|unfollow"}。
func parseFollowArgs(args string) ([]string, string, error) {
	fields := splitArgs(args)
	action := "follow"
	var mids []string
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "action":
			if i+1 < len(fields) {
				action = strings.ToLower(fields[i+1])
				i++
			}
		case "mid":
			if i+1 < len(fields) {
				mids = append(mids, strings.Split(fields[i+1], ",")...)
				i++
			}
		}
	}
	if len(mids) == 0 {
		// 允许直接传 mid
		for _, f := range fields {
			if isDigits(f) {
				mids = append(mids, f)
				break
			}
		}
	}
	if action != "follow" && action != "unfollow" {
		return nil, "", fmt.Errorf("action 只能是 follow 或 unfollow")
	}
	clean := make([]string, 0, len(mids))
	for _, m := range mids {
		m = strings.TrimSpace(m)
		if !isDigits(m) {
			return nil, "", fmt.Errorf("mid 必须是纯数字")
		}
		clean = append(clean, m)
	}
	if len(clean) == 0 {
		return nil, "", fmt.Errorf("缺少 mid")
	}
	if len(clean) > maxFollowBatch {
		return nil, "", fmt.Errorf("单次最多 %d 个", maxFollowBatch)
	}
	return clean, action, nil
}

// parseCountArg 解析 {"count":n}。
func parseCountArg(args string, min, max int) (int, error) {
	fields := splitArgs(args)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "count" && i+1 < len(fields) {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil || n < min || n > max {
				return 0, fmt.Errorf("count 需为%d至%d", min, max)
			}
			return n, nil
		}
	}
	if len(fields) == 1 && isDigits(fields[0]) {
		n, err := strconv.Atoi(fields[0])
		if err != nil || n < min || n > max {
			return 0, fmt.Errorf("count 需为%d至%d", min, max)
		}
		return n, nil
	}
	return min, nil
}

// parseDMArgs 解析 {"mid":"1","text":"..."}。
func parseDMArgs(args string) (string, string, error) {
	fields := splitArgs(args)
	mid, text := "", ""
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "mid":
			if i+1 < len(fields) {
				mid = fields[i+1]
				i++
			}
		case "text":
			if i+1 < len(fields) {
				text = fields[i+1]
				i++
			}
		}
	}
	if !isDigits(mid) {
		return "", "", fmt.Errorf("mid 必须是纯数字")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", fmt.Errorf("私信内容不能为空")
	}
	if len([]rune(text)) > maxDMRunes {
		return "", "", fmt.Errorf("私信不能超过%d字", maxDMRunes)
	}
	return mid, text, nil
}

// parseTextArg 解析 {"text":"..."}，并做长度校验。
func parseTextArg(args string, max int) (string, error) {
	fields := splitArgs(args)
	text := ""
	for i := 0; i < len(fields); i++ {
		if fields[i] == "text" && i+1 < len(fields) {
			text = fields[i+1]
			i++
		}
	}
	if text == "" {
		text = strings.TrimSpace(args)
	}
	if text == "" {
		return "", fmt.Errorf("正文不能为空")
	}
	if len([]rune(text)) > max {
		return "", fmt.Errorf("正文不能超过%d字", max)
	}
	return text, nil
}

// splitArgs 把参数串拆成 key,value 序列。
// MCP 调用直接传 JSON 对象：优先按 JSON 解析（字符串已反转义、正文可含空格与引号）；
// 解析失败再走旧的标点切分，保持 ReAct 的宽松输入兼容。
func splitArgs(args string) []string {
	if fields, ok := jsonArgs(args); ok {
		return fields
	}
	var fields []string
	var cur strings.Builder
	inQuote := false
	for _, r := range args {
		switch {
		case r == '"':
			inQuote = !inQuote
			if !inQuote && cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		case !inQuote && strings.ContainsRune(" \t\n,:{}=", r):
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields
}

// jsonArgs 把 JSON 对象摊平成 key,value 序列；只接受对象，失败时回落到旧切分。
func jsonArgs(args string) ([]string, bool) {
	trimmed := strings.TrimSpace(args)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return nil, false
	}
	fields := make([]string, 0, len(obj)*2)
	for key, raw := range obj {
		fields = append(fields, key, jsonScalar(raw))
	}
	return fields, true
}

// jsonScalar 把 JSON 标量/字符串数组转成工具解析器接受的文本。
func jsonScalar(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return strings.Join(list, ",")
	}
	return string(raw)
}
