package knowledge

// 知识条目向量化：把 title/usage/tags 拼成输入，批量调用 embedding 并增量入库。
// 本文件只定义领域类型与任务流程，不直接访问数据库或 HTTP。

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

// IndexTemplate 是知识索引的输入模板版本；embeddingText 拼接规则变化时必须改版本号。
const IndexTemplate = "knowledge.title-usage-tags/v1"

// EmbeddingDocument 是一条待向量化知识的最小投影。
type EmbeddingDocument struct {
	ID     string
	Source string
	Title  string
	Text   string
	// TextHash 是嵌入文本的 hash，用于内容未变的增量跳过。
	TextHash string
	// SourceHash 是原始字段（title/usage/tags）的 hash，用于写回 CAS。
	SourceHash string
	UpdatedAt  time.Time
}

// EmbeddingMeta 是数据库里已存在向量的索引状态，三个字段各司其职：
//   - TextHash 判内容是否变化；
//   - SourceHash 判写回时源是否已更新；
//   - Fingerprint 判向量空间是否一致（换模型/提供方/维度/模板后不复用）。
type EmbeddingMeta struct {
	TextHash    string
	SourceHash  string
	Fingerprint string
}

// EmbeddingRecord 是一次批量 upsert 的单行。
type EmbeddingRecord struct {
	ID        string
	Source    string
	Title     string
	UpdatedAt time.Time
	Hash      string
	// SourceHash 是本次计算所基于的源字段 hash，写回后成为该行的版本标记。
	SourceHash string
	// ExpectedSourceHash 是读取时该行已有的源 hash（空表示行不存在或旧行为空）。
	// 写回要求当前行仍是这个状态，否则说明其他进程已按更新源写过，本次结果作废。
	ExpectedSourceHash string
	Fingerprint        string
	Embedding          []float64
}

// SemanticHit 是向量检索命中的条目与余弦相似度。
type SemanticHit struct {
	ID         string
	Similarity float64
}

// Embedder 由 OpenAI 兼容客户端实现；测试可注入 mock。
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float64, error)
}

// EmbeddingStore 由 database 层实现，负责持久化与 pgvector 查询。
type EmbeddingStore interface {
	ExistingEmbeddings(ctx context.Context) (map[string]EmbeddingMeta, error)
	// UpsertEmbeddings 只对「源 hash 未变」的行写回，返回实际写入条数。
	// 返回条数小于 records 表示这些行的源在计算期间已被更新，写回被拒绝。
	UpsertEmbeddings(ctx context.Context, records []EmbeddingRecord) (int, error)
	// SemanticSearch 只检索与当前索引指纹一致的向量；空指纹拒绝查询。
	SemanticSearch(ctx context.Context, vector []float64, fingerprint string, limit int) ([]SemanticHit, error)
}

// EmbedOptions 控制批量与重试；零值按默认值处理。
type EmbedOptions struct {
	BatchSize  int
	RetryDelay time.Duration
	// Fingerprint 是当前向量空间的指纹（模型/提供方/维度/输入模板），必填。
	// 与已入库指纹不同的条目会重新计算，避免同维度换模型后复用旧空间。
	Fingerprint string
	// Progress 在每批成功后调用，供 CLI/fishd 打进度日志。
	Progress func(done, total, batches int)
}

// EmbedStats 汇总一次增量任务的结果。
type EmbedStats struct {
	Revision string
	Total    int
	Skipped  int
	Pending  int
	Batches  int
	Embedded int
	// Rejected 是计算期间源已变化、被 CAS 拒绝写回的条数（下一轮会重算）。
	Rejected int
}

// EmbeddingDimension 与 migrations/0006 的 vector(1024) 对齐。
const EmbeddingDimension = 1024

const (
	defaultEmbedBatchSize = 32
	defaultEmbedRetryWait = 200 * time.Millisecond
	embedRetries          = 1
)

// EmbeddingDocuments 返回当前快照的向量化输入与内容 hash；增量判断不看 mtime。
func (r *FileReader) EmbeddingDocuments(ctx context.Context) ([]EmbeddingDocument, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("知识Reader不能为空")
	}
	st := r.current()
	documents := make([]EmbeddingDocument, 0, len(st.entries))
	for _, e := range st.entries {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		text := embeddingText(e)
		documents = append(documents, EmbeddingDocument{
			ID:         e.item.ID,
			Source:     e.file,
			Title:      e.title,
			Text:       text,
			TextHash:   hashText(text),
			SourceHash: sourceHash(e),
			// PostgreSQL timestamptz 只保留微秒，先截断避免展示层出现纳秒误差。
			UpdatedAt: e.modTime.UTC().Truncate(time.Microsecond),
		})
	}
	return documents, nil
}

// sourceHash 由参与向量的原始字段计算：summary/keywords 不影响语义输入，不纳入。
// 字段用长度前缀分隔，避免相邻字段拼接产生歧义（"ab"+"c" 与 "a"+"bc"）。
func sourceHash(e entry) string {
	parts := make([]string, 0, 2+len(e.tags))
	parts = append(parts, e.title, e.usage)
	parts = append(parts, e.tags...)
	var builder strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&builder, "%d:%s\n", len(part), part)
	}
	return hashText(builder.String())
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// embeddingText 按派发单拼接 title + usage + tags，保持文件内顺序。
func embeddingText(e entry) string {
	parts := make([]string, 0, 3)
	if title := strings.TrimSpace(e.title); title != "" {
		parts = append(parts, title)
	}
	if usage := strings.TrimSpace(e.usage); usage != "" {
		parts = append(parts, usage)
	}
	tags := make([]string, 0, len(e.tags))
	seen := make(map[string]bool, len(e.tags))
	for _, raw := range e.tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	if len(tags) > 0 {
		parts = append(parts, strings.Join(tags, " "))
	}
	return strings.Join(parts, "\n")
}

// RunEmbedding 执行一次增量向量化：新增、内容变化或索引指纹变化的知识条目才重算。
func RunEmbedding(ctx context.Context, reader *FileReader, embedder Embedder, store EmbeddingStore, opts EmbedOptions) (EmbedStats, error) {
	if err := checkContext(ctx); err != nil {
		return EmbedStats{}, err
	}
	// 账本用途：批量写知识索引（关联标识只用索引名，不带知识正文）。
	ctx = llmmeta.WithCall(ctx, llmmeta.Call{Purpose: llmmeta.PurposeEmbedIndex, CorrelationID: "knowledge"})
	if reader == nil || embedder == nil || store == nil {
		return EmbedStats{}, errors.New("知识向量化依赖不能为空")
	}
	fingerprint := strings.TrimSpace(opts.Fingerprint)
	if fingerprint == "" {
		return EmbedStats{}, errors.New("知识向量化缺少索引指纹：无法确认向量空间")
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

	documents, err := reader.EmbeddingDocuments(ctx)
	if err != nil {
		return EmbedStats{}, err
	}
	existing, err := store.ExistingEmbeddings(ctx)
	if err != nil {
		return EmbedStats{}, err
	}
	stats := EmbedStats{Total: len(documents)}
	pending := make([]EmbeddingDocument, 0, len(documents))
	for _, doc := range documents {
		meta, ok := existing[doc.ID]
		if ok && meta.TextHash == doc.TextHash && meta.SourceHash == doc.SourceHash && meta.Fingerprint == fingerprint {
			stats.Skipped++
			continue
		}
		pending = append(pending, doc)
	}
	stats.Pending = len(pending)
	if len(pending) == 0 {
		if info, err := reader.SnapshotInfo(); err == nil {
			stats.Revision = info.Revision
		}
		return stats, nil
	}
	stats.Batches = (len(pending) + batchSize - 1) / batchSize

	for start := 0; start < len(pending); start += batchSize {
		if err := checkContext(ctx); err != nil {
			return stats, err
		}
		end := start + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		inputs := make([]string, len(batch))
		for i, doc := range batch {
			inputs[i] = doc.Text
		}
		vectors, err := embedWithRetry(ctx, embedder, inputs, retryDelay)
		if err != nil {
			return stats, fmt.Errorf("向量化批次失败：%w", err)
		}
		if len(vectors) != len(batch) {
			return stats, errors.New("embedding返回向量数不符")
		}
		// 计算期间快照可能已被重载：重新核对当前源，避免旧向量覆盖新版本。
		current, err := reader.EmbeddingDocuments(ctx)
		if err != nil {
			return stats, err
		}
		sources := make(map[string]string, len(current))
		for _, doc := range current {
			sources[doc.ID] = doc.SourceHash
		}
		records := make([]EmbeddingRecord, 0, len(batch))
		for i, vector := range vectors {
			if err := validateEmbeddingVector(vector); err != nil {
				return stats, err
			}
			doc := batch[i]
			if sources[doc.ID] != doc.SourceHash {
				stats.Rejected++
				continue
			}
			records = append(records, EmbeddingRecord{
				ID:                 doc.ID,
				Source:             doc.Source,
				Title:              doc.Title,
				UpdatedAt:          doc.UpdatedAt,
				Hash:               doc.TextHash,
				SourceHash:         doc.SourceHash,
				ExpectedSourceHash: existing[doc.ID].SourceHash,
				Fingerprint:        fingerprint,
				Embedding:          vector,
			})
		}
		if len(records) > 0 {
			written, err := store.UpsertEmbeddings(ctx, records)
			if err != nil {
				return stats, fmt.Errorf("写入知识向量失败：%w", err)
			}
			if written < 0 || written > len(records) {
				return stats, errors.New("写入知识向量返回条数非法")
			}
			stats.Embedded += written
			stats.Rejected += len(records) - written
		}
		if opts.Progress != nil {
			opts.Progress(stats.Embedded, stats.Pending, stats.Batches)
		}
	}
	if info, err := reader.SnapshotInfo(); err == nil {
		stats.Revision = info.Revision
	}
	return stats, nil
}

func embedWithRetry(ctx context.Context, embedder Embedder, inputs []string, delay time.Duration) ([][]float64, error) {
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
