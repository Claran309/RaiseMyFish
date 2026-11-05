package main

// 本文件实现 `fishctl viewer`：embed 增量向量化档案，search 按描述找观众。
//
// 边界：
//   - 密钥只来自配置层已解析的环境变量，命令层不读取也不打印密钥；
//   - 入库只通过 viewer.RunEmbedding，查询只通过 viewer.Searcher，不在这里写 SQL；
//   - embedding 未启用时 search 仍按关键词工作；运行期语义失败也降级为关键词并写 stderr。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/embedding"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/viewer"
)

const (
	defaultViewerLimit = 5
	maxViewerLimit     = 50
)

var errViewerUsage = errors.New("用法：fishctl viewer embed | viewer search --q <描述> [--limit N]")

// viewerCommand 是解析后的子命令参数。
type viewerCommand struct {
	Action string
	Query  string
	Limit  int
}

// viewerEmbedOutput 是给运维看的单行 JSON 摘要。
type viewerEmbedOutput struct {
	Total    int `json:"total"`
	Skipped  int `json:"skipped"`
	Pending  int `json:"pending"`
	Batches  int `json:"batches"`
	Embedded int `json:"embedded"`
}

// viewerItem 是检索命中的 JSON 投影，只暴露认人需要的字段。
type viewerItem struct {
	UID         string   `json:"uid"`
	Name        string   `json:"name"`
	Hits        int      `json:"hits"`
	Replies     int      `json:"replies"`
	Similarity  float64  `json:"similarity"`
	RecentTexts []string `json:"recent_texts,omitempty"`
}

// runViewer 解析参数、加载配置、连接数据库并执行命令。
func runViewer(ctx context.Context, configPath, envPath string, args []string, out, stderr io.Writer) (result error) {
	spec, err := parseViewerArgs(args, stderr)
	if err != nil {
		return err
	}
	if spec.Action == "help" {
		return nil
	}

	cfg, err := config.LoadForCLI(configPath, envPath)
	if err != nil {
		return err
	}
	if spec.Action == "embed" && !cfg.Embedding.Enabled {
		return errors.New("embedding 未启用：请先在 [embedding] 设置 enabled=true")
	}
	var client *embedding.Client
	if cfg.Embedding.Enabled {
		client, err = embedding.New(embedding.Options{
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
	repo, err := database.NewViewerRepo(resources.DB)
	if err != nil {
		return err
	}

	switch spec.Action {
	case "embed":
		return executeViewerEmbed(probe, repo, client, cfg.Embedding.BatchSize, out, stderr)
	case "search":
		var embedder viewer.Embedder
		if client != nil {
			embedder = client
		}
		searcher, err := viewer.NewSearcher(repo, embedder, viewer.SearchOptions{
			Alpha: cfg.Embedding.Alpha,
			OnSemanticError: func(err error) {
				fmt.Fprintf(stderr, "viewer search: 语义检索不可用，已降级为关键词：%v\n", err)
			},
		})
		if err != nil {
			return err
		}
		return executeViewerSearch(probe, searcher, spec, out)
	default:
		return errViewerUsage
	}
}

// parseViewerArgs 解析子命令与参数；--help 返回 help 动作。
func parseViewerArgs(args []string, stderr io.Writer) (viewerCommand, error) {
	if len(args) == 0 {
		return viewerCommand{}, errViewerUsage
	}
	switch args[0] {
	case "embed":
		fs := flag.NewFlagSet("viewer embed", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl viewer embed") }
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return viewerCommand{Action: "help"}, nil
			}
			return viewerCommand{}, errViewerUsage
		}
		if fs.NArg() != 0 {
			return viewerCommand{}, errViewerUsage
		}
		return viewerCommand{Action: "embed"}, nil

	case "search":
		fs := flag.NewFlagSet("viewer search", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl viewer search --q <描述> [--limit N]") }
		query := fs.String("q", "", "描述词，如「那个爱要白饭的人」")
		limit := fs.Int("limit", defaultViewerLimit, "最多返回条数")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return viewerCommand{Action: "help"}, nil
			}
			return viewerCommand{}, errViewerUsage
		}
		if fs.NArg() != 0 {
			return viewerCommand{}, errViewerUsage
		}
		if strings.TrimSpace(*query) == "" {
			return viewerCommand{}, errors.New("viewer search 需要非空 --q 描述词")
		}
		if *limit < 1 || *limit > maxViewerLimit {
			return viewerCommand{}, fmt.Errorf("limit 必须在1至%d之间", maxViewerLimit)
		}
		return viewerCommand{Action: "search", Query: strings.TrimSpace(*query), Limit: *limit}, nil

	default:
		return viewerCommand{}, errViewerUsage
	}
}

// executeViewerEmbed 执行增量入库并输出摘要；进度逐批写 stderr。
func executeViewerEmbed(ctx context.Context, store viewer.EmbeddingStore, embedder viewer.Embedder, batchSize int, out, progressOut io.Writer) error {
	if store == nil || embedder == nil {
		return errors.New("档案入库依赖不能为空")
	}
	doneBatches := 0
	stats, err := viewer.RunEmbedding(ctx, store, embedder, viewer.EmbedOptions{
		BatchSize: batchSize,
		Progress: func(done, total, batches int) {
			doneBatches++
			fmt.Fprintf(progressOut, "viewer embed: 已处理 %d/%d 条（%d/%d 批）\n", done, total, doneBatches, batches)
		},
	})
	if err != nil {
		return fmt.Errorf("档案入库失败：%w", err)
	}
	summary := viewerEmbedOutput{
		Total:    stats.Total,
		Skipped:  stats.Skipped,
		Pending:  stats.Pending,
		Batches:  stats.Batches,
		Embedded: stats.Embedded,
	}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出档案入库结果：%w", err)
	}
	return nil
}

// executeViewerSearch 执行检索并输出命中的 uid 档案。
func executeViewerSearch(ctx context.Context, searcher *viewer.Searcher, spec viewerCommand, out io.Writer) error {
	hits, err := searcher.Search(ctx, spec.Query, spec.Limit)
	if err != nil {
		return fmt.Errorf("检索观众档案：%w", err)
	}
	items := make([]viewerItem, 0, len(hits))
	for _, hit := range hits {
		texts := hit.Profile.RecentTexts
		if len(texts) > 3 {
			texts = texts[:3]
		}
		items = append(items, viewerItem{
			UID:         hit.Profile.UID,
			Name:        hit.Profile.Name,
			Hits:        hit.Profile.Hits,
			Replies:     hit.Profile.Replies,
			Similarity:  hit.Similarity,
			RecentTexts: texts,
		})
	}
	summary := struct {
		Query    string       `json:"query"`
		Profiles []viewerItem `json:"profiles"`
		Count    int          `json:"count"`
	}{spec.Query, items, len(items)}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出观众检索结果：%w", err)
	}
	return nil
}
