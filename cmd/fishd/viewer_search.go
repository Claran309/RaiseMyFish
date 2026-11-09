package main

// 本文件装配观众档案检索：仓储 + 可选语义 embedding + 人工别名/同音表。
//
// 别名表是增强：文件缺失/解析失败只降级为「无别名」，不阻塞 viewer_search 注册；
// 热更新在每次查询时按文件指纹触发，不需要重启或后台 goroutine。

import (
	"strings"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// buildViewerAliasSource 按配置装载别名文件；未配置或构造失败返回 nil。
func buildViewerAliasSource(cfg config.Viewer, log *zap.Logger) viewer.AliasSource {
	path := strings.TrimSpace(cfg.AliasFile)
	if path == "" {
		return nil
	}
	source, err := viewer.NewAliasFile(path, func(err error) {
		log.Warn("观众别名文件重载失败，继续使用旧快照",
			zap.String("action", "viewer_alias"),
			zap.Error(err),
		)
	})
	if err != nil {
		log.Warn("观众别名文件未启用", zap.String("action", "viewer_alias"), zap.Error(err))
		return nil
	}
	// 立即读一次，启动日志能看出别名表是否真的生效（失败由回调记录）。
	index := source.AliasIndex()
	log.Info("观众别名表已加载",
		zap.String("action", "viewer_alias"),
		zap.Int("confirmed", len(index.Confirmed)),
		zap.Int("rejected", len(index.Rejected)),
	)
	return source
}

// buildViewerSearcher 构造生产档案检索；embedder 可为 nil（纯关键词）。
//
// fingerprint 是当前向量空间指纹（提供方/模型/维度/模板）；有 embedder 时必填，
// 否则语义查询无法拒绝跨模型复用的旧向量。
func buildViewerSearcher(cfg config.Config, source viewer.ProfileSource, embedder viewer.Embedder, fingerprint string, log *zap.Logger) (*viewer.Searcher, error) {
	return viewer.NewSearcher(source, embedder, viewer.SearchOptions{
		Alpha:         cfg.Embedding.Alpha,
		MinSimilarity: cfg.Embedding.MinSimilarity,
		Fingerprint:   fingerprint,
		OnSemanticError: func(err error) {
			log.Warn("档案语义检索降级为关键词", zap.String("action", "viewer_semantic"), zap.Error(err))
		},
		AliasSource: buildViewerAliasSource(cfg.Viewer, log),
	})
}
