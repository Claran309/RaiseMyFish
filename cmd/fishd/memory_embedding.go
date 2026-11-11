package main

// 本文件是 M08：记忆 / 观众档案向量的定时增量刷新。
//
// 背景：memory_records 与 viewer_profiles 的向量此前只能靠
// `fishctl memory embed` / `fishctl viewer embed` 手工跑，新提炼的事实
// 和刚重建的档案要等到有人记得执行命令才会被语义召回看见。
//
// 与知识库刷新（knowledge_embedding.go）同一套模式：
//   - 共用 [embedding] auto_refresh 总开关；
//   - 立即跑一轮，之后按间隔轮询；
//   - 失败只记日志、下一轮重试，绝不影响采集/消费主链路。
//
// 增量语义在领域层（memory.RunEmbedding / viewer.RunEmbedding）：
// 只重算「没有向量」或「内容 hash 变了」的行，没变化不花 embedding 费用。

import (
	"context"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/embedding"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// vectorEmbedder 是两个领域包 Embedder 的公共形状；*embedding.Client 满足它。
type vectorEmbedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float64, error)
}

// defaultMemoryRefreshInterval 是未配置 memory_refresh_interval_seconds 时的节奏。
const defaultMemoryRefreshInterval = 5 * time.Minute

// buildEmbeddingClient 是 embedding 客户端的唯一装配点。
//
// 返回 nil 表示「未启用或装配失败」：调用方一律降级为纯关键词检索，
// 不允许因为向量服务不可用而挡住主链路。
// usage 为 nil 时仍返回可用客户端，只是调用不进成本账本。
func buildEmbeddingClient(cfg config.Config, usage store.UsageRepo, logger *zap.Logger) *embedding.Client {
	if !cfg.Embedding.Enabled {
		return nil
	}
	var onError func(error)
	if logger != nil {
		onError = func(err error) {
			logger.Warn("embedding调用记账失败",
				zap.String("action", "embed_usage"),
				zap.String("result", "failed"),
				zap.Error(err),
			)
		}
	}
	client, err := embedding.New(embedding.Options{
		URL:       cfg.Embedding.URL,
		Model:     cfg.Embedding.Model,
		APIKey:    cfg.Embedding.Key,
		Dimension: cfg.Embedding.Dimension,
		BatchSize: cfg.Embedding.BatchSize,
		Timeout:   embedding.DefaultTimeout,
		OnUsage:   embedding.UsageSinkFor(usage, cfg.Embedding.Model, onError),
	})
	if err != nil {
		if logger != nil {
			logger.Warn("embedding客户端装配失败，语义检索降级为关键词",
				zap.String("action", "semantic_setup"), zap.Error(err))
		}
		return nil
	}
	return client
}

// memoryEmbeddingRefresher 跑一轮「记忆 + 档案」增量向量化。
type memoryEmbeddingRefresher struct {
	memoryStore memory.EmbeddingStore
	viewerStore viewer.EmbeddingStore
	embedder    vectorEmbedder
	batchSize   int
	// memoryAllowPrivate 透传 [embedding] memory_allow_private：默认 false，
	// 私密记忆摘要不离开本机，也就不参与记忆向量化。
	memoryAllowPrivate bool
	// 两侧的索引指纹各自独立：领域模板不同，换模型/提供方后都必须全量重算。
	memoryFingerprint string
	viewerFingerprint string
	logger            *zap.Logger
}

// embedSummary 把两个领域包的统计收敛成同一个形状（字段名相同、类型不同）。
type embedSummary struct {
	total, skipped, pending, embedded, rejected int
}

// refreshOnce 依次刷新两类索引。
//
// 记忆与档案互不阻塞：一侧失败另一侧照跑；失败只记日志，下一轮自然重试。
func (r *memoryEmbeddingRefresher) refreshOnce(ctx context.Context) {
	r.embedAndLog(ctx, "memory", func() (embedSummary, error) {
		stats, err := memory.RunEmbedding(ctx, r.memoryStore, r.embedder, memory.EmbedOptions{
			BatchSize:    r.batchSize,
			AllowPrivate: r.memoryAllowPrivate,
			Fingerprint:  r.memoryFingerprint,
		})
		return embedSummary{stats.Total, stats.Skipped, stats.Pending, stats.Embedded, stats.Rejected}, err
	})
	r.embedAndLog(ctx, "viewer", func() (embedSummary, error) {
		stats, err := viewer.RunEmbedding(ctx, r.viewerStore, r.embedder, viewer.EmbedOptions{
			BatchSize:   r.batchSize,
			Fingerprint: r.viewerFingerprint,
		})
		return embedSummary{stats.Total, stats.Skipped, stats.Pending, stats.Embedded, stats.Rejected}, err
	})
}

// embedAndLog 执行一类增量并按统一口径记日志。
func (r *memoryEmbeddingRefresher) embedAndLog(ctx context.Context, kind string, run func() (embedSummary, error)) {
	stats, err := run()
	if err != nil {
		if ctx.Err() != nil {
			return // 正常停止，不当作失败
		}
		r.logger.Warn("向量增量刷新失败，等待下一轮",
			zap.String("action", "vector_embed"),
			zap.String("index", kind),
			zap.Error(err),
		)
		return
	}
	// 没有实际写入时不打日志，避免每 5 分钟刷一条无意义记录；
	// 但被 CAS 拒绝（计算期间源已变化）必须可见，否则会误以为索引已齐全。
	if stats.embedded == 0 && stats.rejected == 0 {
		return
	}
	r.logger.Info("向量增量刷新完成",
		zap.String("action", "vector_embed"),
		zap.String("index", kind),
		zap.Int("total", stats.total),
		zap.Int("skipped", stats.skipped),
		zap.Int("pending", stats.pending),
		zap.Int("embedded", stats.embedded),
		zap.Int("rejected", stats.rejected),
	)
}

// startMemoryEmbeddingRefresh 立即跑一轮，之后按 memory_refresh_interval_seconds 轮询。
//
// 关闭条件（任一）：embedding 未启用、auto_refresh 关闭、数据库/日志不可用。
// 返回的 stop 会取消并等待 goroutine 退出。
func startMemoryEmbeddingRefresh(ctx context.Context, cfg config.Config, db *gorm.DB, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Embedding.Enabled || !cfg.Embedding.AutoRefresh || db == nil || logger == nil {
		return noop
	}
	// 定时增量也要进成本账本；账本装配失败只降级为不记账，不挡向量化。
	var usage store.UsageRepo
	if repo, uerr := database.NewUsageRepo(db); uerr == nil {
		usage = repo
	} else {
		logger.Warn("向量刷新账本装配失败，本次调用不记成本", zap.String("action", "embed_usage"), zap.Error(uerr))
	}
	client := buildEmbeddingClient(cfg, usage, logger)
	if client == nil {
		return noop
	}
	memoryStore, err := database.NewMemoryRepo(db)
	if err != nil {
		logger.Warn("记忆向量刷新装配失败", zap.String("action", "vector_embed"), zap.Error(err))
		return noop
	}
	viewerStore, err := database.NewViewerRepo(db)
	if err != nil {
		logger.Warn("档案向量刷新装配失败", zap.String("action", "vector_embed"), zap.Error(err))
		return noop
	}
	interval := time.Duration(cfg.Embedding.MemoryRefreshIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultMemoryRefreshInterval
	}
	refresher := &memoryEmbeddingRefresher{
		memoryStore:        memoryStore,
		viewerStore:        viewerStore,
		embedder:           client,
		batchSize:          cfg.Embedding.BatchSize,
		memoryAllowPrivate: cfg.Embedding.MemoryAllowPrivate,
		memoryFingerprint:  client.Fingerprint(memory.IndexTemplate),
		viewerFingerprint:  client.Fingerprint(viewer.IndexTemplate),
		logger:             logger,
	}
	logger.Info("记忆/档案向量定时增量已启动",
		zap.String("action", "vector_embed"),
		zap.Duration("interval", interval),
		zap.Bool("memory_allow_private", cfg.Embedding.MemoryAllowPrivate),
	)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.refreshOnce(runCtx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				refresher.refreshOnce(runCtx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// 确保 *embedding.Client 满足两个领域包的 Embedder 接口（编译期约定）。
var _ memory.Embedder = (*embedding.Client)(nil)
var _ viewer.Embedder = (*embedding.Client)(nil)
