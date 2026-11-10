package store

// 本文件定义观众档案（viewer_profiles）的仓储契约。
//
// 档案是「认人」用的投影：uid 是主键（昵称会变），hits/replies 是累计计数，
// recent_* 是最近若干条原文快照。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ViewerSummaryGenerator 是当前观众侧写生成器版本；来源指纹与知识条目都带上它，
// 生成逻辑变化时必须升版，避免旧侧写被当成同一口径继续使用。
const ViewerSummaryGenerator = "viewer-summary/v2"

// 观众侧写投影动作。
const (
	ViewerSummaryActionPublish = "publish"
	ViewerSummaryActionPurge   = "purge"
)

// ViewerProfile 是一份观众档案。
type ViewerProfile struct {
	// UID 是平台用户 ID（主键）。
	UID string
	// Name 是最近一次见到的昵称快照。
	Name string
	// Hits 是说过几句（in 事件数）。
	Hits int
	// Replies 是鱼回了几次（out 事件数）。
	Replies int
	// LastSeen 是最后出现时间。
	LastSeen time.Time
	// RecentTexts 是最近说过的话（新的在前）。
	RecentTexts []string
	// RecentReplies 是鱼最近回过的话（新的在前）。
	RecentReplies []string
	// ProfileSummary 是 LLM 侧写：性格/偏好/口头禅/关系/最近话题；空表示尚未生成。
	ProfileSummary string
	// SummaryUpdatedAt 是侧写最近写入时间；零值表示从未生成过。
	SummaryUpdatedAt time.Time
	// SummarySourceHash 是生成侧写时来源事件的版本指纹。
	SummarySourceHash string
	// SummarySourceIDs 是生成侧写用到的公开事件 ID（按输入顺序）。
	SummarySourceIDs []string
	// SummaryGenerator 是生成器版本。
	SummaryGenerator string
	// SummaryKeywords 是写入知识条目的检索关键词（含昵称）。
	SummaryKeywords []string
	// SummaryLastSuccessAt 是最近一次模型成功生成侧写的时间；空表示从未成功。
	SummaryLastSuccessAt time.Time
	// SummaryFallbackAt 是最近一次统计降级写入的时间。
	SummaryFallbackAt time.Time
	// SummaryFailures 是连续失败次数，成功生成后清零。
	SummaryFailures int
	// SummaryNextRetryAt 是补偿重试时间；空表示不再自动重试（等待新发言）。
	SummaryNextRetryAt time.Time
	// SummaryAttemptedAt 是最近一次尝试时间（含失败与保留旧版本）。
	SummaryAttemptedAt time.Time
	// UpdatedAt 是档案写入时间。
	UpdatedAt time.Time
}

// Validate 只做结构性检查，不猜业务含义。
func (p ViewerProfile) Validate() error {
	if strings.TrimSpace(p.UID) == "" || strings.TrimSpace(p.Name) == "" {
		return ErrInvalid
	}
	if p.Hits < 0 || p.Replies < 0 || p.LastSeen.IsZero() {
		return ErrInvalid
	}
	return nil
}

// ViewerRepo 是观众档案的读写接口。
type ViewerRepo interface {
	// UpsertProfile 写入或更新一份档案（按 uid 覆盖）。
	UpsertProfile(ctx context.Context, p ViewerProfile) error
	// GetProfile 按 uid 读档案；不存在返回 ErrViewerNotFound。
	GetProfile(ctx context.Context, uid string) (ViewerProfile, error)
	// TopViewers 按 hits 倒序取前 limit 个档案。
	TopViewers(ctx context.Context, limit int) ([]ViewerProfile, error)
}

// ViewerProfileHit 是一条档案检索命中。
type ViewerProfileHit struct {
	Profile    ViewerProfile
	Similarity float64
	Score      float64
}

// ViewerRebuildOptions 是档案重建参数。
type ViewerRebuildOptions struct {
	MinHits       int
	RecentTexts   int
	RecentReplies int
}

// ViewerRebuildStats 是一轮重建的统计。
type ViewerRebuildStats struct {
	Profiles int
	// Revoked 是本轮被撤回档案或清空侧写的 UID；调用方据此异步清理公开知识条目。
	Revoked []string
}

// ViewerSummarySource 是一条侧写来源事件的最小快照。
type ViewerSummarySource struct {
	ID   string
	Text string
	At   time.Time
}

// ViewerSummarySourceHash 计算侧写来源指纹。
//
// 生成侧写与保存侧写使用同一份规范化编码：生成器版本、UID、每条事件的 ID/正文/时间
// 按输入顺序串接。事件被删除、改密或正文变化都会导致保存时的重算结果不同，
// 从而以 CAS 方式拒绝写入过期侧写。
func ViewerSummarySourceHash(uid, generator string, sources []ViewerSummarySource) string {
	h := sha256.New()
	h.Write([]byte(generator))
	h.Write([]byte{0x1f})
	h.Write([]byte(uid))
	for _, source := range sources {
		h.Write([]byte{0x1f})
		h.Write([]byte(source.ID))
		h.Write([]byte{0x1f})
		h.Write([]byte(source.Text))
		h.Write([]byte{0x1f})
		h.Write([]byte(source.At.UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ViewerSummaryWrite 是一次侧写落库请求。
//
// SaveProfileSummary 会在事务内用 Sources 重算来源指纹并与 SourceHash 比对；
// 来源已变化时返回 ErrViewerSourceChanged，调用方放弃这次派生结果。
type ViewerSummaryWrite struct {
	UID        string
	Summary    string
	At         time.Time
	Record     MemoryRecord
	SourceHash string
	Sources    []ViewerSummarySource
	Generator  string
	Keywords   []string
	// Fallback 表示本次内容是统计降级结果，只写 fallback_at。
	Fallback bool
	// Failures 是本次写入后的连续失败次数（降级为 1，成功为 0）。
	Failures int
	// NextRetryAt 是降级后的补偿时间；零值表示不安排重试。
	NextRetryAt time.Time
}

// Validate 只做结构与来源自洽检查；是否与库内事件一致由仓储在事务里验证。
func (w ViewerSummaryWrite) Validate() error {
	if strings.TrimSpace(w.UID) == "" || strings.TrimSpace(w.Summary) == "" || w.At.IsZero() {
		return ErrInvalid
	}
	if strings.TrimSpace(w.Generator) == "" || len(w.Sources) == 0 || len(w.SourceHash) != 64 {
		return ErrInvalid
	}
	if w.Failures < 0 {
		return ErrInvalid
	}
	if err := w.Record.Validate(); err != nil {
		return err
	}
	if len(w.Record.EventIDs) != len(w.Sources) {
		return ErrInvalid
	}
	for i, source := range w.Sources {
		if strings.TrimSpace(source.ID) == "" || source.At.IsZero() {
			return ErrInvalid
		}
		if w.Record.EventIDs[i] != source.ID {
			return ErrInvalid
		}
	}
	if ViewerSummarySourceHash(w.UID, w.Generator, w.Sources) != w.SourceHash {
		return ErrInvalid
	}
	return nil
}

// ViewerSummaryDefer 记录一次「保留旧版本」的失败，只推进补偿状态。
type ViewerSummaryDefer struct {
	UID         string
	At          time.Time
	Failures    int
	NextRetryAt time.Time
}

// Validate 检查补偿写入的最小字段。
func (d ViewerSummaryDefer) Validate() error {
	if strings.TrimSpace(d.UID) == "" || d.At.IsZero() || d.Failures < 1 {
		return ErrInvalid
	}
	return nil
}

// ViewerSummaryJob 是一条待处理的投影任务。
type ViewerSummaryJob struct {
	UID      string
	Action   string
	Attempts int
}

// Validate 检查动作是否合法。
func (j ViewerSummaryJob) Validate() error {
	if strings.TrimSpace(j.UID) == "" || j.Attempts < 0 {
		return ErrInvalid
	}
	if j.Action != ViewerSummaryActionPublish && j.Action != ViewerSummaryActionPurge {
		return ErrInvalid
	}
	return nil
}

// ErrViewerNotFound 是观众档案统一的「不存在」错误（陌生人）。
//
// 它包装 ErrNotFound：按观众语义判断走 ErrViewerNotFound，
// 按通用存储语义判断走 ErrNotFound，两者 errors.Is 都成立——
// 不允许再出现「仓储返回一个、检索层识别另一个」的分裂契约。
var ErrViewerNotFound = fmt.Errorf("观众档案不存在: %w", ErrNotFound)

// ErrViewerSourceChanged 表示侧写来源在生成与保存之间被撤回、改密或修改。
//
// 它包装 ErrConflict：按来源新鲜度判断走 ErrViewerSourceChanged，
// 按通用冲突语义判断走 ErrConflict。
var ErrViewerSourceChanged = fmt.Errorf("观众侧写来源已变化: %w", ErrConflict)
