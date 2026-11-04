package knowledge

// 知识条目向量化：把 title/usage/tags 拼成输入，批量调用 embedding 并增量入库。
// 本文件只定义领域类型与任务流程，不直接访问数据库或 HTTP。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// EmbeddingDocument 是一条待向量化知识的最小投影。
type EmbeddingDocument struct {
	ID        string
	Source    string
	Title     string
	Text      string
	UpdatedAt time.Time
}

// EmbeddingMeta 是数据库里已存在向量的增量判断信息。
type EmbeddingMeta struct {
	Source    string
	Title     string
	UpdatedAt time.Time
}

// EmbeddingRecord 是一次批量 upsert 的单行。
type EmbeddingRecord struct {
	ID        string
	Source    string
	Title     string
	UpdatedAt time.Time
	Embedding []float64
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
	UpsertEmbeddings(ctx context.Context, records []EmbeddingRecord) error
	SemanticSearch(ctx context.Context, vector []float64, limit int) ([]SemanticHit, error)
}

// EmbedOptions 控制批量与重试；零值按默认值处理。
type EmbedOptions struct {
	BatchSize  int
	RetryDelay time.Duration
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
}

// EmbeddingDimension 与 migrations/0006 的 vector(1024) 对齐。
const EmbeddingDimension = 1024

const (
	defaultEmbedBatchSize = 32
	defaultEmbedRetryWait = 200 * time.Millisecond
	embedRetries          = 1
)

// EmbeddingDocuments 返回当前快照的向量化输入；文件来源与 mtime 用于增量判断。
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
		documents = append(documents, EmbeddingDocument{
			ID:        e.item.ID,
			Source:    e.file,
			Title:     e.title,
			Text:      embeddingText(e),
			UpdatedAt: e.modTime,
		})
	}
	return documents, nil
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

// RunEmbedding 执行一次增量向量化：只处理新增、标题/来源变化或文件 mtime 更新的条目。
func RunEmbedding(ctx context.Context, reader *FileReader, embedder Embedder, store EmbeddingStore, opts EmbedOptions) (EmbedStats, error) {
	if err := checkContext(ctx); err != nil {
		return EmbedStats{}, err
	}
	if reader == nil || embedder == nil || store == nil {
		return EmbedStats{}, errors.New("知识向量化依赖不能为空")
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
		if ok && meta.Source == doc.Source && meta.Title == doc.Title && !meta.UpdatedAt.Before(doc.UpdatedAt) {
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
		records := make([]EmbeddingRecord, 0, len(batch))
		for i, vector := range vectors {
			if err := validateEmbeddingVector(vector); err != nil {
				return stats, err
			}
			records = append(records, EmbeddingRecord{
				ID:        batch[i].ID,
				Source:    batch[i].Source,
				Title:     batch[i].Title,
				UpdatedAt: batch[i].UpdatedAt,
				Embedding: vector,
			})
		}
		if err := store.UpsertEmbeddings(ctx, records); err != nil {
			return stats, fmt.Errorf("写入知识向量失败：%w", err)
		}
		stats.Embedded += len(records)
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
