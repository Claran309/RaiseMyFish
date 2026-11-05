package memory

// 本文件实现长期记忆的增量向量化任务：title + summary 拼接后批量入库。
//
// 边界：
//   - 不直接访问数据库或 HTTP，由注入的 EmbeddingStore / Embedder 完成；
//   - 增量判断用文本 SHA-256：内容没变就不重复调用 embedding；
//   - 空摘要跳过，避免把整表空行送进向量服务。

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

// EmbeddingInput 是一条待向量化记忆的源字段。
type EmbeddingInput struct {
	ID      string
	Title   string
	Summary string
}

// EmbeddingRecord 是一次批量 upsert 的单行。
type EmbeddingRecord struct {
	ID        string
	Hash      string
	Embedding []float64
}

// EmbeddingStore 由 database 层实现，负责读取源字段、增量状态与写回向量。
type EmbeddingStore interface {
	MemoryEmbeddingInputs(ctx context.Context) ([]EmbeddingInput, error)
	ExistingMemoryEmbeddingHashes(ctx context.Context) (map[string]string, error)
	UpsertMemoryEmbeddings(ctx context.Context, records []EmbeddingRecord) error
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
	id   string
	text string
	hash string
}

// RunEmbedding 执行一次增量向量化：新增或文本变化的记忆才重新计算。
func RunEmbedding(ctx context.Context, store EmbeddingStore, embedder Embedder, opts EmbedOptions) (EmbedStats, error) {
	if ctx == nil {
		return EmbedStats{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return EmbedStats{}, err
	}
	if store == nil || embedder == nil {
		return EmbedStats{}, errors.New("记忆向量化依赖不能为空")
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

	inputs, err := store.MemoryEmbeddingInputs(ctx)
	if err != nil {
		return EmbedStats{}, err
	}
	existing, err := store.ExistingMemoryEmbeddingHashes(ctx)
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
		if existing[input.ID] == hash {
			stats.Skipped++
			continue
		}
		pending = append(pending, embedDocument{id: input.ID, text: text, hash: hash})
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
			return stats, fmt.Errorf("记忆向量化批次失败：%w", err)
		}
		records := make([]EmbeddingRecord, 0, len(batch))
		for i, vector := range vectors {
			if err := validateEmbeddingVector(vector); err != nil {
				return stats, err
			}
			records = append(records, EmbeddingRecord{ID: batch[i].id, Hash: batch[i].hash, Embedding: vector})
		}
		if err := store.UpsertMemoryEmbeddings(ctx, records); err != nil {
			return stats, fmt.Errorf("写入记忆向量失败：%w", err)
		}
		stats.Embedded += len(records)
		if opts.Progress != nil {
			opts.Progress(stats.Embedded, stats.Pending, stats.Batches)
		}
	}
	return stats, nil
}

// embeddingText 按「标题 + 摘要」拼接；空段不产生多余换行。
func embeddingText(input EmbeddingInput) string {
	parts := make([]string, 0, 2)
	if title := strings.TrimSpace(input.Title); title != "" {
		parts = append(parts, title)
	}
	if summary := strings.TrimSpace(input.Summary); summary != "" {
		parts = append(parts, summary)
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
