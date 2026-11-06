package reasoning

// 本文件实现 7 个固定只读工具：查观众史、查自己回复、直播间状态、额度、时间、画面、搜索。
// 观众档案语义检索（viewer_search）在注入依赖时额外注册。
//
// 共同约束（派发单）：只读、不写库、参数校验、结果截断 ≤500 字。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
)

// 只读工具名（协议的一部分，改名等于改协议）。
const (
	ToolViewerMemory = "viewer_memory"
	ToolViewerSearch = "viewer_search"
	ToolReplyHistory = "reply_history"
	ToolLiveStatus   = "live_status"
	ToolQuota        = "quota"
	ToolNow          = "now"
	ToolVision       = "vision"
	ToolWebSearch    = "web_search"
)

// maxReadOnlyRunes 是只读工具结果的统一截断长度。
const maxReadOnlyRunes = 500

// ViewerSearcher 按观众 ID 查历史消息。
type ViewerSearcher interface {
	ViewerRecent(ctx context.Context, fromID string, limit int) ([]event.Event, error)
}

// ViewerProfileSearcher 解析「这句话在说谁」：UID/昵称/别名/近似/内容/语义。
//
// 用 Find 而不是 Search：调用方需要知道结果状态（找到/歧义/没找到/不可用），
// 否则「查询失败」会被模型讲成「没有这个人」。
type ViewerProfileSearcher interface {
	Find(ctx context.Context, query string, limit int) (viewer.Result, error)
}

// ReplySearcher 查本会话最近发出的回复。
type ReplySearcher interface {
	RecentReplies(ctx context.Context, ch event.Channel, session string, limit int) ([]event.Event, error)
}

// LiveStatusSource 返回直播间状态摘要（是否在播/标题/人气）。
type LiveStatusSource interface {
	LiveStatus(ctx context.Context, room string) (string, error)
}

// QuotaSource 返回某天已用的送礼额度（用于 quota 工具）。
type QuotaSource interface {
	Used(day string) (int, error)
}

// WebSearcher 是可选的搜索后端；未配置时工具返回「未配置」而不是编造结果。
type WebSearcher interface {
	Search(ctx context.Context, query string) (string, error)
}

// FrameSource 截一帧（vision 工具用）。
type FrameSource interface {
	Snapshot(ctx context.Context) (string, error)
}

// FrameReader 把一帧变成画面描述。
type FrameReader interface {
	Describe(ctx context.Context, framePath string) (string, error)
}

// ReadOnlyDeps 是只读工具的数据来源；为 nil 的工具仍会注册，
// 但调用时明确返回「未配置」，让模型知道「这条路走不通」而不是「没有数据」。
type ReadOnlyDeps struct {
	Viewer ViewerSearcher
	// ViewerSearch 为 nil 时不注册 viewer_search（不影响固定 7 个工具）。
	ViewerSearch ViewerProfileSearcher
	Replies      ReplySearcher
	Live         LiveStatusSource
	Quota        QuotaSource
	Frame        FrameSource
	FrameRead    FrameReader
	Search       WebSearcher
	// Room 是本会话房间号；空串时直播类工具返回未配置。
	Room string
	// SpeakerUID 是本条事件的发送者 UID（可信平台身份），用于「我是谁」。
	SpeakerUID string
	// QuotaLimit 是每日送礼上限（0 表示未配置）。
	QuotaLimit int
	// Now 可注入时钟（测试用）。
	Now func() time.Time
}

// BuildReadOnlyTools 装配 7 个只读工具。
func BuildReadOnlyTools(d ReadOnlyDeps) (*Toolbox, error) {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	room := strings.TrimSpace(d.Room)
	tools := make([]Tool, 0, 7)

	tools = append(tools, Tool{
		Name: ToolViewerMemory,
		Desc: "查某个观众说过什么；args 传 {\"from_id\":\"纯数字\",\"limit\":1-20}",
		Run: func(ctx context.Context, args string) (string, error) {
			fromID, limit, err := parseViewerArgs(args)
			if err != nil {
				return "", err
			}
			if d.Viewer == nil {
				return "未配置观众历史数据源", nil
			}
			rows, err := d.Viewer.ViewerRecent(ctx, fromID, limit)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return "这个观众最近没说过话", nil
			}
			var parts []string
			for _, e := range rows {
				parts = append(parts, e.Text)
			}
			return clip(strings.Join(parts, " / "), maxReadOnlyRunes), nil
		},
	})

	if d.ViewerSearch != nil {
		tools = append(tools, Tool{
			Name: ToolViewerSearch,
			Desc: "查某个观众是谁（uid/昵称/别名/描述都行）；args 传名字或描述（问「XX是谁」就传 XX）",
			Run: func(ctx context.Context, args string) (string, error) {
				query := strings.TrimSpace(args)
				if query == "" {
					return "", fmt.Errorf("查询词不能为空")
				}
				if len([]rune(query)) > 100 {
					return "", fmt.Errorf("查询词不能超过100字")
				}
				// 先解析「我是谁/你是谁」这类主体，再决定查谁。
				resolved, direct := resolveIdentitySubject(query, d.SpeakerUID)
				if direct != "" {
					return direct, nil
				}
				result, err := d.ViewerSearch.Find(ctx, resolved, 3)
				if err != nil {
					return "", err
				}
				return clip(formatIdentityResult(result), maxReadOnlyRunes), nil
			},
		})
	}

	tools = append(tools, Tool{
		Name: ToolReplyHistory,
		Desc: "查本房间自己最近回过什么；args 传 {\"limit\":1-20}",
		Run: func(ctx context.Context, args string) (string, error) {
			limit, err := parseLimitArg(args, 10, 20)
			if err != nil {
				return "", err
			}
			if d.Replies == nil {
				return "未配置回复历史数据源", nil
			}
			rows, err := d.Replies.RecentReplies(ctx, event.ChLive, room, limit)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return "最近没回过什么", nil
			}
			var parts []string
			for _, e := range rows {
				parts = append(parts, e.Text)
			}
			return clip(strings.Join(parts, " / "), maxReadOnlyRunes), nil
		},
	})

	tools = append(tools, Tool{
		Name: ToolLiveStatus,
		Desc: "查直播间现在在不在播、标题和人气；args 忽略",
		Run: func(ctx context.Context, _ string) (string, error) {
			if d.Live == nil || room == "" {
				return "未配置直播间状态数据源", nil
			}
			out, err := d.Live.LiveStatus(ctx, room)
			if err != nil {
				return "", err
			}
			return clip(out, maxReadOnlyRunes), nil
		},
	})

	tools = append(tools, Tool{
		Name: ToolQuota,
		Desc: "查今天送了多少礼物、还剩多少额度；args 忽略",
		Run: func(_ context.Context, _ string) (string, error) {
			day := now().Format("2006-01-02")
			if d.Quota == nil || d.QuotaLimit <= 0 {
				return "未配置礼物额度", nil
			}
			used, err := d.Quota.Used(day)
			if err != nil {
				return "", err
			}
			left := d.QuotaLimit - used
			if left < 0 {
				left = 0
			}
			return fmt.Sprintf("今天已送 %d 个，上限 %d 个，还剩 %d 个（电池余额需平台查询，未接入）", used, d.QuotaLimit, left), nil
		},
	})

	tools = append(tools, Tool{
		Name: ToolNow,
		Desc: "查当前时间（本地时区）；args 忽略",
		Run: func(_ context.Context, _ string) (string, error) {
			t := now()
			return t.Format("2006-01-02 15:04:05 ") + t.Format("MST"), nil
		},
	})

	tools = append(tools, Tool{
		Name: ToolVision,
		Desc: "看当前直播画面并详细描述；args 忽略",
		Run: func(ctx context.Context, _ string) (string, error) {
			if d.Frame == nil || d.FrameRead == nil {
				return "未配置画面采集", nil
			}
			path, err := d.Frame.Snapshot(ctx)
			if err != nil {
				return "", err
			}
			out, err := d.FrameRead.Describe(ctx, path)
			if err != nil {
				return "", err
			}
			return clip(out, maxReadOnlyRunes), nil
		},
	})

	tools = append(tools, Tool{
		Name: ToolWebSearch,
		Desc: "联网查证一个事实；args 传查询词（≤100 字）",
		Run: func(ctx context.Context, args string) (string, error) {
			query := strings.TrimSpace(args)
			if query == "" {
				return "", fmt.Errorf("查询词不能为空")
			}
			if len([]rune(query)) > 100 {
				return "", fmt.Errorf("查询词不能超过100字")
			}
			if d.Search == nil {
				return "未配置搜索源，无法联网查证", nil
			}
			out, err := d.Search.Search(ctx, query)
			if err != nil {
				return "", err
			}
			return clip(out, maxReadOnlyRunes), nil
		},
	})

	return NewToolbox(tools...)
}

// formatViewerHit 把一条档案压成「uid 昵称（说过/回过次数）：最近发言」的一行。
func formatViewerHit(p store.ViewerProfile) string {
	line := fmt.Sprintf("%s %s（说过 %d 次 / 鱼回过 %d 次）", p.UID, p.Name, p.Hits, p.Replies)
	if len(p.RecentTexts) > 0 {
		line += "：" + clip(strings.Join(p.RecentTexts, " / "), 60)
	}
	return line
}

// resolveIdentitySubject 解析提问主体，返回真正要查的词与「可直接回答」的答案。
//
// 「我是谁」问的是发送者本人（用可信 UID 精确查，不在档案库里猜）；
// 「你是谁」问的是鱼自己，不该占用观众检索。
func resolveIdentitySubject(query, speakerUID string) (string, string) {
	trimmed := strings.TrimSpace(strings.Trim(query, " 　?？!！。.，,、～~：:"))
	switch trimmed {
	case "我", "自己", "本人", "我是谁", "我是谁呀", "我是谁啊", "你知道我是谁", "知道我是谁", "猜猜我是谁":
		if strings.TrimSpace(speakerUID) == "" {
			return "", "不知道当前提问者是谁（本条消息没有携带平台 UID）"
		}
		return strings.TrimSpace(speakerUID), ""
	case "你", "你自己", "你是谁", "你是谁呀", "你是谁啊", "你是哪位":
		return "", "「你」指的是本鱼自己，不是观众，不用查观众档案"
	}
	return query, ""
}

// formatIdentityResult 把身份解析结果讲成模型能正确使用的一段话。
//
// 关键是要把「命中理由」说清楚：只在发言里提到过名字，不等于那个人就是本人。
func formatIdentityResult(result viewer.Result) string {
	switch result.Status {
	case viewer.StatusNotFound:
		return "没有找到匹配的观众（可以老实说不认识）"
	case viewer.StatusUnavailable:
		return "观众档案检索暂时不可用（这不等于「没有这个人」，只能说暂时查不到）"
	case viewer.StatusAmbiguous:
		parts := make([]string, 0, len(result.Candidates))
		for _, c := range result.Candidates {
			parts = append(parts, formatIdentityCandidate(c))
		}
		return "有多个候选，不能确定是哪一位（不要替用户合并成一个人）：" + strings.Join(parts, " / ")
	}
	if len(result.Candidates) == 0 {
		return "没有找到匹配的观众"
	}
	parts := make([]string, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		parts = append(parts, formatIdentityCandidate(c))
	}
	text := strings.Join(parts, " / ")
	if result.Degraded {
		text += "（语义检索不可用，仅按关键词/昵称匹配）"
	}
	return text
}

// formatIdentityCandidate 给一条候选加上「凭什么认为是他」的档位说明。
func formatIdentityCandidate(c viewer.Candidate) string {
	line := formatViewerHit(c.Profile)
	line += "【" + tierReasonText(c) + "】"
	return line
}

// tierReasonText 把档位翻译成对模型有意义的一句话。
func tierReasonText(c viewer.Candidate) string {
	switch c.Tier {
	case viewer.TierUID:
		return "UID 精确命中"
	case viewer.TierName:
		return "昵称精确命中"
	case viewer.TierAlias:
		return "别名命中（可能是历史昵称/常见写法）"
	case viewer.TierNear:
		if c.Reason == "phonetic" {
			return "发音相近的候选，需确认"
		}
		return "昵称近似（可能是错字/谐音），需确认"
	case viewer.TierContent:
		return "仅在发言里提到过这个名字，不一定是本人"
	default:
		return "语义相近，不保证是本人"
	}
}

// parseViewerArgs 解析 viewer_memory 的参数：from_id（纯数字）+ limit(1-20)。
func parseViewerArgs(args string) (string, int, error) {
	fields := strings.FieldsFunc(args, func(r rune) bool {
		return r == ' ' || r == ',' || r == ':' || r == '=' || r == '"' || r == '{' || r == '}'
	})
	fromID := ""
	limit := 10
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "from_id":
			if i+1 < len(fields) {
				fromID = fields[i+1]
				i++
			}
		case "limit":
			if i+1 < len(fields) {
				n, err := strconv.Atoi(fields[i+1])
				if err != nil || n < 1 || n > 20 {
					return "", 0, fmt.Errorf("limit 需为1至20")
				}
				limit = n
				i++
			}
		}
	}
	// 也允许直接传「纯数字」或「纯数字 5」
	if fromID == "" && len(fields) > 0 && isDigits(fields[0]) {
		fromID = fields[0]
	}
	if fromID == "" || !isDigits(fromID) {
		return "", 0, fmt.Errorf("from_id 必须是纯数字")
	}
	return fromID, limit, nil
}

// parseLimitArg 解析 {"limit":n} 形式的参数，非法值直接报错（不静默回落）。
func parseLimitArg(args string, fallback, max int) (int, error) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "{}" {
		return fallback, nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ' ' || r == ',' || r == ':' || r == '=' || r == '"' || r == '{' || r == '}'
	})
	for i := 0; i < len(fields); i++ {
		if fields[i] == "limit" && i+1 < len(fields) {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil || n < 1 || n > max {
				return 0, fmt.Errorf("limit 需为1至%d", max)
			}
			return n, nil
		}
	}
	if len(fields) == 1 && isDigits(fields[0]) {
		n, err := strconv.Atoi(fields[0])
		if err != nil || n < 1 || n > max {
			return 0, fmt.Errorf("limit 需为1至%d", max)
		}
		return n, nil
	}
	return fallback, nil
}

func isDigits(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
