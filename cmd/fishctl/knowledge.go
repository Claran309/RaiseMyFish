package main

// 本文件实现 `fishctl knowledge embed`：把知识条目批量向量化写入 pgvector。
//
// 边界：
//   - 密钥只来自配置层已解析的环境变量，命令层不读取也不打印密钥；
//   - 只通过 knowledge.RunEmbedding 调用 Embedder / EmbeddingStore，不在这里写 SQL；
//   - 进度写 stderr，机器可读结果写 stdout 单行 JSON。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/embedding"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/knowledge"
)

var errKnowledgeUsage = errors.New("用法：fishctl knowledge embed")

// knowledgeEmbedOutput 是给运维看的单行 JSON 摘要。
type knowledgeEmbedOutput struct {
	Revision string `json:"revision"`
	Total    int    `json:"total"`
	Skipped  int    `json:"skipped"`
	Pending  int    `json:"pending"`
	Batches  int    `json:"batches"`
	Embedded int    `json:"embedded"`
}

// runKnowledge 解析参数、加载配置、装配 Reader/Embedding/仓储并执行入库。
func runKnowledge(ctx context.Context, configPath, envPath string, args []string, out, stderr io.Writer) (result error) {
	if len(args) == 0 || args[0] != "embed" {
		return errKnowledgeUsage
	}
	fs := flag.NewFlagSet("knowledge embed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl knowledge embed") }
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errKnowledgeUsage
	}
	if fs.NArg() != 0 {
		return errKnowledgeUsage
	}

	cfg, err := config.LoadForCLI(configPath, envPath)
	if err != nil {
		return err
	}
	if !cfg.Knowledge.Enabled {
		return errors.New("知识库未启用：请先在 [knowledge] 设置 enabled=true")
	}
	if !cfg.Embedding.Enabled {
		return errors.New("embedding 未启用：请先在 [embedding] 设置 enabled=true")
	}
	reader, err := knowledge.NewFileReader(ctx, cfg.Knowledge.Dir)
	if err != nil {
		return fmt.Errorf("加载知识快照：%w", err)
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

	probe, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.TimeoutSeconds)*time.Second)
	defer cancel()
	resources, err := infra.Setup(probe, cfg)
	if err != nil {
		return err
	}
	if resources == nil {
		return errors.New("基础设施未就绪")
	}
	defer func() { result = errors.Join(result, resources.Close(ctx)) }()
	if err := database.Ready(probe, resources.DB); err != nil {
		return err
	}
	store, err := database.NewKnowledgeEmbeddingRepo(resources.DB)
	if err != nil {
		return err
	}
	return executeKnowledgeEmbed(ctx, reader, client, store, cfg.Embedding.BatchSize, out, stderr)
}

// executeKnowledgeEmbed 执行增量入库并输出摘要；进度逐批写 stderr。
func executeKnowledgeEmbed(ctx context.Context, reader *knowledge.FileReader, embedder knowledge.Embedder, store knowledge.EmbeddingStore, batchSize int, out, progressOut io.Writer) error {
	if reader == nil || embedder == nil || store == nil {
		return errors.New("知识入库依赖不能为空")
	}
	doneBatches := 0
	stats, err := knowledge.RunEmbedding(ctx, reader, embedder, store, knowledge.EmbedOptions{
		BatchSize: batchSize,
		Progress: func(done, total, batches int) {
			doneBatches++
			fmt.Fprintf(progressOut, "knowledge embed: 已处理 %d/%d 条（%d/%d 批）\n", done, total, doneBatches, batches)
		},
	})
	if err != nil {
		return fmt.Errorf("知识入库失败：%w", err)
	}
	summary := knowledgeEmbedOutput{
		Revision: stats.Revision,
		Total:    stats.Total,
		Skipped:  stats.Skipped,
		Pending:  stats.Pending,
		Batches:  stats.Batches,
		Embedded: stats.Embedded,
	}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出知识入库结果：%w", err)
	}
	return nil
}
