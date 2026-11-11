package viewer

// 本文件实现观众档案的增量向量化任务。
//
// **一路一个向量（M10）**：以前把「昵称 + 本人发言 + 鱼的回复」拼成一个向量，
// 于是鱼经常说的「白饭」被算成所有被回复过的人的特点——认人和查证都被污染。
// 现在按用途拆成三路，各自独立计算、独立失效：
//
//	identity    = 昵称（认人：同音、改名、模糊昵称）
//	facts       = 本人发言（查「他说过什么 / 谁爱要白饭」）
//	interaction = 鱼的回复（查互动关系，**不参与认人**）
//
// 边界：
//   - 不直接访问数据库或 HTTP，由注入的 EmbeddingStore / Embedder 完成；
//   - 增量判断用文本 SHA-256：某一路内容没变就跳过该路，不影响另外两路。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"RaiseMyFish/internal/llmmeta"
)

// EmbeddingDimension 与 migrations/0007 的 vector(1024) 对齐。
const EmbeddingDimension = 1024

// IndexTemplate 是三路索引输入模板的版本打包；任一路拼接规则变化都要改版本。
const IndexTemplate = "viewer.identity-v1+facts-v1+interaction-v1"

const (
	defaultEmbedBatchSize = 32
	defaultEmbedRetryWait = 200 * time.Millisecond
	embedRetries          = 1
)

// EmbeddingKind 是向量的用途；同时是数据库列的选择依据。
type EmbeddingKind string

const (
	// KindIdentity 只含昵称。
	KindIdentity EmbeddingKind = "identity"
	// KindFacts 只含本人发言。
	KindFacts EmbeddingKind = "facts"
	// KindInteraction 只含鱼的回复。
	KindInteraction EmbeddingKind = "interaction"
)

// AllEmbeddingKinds 返回稳定顺序的三路，便于统计、测试与入库循环。
func AllEmbeddingKinds() []EmbeddingKind {
	return []EmbeddingKind{KindIdentity, KindFacts, KindInteraction}
}

// Valid 判断是否是已知的向量用途。
func (k EmbeddingKind) Valid() bool {
	switch k {
	case KindIdentity, KindFacts, KindInteraction:
		return true
	default:
		return false
	}
}

// EmbeddingInput 是一份待向量化档案的源字段。
type EmbeddingInput struct {
	UID     string
	Name    string
	Texts   []string
	Replies []string
	// SourceHashes 是仓储按「这一路自己的源字段」计算的版本 hash。
	// identity 只看昵称、facts 只看本人发言、interaction 只看鱼的回复，
	// 任一路变化只让该路 stale，不影响另外两路。
	SourceHashes map[EmbeddingKind]string
}

// EmbeddingIndex 是某一路已入库向量的索引状态：
// TextHash 用于内容未变的增量跳过，SourceHash 用于 stale 判定与写回 CAS，
// Fingerprint 用于拒绝跨向量空间复用。
type EmbeddingIndex struct {
	TextHash    string
	SourceHash  string
	Fingerprint string
}

// EmbeddingRecord 是一次批量 upsert 的单行（一条向量）。
type EmbeddingRecord struct {
	UID         string
	Kind        EmbeddingKind
	Hash        string
	SourceHash  string
	Fingerprint string
	Embedding   []float64
}

// EmbeddingStore 由 database 层实现，负责读取源字段、增量状态与写回向量。
type EmbeddingStore interface {
	ViewerEmbeddingInputs(ctx context.Context) ([]EmbeddingInput, error)
	// ExistingProfileEmbeddingIndexes 返回 uid → kind → 索引状态（只含有向量的路）。
	ExistingProfileEmbeddingIndexes(ctx context.Context) (map[string]map[EmbeddingKind]EmbeddingIndex, error)
	// UpsertProfileEmbeddings 只对「该路源 hash 未变」的行写回，返回实际写入条数。
	UpsertProfileEmbeddings(ctx context.Context, records []EmbeddingRecord) (int, error)
}

// EmbedOptions 控制批量与重试；零值按默认值处理。
type EmbedOptions struct {
	BatchSize  int
	RetryDelay time.Duration
	// Fingerprint 是当前向量空间的指纹（模型/提供方/维度/三路模板），必填。
	Fingerprint string
	// Progress 在每批成功后调用，供 CLI 打进度日志。
	Progress func(done, total, batches int)
}

// EmbedStats 汇总一次增量任务的结果。
//
// 统计口径是**向量文档**而不是档案：一份档案最多贡献三条（identity/facts/interaction），
// 所以 Total 可能是档案数的 3 倍。
type EmbedStats struct {
	Total    int
	Skipped  int
	Pending  int
	Batches  int
	Embedded int
	// Rejected 是计算期间该路源已变化、被 CAS 拒绝写回的条数。
	Rejected int
}

type embedDocument struct {
	uid        string
	kind       EmbeddingKind
	text       string
	hash       string
	sourceHash string
}

// RunEmbedding 执行一次增量向量化：新增或内容变化的档案才重新计算。
func RunEmbedding(ctx context.Context, store EmbeddingStore, embedder Embedder, opts EmbedOptions) (EmbedStats, error) {
	if ctx == nil {
		return EmbedStats{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return EmbedStats{}, err
	}
	// 账本用途：批量写档案索引（关联标识只用索引名，不带昵称/发言）。
	ctx = llmmeta.WithCall(ctx, llmmeta.Call{Purpose: llmmeta.PurposeEmbedIndex, CorrelationID: "viewer"})
	if store == nil || embedder == nil {
		return EmbedStats{}, errors.New("档案向量化依赖不能为空")
	}
	fingerprint := strings.TrimSpace(opts.Fingerprint)
	if fingerprint == "" {
		return EmbedStats{}, errors.New("档案向量化缺少索引指纹：无法确认向量空间")
	}
	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = defaultEmbedBatchSize
	}
	if batchSize < 1 || batchSize > 2048 {
		return EmbedStats{}, errors.New("embedding批量大小必须在1至2048之间")
	}
	retryDelay := opts.RetryDelay
	if retryDelay < 0 {
		return EmbedStats{}, errors.New("embedding重试间隔不能为负")
	}
	if retryDelay == 0 {
		retryDelay = defaultEmbedRetryWait
	}

	inputs, err := store.ViewerEmbeddingInputs(ctx)
	if err != nil {
		return EmbedStats{}, err
	}
	existing, err := store.ExistingProfileEmbeddingIndexes(ctx)
	if err != nil {
		return EmbedStats{}, err
	}

	stats := EmbedStats{}
	pending := make([]embedDocument, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input.UID) == "" {
			continue
		}
		known := existing[input.UID]
		for _, doc := range documentsFor(input) {
			stats.Total++
			if doc.text == "" {
				// 这一路没有内容（比如从没被回复过 → 没有 interaction 向量）
				stats.Skipped++
				continue
			}
			doc.hash = hashText(doc.text)
			sourceHash := strings.TrimSpace(input.SourceHashes[doc.kind])
			if sourceHash == "" {
				return EmbedStats{}, fmt.Errorf("档案 %s 缺少 %s 源hash，拒绝向量化", input.UID, doc.kind)
			}
			doc.sourceHash = sourceHash
			index := known[doc.kind]
			if index.TextHash == doc.hash && index.SourceHash == sourceHash && index.Fingerprint == fingerprint {
				stats.Skipped++
				continue
			}
			pending = append(pending, doc)
		}
	}
	stats.Pending = len(pending)
	if len(pending) == 0 {
		return stats, nil
	}
	stats.Batches = (len(pending) + batchSize - 1) / batchSize

	for start := 0; start < len(pending); start += batchSize {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		end := start + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		vectors, err := embedWithRetry(ctx, embedder, batch, retryDelay)
		if err != nil {
			return stats, fmt.Errorf("档案向量化批次失败：%w", err)
		}
		records := make([]EmbeddingRecord, 0, len(batch))
		for i, vector := range vectors {
			if err := validateEmbeddingVector(vector); err != nil {
				return stats, err
			}
			records = append(records, EmbeddingRecord{
				UID: batch[i].uid, Kind: batch[i].kind,
				Hash: batch[i].hash, SourceHash: batch[i].sourceHash,
				Fingerprint: fingerprint, Embedding: vector,
			})
		}
		written, err := store.UpsertProfileEmbeddings(ctx, records)
		if err != nil {
			return stats, fmt.Errorf("写入档案向量失败：%w", err)
		}
		if written < 0 || written > len(records) {
			return stats, errors.New("写入档案向量返回条数非法")
		}
		stats.Embedded += written
		stats.Rejected += len(records) - written
		if opts.Progress != nil {
			opts.Progress(stats.Embedded, stats.Pending, stats.Batches)
		}
	}
	return stats, nil
}

// documentsFor 把一份档案拆成三路文档（M10）。
//
// 每一路只放自己该有的内容：把「鱼说的话」放进 identity 或 facts，
// 就会出现「所有被回复过的人都爱吃白饭」这类污染。
func documentsFor(input EmbeddingInput) []embedDocument {
	return []embedDocument{
		{uid: input.UID, kind: KindIdentity, text: identityText(input)},
		{uid: input.UID, kind: KindFacts, text: factsText(input)},
		{uid: input.UID, kind: KindInteraction, text: interactionText(input)},
	}
}

// identityText 只保留昵称：认人只该看「他叫什么」，不看他说了什么。
func identityText(input EmbeddingInput) string {
	return strings.TrimSpace(input.Name)
}

// factsText 只拼本人发言：这是「谁说过这句话」的唯一依据。
func factsText(input EmbeddingInput) string {
	return joinNonEmpty(input.Texts)
}

// interactionText 只拼鱼的回复：用于互动关系，不参与认人。
func interactionText(input EmbeddingInput) string {
	return joinNonEmpty(input.Replies)
}

// joinNonEmpty 用换行拼接非空片段，空段不产生多余换行。
func joinNonEmpty(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "\n")
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func embedWithRetry(ctx context.Context, embedder Embedder, batch []embedDocument, delay time.Duration) ([][]float64, error) {
	inputs := make([]string, len(batch))
	for i := range batch {
		inputs[i] = batch[i].text
	}
	var lastErr error
	for attempt := 0; attempt <= embedRetries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		vectors, err := embedder.Embed(ctx, inputs)
		if err == nil {
			if len(vectors) != len(batch) {
				return nil, errors.New("embedding返回向量数不符")
			}
			return vectors, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func validateEmbeddingVector(vector []float64) error {
	if len(vector) != EmbeddingDimension {
		return fmt.Errorf("embedding维度不符：期望%d，实际%d", EmbeddingDimension, len(vector))
	}
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("embedding包含非法数值")
		}
	}
	return nil
}
