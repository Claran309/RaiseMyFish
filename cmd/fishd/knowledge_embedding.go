package main

// 本文件把 embedding 接入 fishd：语义召回包装与定时增量。
//
// 边界：
//   - embedding 是增强能力：装配或调用失败只记日志/降级为关键词，不影响主链路；
//   - 定时增量按知识库 revision 判断，revision 未变不重复扫库；
//   - 密钥只存在于 config 层解析后的字段，日志不输出。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/embedding"
	"RaiseMyFish/internal/knowledge"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// attachKnowledgeSemantic 在 FileReader 上叠加混合召回；未启用 embedding 时保持纯关键词。
func attachKnowledgeSemantic(cfg config.Config, db *gorm.DB, assembly *knowledgeAssembly, logger *zap.Logger) error {
	if !cfg.Embedding.Enabled || assembly == nil || assembly.Base == nil {
		return nil
	}
	client, err := embedding.New(embedding.Options{
		URL:       cfg.Embedding.URL,
		Model:     cfg.Embedding.Model,
		APIKey:    cfg.Embedding.Key,
		Dimension: cfg.Embedding.Dimension,
		BatchSize: cfg.Embedding.BatchSize,
		Timeout:   embedding.DefaultTimeout,
	})
	if err != nil {
		return fmt.Errorf("装配embedding客户端：%w", err)
	}
	store, err := database.NewKnowledgeEmbeddingRepo(db)
	if err != nil {
		return fmt.Errorf("装配知识向量仓储：%w", err)
	}
	var onError func(error)
	if logger != nil {
		onError = func(err error) {
			logger.Warn("知识语义召回降级为关键词", zap.String("action", "knowledge_semantic"), zap.Error(err))
		}
	}
	hybrid, err := knowledge.NewHybridReader(assembly.Base, client, store, knowledge.HybridOptions{
		Alpha:           cfg.Embedding.Alpha,
		MinSimilarity:   cfg.Embedding.MinSimilarity,
		OnSemanticError: onError,
	})
	if err != nil {
		return fmt.Errorf("装配混合召回：%w", err)
	}
	assembly.Reader = hybrid
	assembly.Embedder = client
	assembly.Store = store
	return nil
}

// knowledgeEmbeddingRefresher 保存上次成功向量化的 revision，避免空转。
type knowledgeEmbeddingRefresher struct {
	reader       *knowledge.FileReader
	embedder     knowledge.Embedder
	store        knowledge.EmbeddingStore
	batchSize    int
	lastRevision string
	logger       *zap.Logger
}

// runOnce 只在 revision 变化时执行增量；成功后才推进 lastRevision，失败下轮重试。
func (r *knowledgeEmbeddingRefresher) runOnce(ctx context.Context) error {
	info, err := r.reader.SnapshotInfo()
	if err != nil {
		return fmt.Errorf("读取知识版本：%w", err)
	}
	if info.Revision == r.lastRevision {
		return nil
	}
	stats, err := knowledge.RunEmbedding(ctx, r.reader, r.embedder, r.store, knowledge.EmbedOptions{
		BatchSize: r.batchSize,
		Progress: func(done, total, batches int) {
			r.logger.Info("知识向量化进度",
				zap.String("action", "knowledge_embed"),
				zap.Int("embedded", done),
				zap.Int("total", total),
				zap.Int("batches", batches),
			)
		},
	})
	if err != nil {
		return err
	}
	r.lastRevision = info.Revision
	r.logger.Info("知识向量化完成",
		zap.String("action", "knowledge_embed"),
		zap.String("revision", stats.Revision),
		zap.Int("total", stats.Total),
		zap.Int("skipped", stats.Skipped),
		zap.Int("pending", stats.Pending),
		zap.Int("batches", stats.Batches),
		zap.Int("embedded", stats.Embedded),
	)
	return nil
}

// startKnowledgeEmbeddingRefresh 立即检查一次，之后按配置间隔轮询 revision。
func startKnowledgeEmbeddingRefresh(ctx context.Context, cfg config.Config, assembly knowledgeAssembly, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Embedding.AutoRefresh || assembly.Base == nil || assembly.Embedder == nil || assembly.Store == nil || logger == nil {
		return noop
	}
	interval := time.Duration(cfg.Embedding.RefreshIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Hour
	}
	refresher := &knowledgeEmbeddingRefresher{
		reader:    assembly.Base,
		embedder:  assembly.Embedder,
		store:     assembly.Store,
		batchSize: cfg.Embedding.BatchSize,
		logger:    logger,
	}
	logger.Info("知识定时增量已启动",
		zap.String("action", "knowledge_embed"),
		zap.Duration("interval", interval),
	)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runOnce := func() {
			if err := refresher.runOnce(runCtx); err != nil && !errors.Is(err, context.Canceled) && runCtx.Err() == nil {
				logger.Error("知识向量化失败，等待下一轮", zap.String("action", "knowledge_embed"), zap.Error(err))
			}
		}
		runOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				runOnce()
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
