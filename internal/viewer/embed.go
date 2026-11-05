package viewer

// 本文件实现观众档案的增量向量化任务：昵称 + 最近发言/回复拼接后入库。
//
// 档案没有 title/summary 列，这里把「昵称」当作标题位、把最近发言与回复
// 当作摘要位；语义查询「那个爱要白饭的人」正是靠发言内容命中。
//
// 边界：
//   - 不直接访问数据库或 HTTP，由注入的 EmbeddingStore / Embedder 完成；
//   - 增量判断用文本 SHA-256：档案内容没变就不重复调用 embedding。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// EmbeddingDimension 与 migrations/0007 的 vector(1024) 对齐。
const EmbeddingDimension = 1024

const (
	defaultEmbedBatchSize = 32
	defaultEmbedRetryWait = 200 * time.Millisecond
	embedRetries          = 1
)

// EmbeddingInput 是一份待向量化档案的源字段。
type EmbeddingInput struct {
	UID     string
	Name    string
	Texts   []string
	Replies []string
}

// EmbeddingRecord 是一次批量 upsert 的单行。
type EmbeddingRecord struct {
	UID       string
	Hash      string
	Embedding []float64
}

// EmbeddingStore 由 database 层实现，负责读取源字段、增量状态与写回向量。
type EmbeddingStore interface {
	ViewerEmbeddingInputs(ctx context.Context) ([]EmbeddingInput, error)
	ExistingProfileEmbeddingHashes(ctx context.Context) (map[string]string, error)
	UpsertProfileEmbeddings(ctx context.Context, records []EmbeddingRecord) error
}

// EmbedOptions 控制批量与重试；零值按默认值处理。
type EmbedOptions struct {
	BatchSize  int
	RetryDelay time.Duration
	// Progress 在每批成功后调用，供 CLI 打进度日志。
	Progress func(done, total, batches int)
}

// EmbedStats 汇总一次增量任务的结果。
type EmbedStats struct {
	Total    int
	Skipped  int
	Pending  int
	Batches  int
	Embedded int
}

type embedDocument struct {
	uid  string
	text string
	hash string
}

// RunEmbedding 执行一次增量向量化：新增或内容变化的档案才重新计算。
func RunEmbedding(ctx context.Context, store EmbeddingStore, embedder Embedder, opts EmbedOptions) (EmbedStats, error) {
	if ctx == nil {
		return EmbedStats{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return EmbedStats{}, err
	}
	if store == nil || embedder == nil {
		return EmbedStats{}, errors.New("档案向量化依赖不能为空")
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
	existing, err := store.ExistingProfileEmbeddingHashes(ctx)
	if err != nil {
		return EmbedStats{}, err
	}

	stats := EmbedStats{Total: len(inputs)}
	pending := make([]embedDocument, 0, len(inputs))
	for _, input := range inputs {
		text := embeddingText(input)
		if text == "" {
			stats.Skipped++
			continue
		}
		hash := hashText(text)
		if existing[input.UID] == hash {
			stats.Skipped++
			continue
		}
		pending = append(pending, embedDocument{uid: input.UID, text: text, hash: hash})
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
			records = append(records, EmbeddingRecord{UID: batch[i].uid, Hash: batch[i].hash, Embedding: vector})
		}
		if err := store.UpsertProfileEmbeddings(ctx, records); err != nil {
			return stats, fmt.Errorf("写入档案向量失败：%w", err)
		}
		stats.Embedded += len(records)
		if opts.Progress != nil {
			opts.Progress(stats.Embedded, stats.Pending, stats.Batches)
		}
	}
	return stats, nil
}

// embeddingText 按「昵称 + 最近发言 + 最近回复」拼接，空段不产生多余换行。
func embeddingText(input EmbeddingInput) string {
	parts := make([]string, 0, len(input.Texts)+len(input.Replies)+1)
	if name := strings.TrimSpace(input.Name); name != "" {
		parts = append(parts, name)
	}
	for _, text := range input.Texts {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	for _, reply := range input.Replies {
		if trimmed := strings.TrimSpace(reply); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n")
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
