package config

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

// Memory 是记忆提炼 worker 的配置（`[memory]`）。
//
// 默认开启：记忆是增强能力，worker 启动失败只降级，不阻塞 fishd。
// allow_private 默认 false，私密事件必须由配置显式授权才会提炼。
type Memory struct {
	Enabled         bool `toml:"enabled"`
	IntervalSeconds int  `toml:"interval_seconds"`
	AllowPrivate    bool `toml:"allow_private"`
	// Frozen 为 true 时提炼 worker 整轮跳过（只读不写，不推进水位）。
	// 运行时可另用状态文件 state/memory.frozen 冻结；配置冻结优先，解冻文件不会覆盖它。
	Frozen bool `toml:"frozen"`
	// LLMExtract 决定是否对「值得跑」的事件用模型做语义提炼（其余走规则版）。
	LLMExtract bool `toml:"llm_extract"`
	// LLMExtractMaxPerHour 是每小时最多调用几次模型（限流，超出回落规则版）。
	LLMExtractMaxPerHour int `toml:"llm_extract_max_per_hour"`
	// SoulEnabled 控制是否重建灵魂记忆（长期设定文件）。
	SoulEnabled bool `toml:"soul_enabled"`
	// SoulMaxEntries 是灵魂记忆条数上限。
	SoulMaxEntries int `toml:"soul_max_entries"`
	// SoulPath 是 soul-memories.md 路径；留空时从 LLM.PersonaDir 派生。
	SoulPath string `toml:"soul_path"`
	// ViewerSummaryIntervalSeconds 是观众侧写定时任务的间隔（默认每小时）。
	// 0 表示停止调度但保留已有侧写；启用时范围 60-86400 秒。侧写只读公开非 DM 弹幕。
	ViewerSummaryIntervalSeconds int `toml:"viewer_summary_interval_seconds"`
	// ViewerSummaryMaxPerRun 是每轮最多处理多少位观众（含回填名额）。
	ViewerSummaryMaxPerRun int `toml:"viewer_summary_max_per_run"`
	// ViewerSummaryBackfillLimit 是每轮最多给几位从未侧写的观众做首次回填；0 关闭回填。
	ViewerSummaryBackfillLimit int `toml:"viewer_summary_backfill_limit"`
	// ViewerSummaryMinNewMessages 是已有侧写后触发更新的最少新公开发言数。
	ViewerSummaryMinNewMessages int `toml:"viewer_summary_min_new_messages"`
	// ViewerSummaryRecentTTLSeconds 是近期观察层的有效期。
	ViewerSummaryRecentTTLSeconds int `toml:"viewer_summary_recent_ttl_seconds"`
	// ViewerSummaryDailyViewers 是每天最多尝试侧写的观众数（按尝试时间去重）。
	ViewerSummaryDailyViewers int `toml:"viewer_summary_daily_viewers"`
	// OwnerUID 是主人的可信平台 UID：只有发言人 UID 与之相等才算主人自述，
	// 也才可能提升为长期设定。昵称可被改名冒充，不参与身份判定。
	OwnerUID string `toml:"owner_uid"`
	// OwnerName 是主人的昵称，仅用于 LLM 提炼时的语义提示，不用于身份认证。
	OwnerName string `toml:"owner_name"`
}

func defaultMemory() Memory {
	return Memory{
		Enabled: true, IntervalSeconds: 300, LLMExtract: true, LLMExtractMaxPerHour: 30,
		SoulEnabled: true, SoulMaxEntries: 20,
		ViewerSummaryIntervalSeconds:  3600,
		ViewerSummaryMaxPerRun:        10,
		ViewerSummaryBackfillLimit:    2,
		ViewerSummaryMinNewMessages:   3,
		ViewerSummaryRecentTTLSeconds: 7 * 24 * 3600,
		ViewerSummaryDailyViewers:     50,
	}
}

// deriveSoulPath 在未显式配置 soul_path 时跟随人格包：
// <persona_dir>/knowledge/soul-memories.md，多 persona 各用各的。
func (m *Memory) deriveSoulPath(personaDir string) {
	if strings.TrimSpace(m.SoulPath) != "" {
		return
	}
	dir := strings.TrimSpace(personaDir)
	if dir == "" {
		return
	}
	m.SoulPath = path.Join(dir, "knowledge", "soul-memories.md")
}

// Validate 只做静态校验；水位文件与数据库在装配阶段检查。
// 未启用时忽略其余字段，允许关闭后保留占位值。
func (m Memory) Validate() error {
	if !m.Enabled {
		return nil
	}
	if m.IntervalSeconds < 10 || m.IntervalSeconds > 86400 {
		return errors.New("记忆提炼间隔必须在10至86400秒之间")
	}
	if m.LLMExtract && (m.LLMExtractMaxPerHour < 1 || m.LLMExtractMaxPerHour > 1000) {
		return errors.New("LLM 提炼每小时上限必须在1至1000之间")
	}
	if strings.ContainsAny(m.OwnerUID, " \t\r\n\x00") || utf8.RuneCountInString(m.OwnerUID) > 64 {
		return errors.New("记忆 owner_uid 必须是不含空白和控制字符的 UID")
	}
	if strings.ContainsAny(m.OwnerName, "\r\n\x00") {
		return errors.New("记忆 owner_name 不能包含控制字符")
	}
	if m.ViewerSummaryIntervalSeconds != 0 && (m.ViewerSummaryIntervalSeconds < 60 || m.ViewerSummaryIntervalSeconds > 86400) {
		return errors.New("观众侧写间隔必须为0（关闭）或60至86400秒")
	}
	if m.ViewerSummaryMaxPerRun < 1 || m.ViewerSummaryMaxPerRun > 100 {
		return errors.New("观众侧写每轮上限必须在1至100之间")
	}
	if m.ViewerSummaryBackfillLimit < 0 || m.ViewerSummaryBackfillLimit > 100 {
		return errors.New("观众侧写回填上限必须在0至100之间")
	}
	if m.ViewerSummaryMinNewMessages < 1 || m.ViewerSummaryMinNewMessages > 100 {
		return errors.New("观众侧写触发消息数必须在1至100之间")
	}
	if m.ViewerSummaryRecentTTLSeconds < 3600 || m.ViewerSummaryRecentTTLSeconds > 2592000 {
		return errors.New("观众侧写近期层有效期必须在3600至2592000秒之间")
	}
	if m.ViewerSummaryDailyViewers < 1 || m.ViewerSummaryDailyViewers > 500 {
		return errors.New("观众侧写每日观众预算必须在1至500之间")
	}
	if m.SoulEnabled {
		if m.SoulMaxEntries < 1 || m.SoulMaxEntries > 100 {
			return errors.New("灵魂记忆条数上限必须在1至100之间")
		}
		if strings.TrimSpace(m.SoulPath) == "" {
			return errors.New("开启灵魂记忆时必须配置 soul_path")
		}
	}
	return nil
}
