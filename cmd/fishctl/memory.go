package main

// 本文件实现 `fishctl memory` 运维命令：list / search / stats / embed 查询与入库，
// 以及 freeze / unfreeze 运行时冻结开关。
//
// 边界：
//   - 查询走 store.MemoryRepo 的只读方法，不在命令层写 SQL；
//   - 输出与 status 一致，使用单行 JSON；
//   - 这是本机运维视角，会列出私密行；给 Brain 的召回仍由 repository 过滤隐私。
//   - embed 走 memory.RunEmbedding，增量判断在领域层，命令层只打进度与摘要；
//     私密记忆是否外发由 [embedding] memory_allow_private 显式授权。
//   - freeze/unfreeze 只改状态文件，不读配置、不连数据库，PG 不可用时也能操作。

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
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

const (
	defaultMemoryLimit = 20
	maxMemoryLimit     = 200
)

var errMemoryUsage = errors.New("用法：fishctl memory list [--limit N] | memory search --q <关键词> [--limit N] | memory stats | memory embed | memory freeze | memory unfreeze")

// memoryFreezeFile 是冻结状态文件位置；测试可替换，生产固定与 fishd 共用默认路径。
var memoryFreezeFile = memory.DefaultFreezePath

// memoryReadRepo 是命令依赖的最小只读接口，database.MemoryRepo 直接满足。
type memoryReadRepo interface {
	ListRecords(ctx context.Context, limit int) ([]store.MemoryRecord, error)
	SearchRecords(ctx context.Context, q string, limit int) ([]store.MemoryRecord, error)
	CountByKind(ctx context.Context) (map[string]int, error)
}

var _ memoryReadRepo = (*database.MemoryRepo)(nil)

// memoryCommand 是解析后的子命令参数。
type memoryCommand struct {
	Action string
	Limit  int
	Query  string
}

// memoryItem 是 JSON 输出的单条记录，只暴露 ops 需要的字段。
type memoryItem struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Importance float64   `json:"importance"`
	At         time.Time `json:"at"`
}

// runMemory 解析参数、连接数据库并执行命令。
func runMemory(ctx context.Context, configPath, envPath string, args []string, out, stderr io.Writer) (result error) {
	spec, err := parseMemoryArgs(args, stderr)
	if err != nil {
		return err
	}
	if spec.Action == "help" {
		return nil
	}
	// 冻结开关是纯本地状态文件操作：不读配置、不连 PG，数据库故障时仍可执行。
	if spec.Action == "freeze" || spec.Action == "unfreeze" {
		return executeMemoryFreeze(spec, out)
	}
	if spec.Action == "embed" {
		return runMemoryEmbed(ctx, configPath, envPath, out, stderr)
	}

	// 只读命令不采集直播，跳过 collector.live 校验，避免凭据缺失时不可用。
	cfg, err := config.LoadForCLI(configPath, envPath)
	if err != nil {
		return err
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
	repo, err := database.NewMemoryRepo(resources.DB)
	if err != nil {
		return err
	}
	return executeMemory(probe, repo, spec, out)
}

// parseMemoryArgs 解析子命令与参数；--help 返回 help 动作，由调用方直接结束。
func parseMemoryArgs(args []string, stderr io.Writer) (memoryCommand, error) {
	if len(args) == 0 {
		return memoryCommand{}, errMemoryUsage
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl memory list [--limit N]") }
		limit := fs.Int("limit", defaultMemoryLimit, "最多返回条数")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return memoryCommand{Action: "help"}, nil
			}
			return memoryCommand{}, errMemoryUsage
		}
		if fs.NArg() != 0 {
			return memoryCommand{}, errMemoryUsage
		}
		if *limit < 1 || *limit > maxMemoryLimit {
			return memoryCommand{}, fmt.Errorf("limit 必须在1至%d之间", maxMemoryLimit)
		}
		return memoryCommand{Action: "list", Limit: *limit}, nil

	case "search":
		fs := flag.NewFlagSet("memory search", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl memory search --q <关键词> [--limit N]") }
		query := fs.String("q", "", "搜索关键词")
		limit := fs.Int("limit", defaultMemoryLimit, "最多返回条数")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return memoryCommand{Action: "help"}, nil
			}
			return memoryCommand{}, errMemoryUsage
		}
		if fs.NArg() != 0 {
			return memoryCommand{}, errMemoryUsage
		}
		if strings.TrimSpace(*query) == "" {
			return memoryCommand{}, errors.New("memory search 需要非空 --q 关键词")
		}
		if *limit < 1 || *limit > maxMemoryLimit {
			return memoryCommand{}, fmt.Errorf("limit 必须在1至%d之间", maxMemoryLimit)
		}
		return memoryCommand{Action: "search", Limit: *limit, Query: strings.TrimSpace(*query)}, nil

	case "stats":
		fs := flag.NewFlagSet("memory stats", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl memory stats") }
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return memoryCommand{Action: "help"}, nil
			}
			return memoryCommand{}, errMemoryUsage
		}
		if fs.NArg() != 0 {
			return memoryCommand{}, errMemoryUsage
		}
		return memoryCommand{Action: "stats", Limit: defaultMemoryLimit}, nil

	case "embed":
		fs := flag.NewFlagSet("memory embed", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintln(stderr, "用法：fishctl memory embed") }
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return memoryCommand{Action: "help"}, nil
			}
			return memoryCommand{}, errMemoryUsage
		}
		if fs.NArg() != 0 {
			return memoryCommand{}, errMemoryUsage
		}
		return memoryCommand{Action: "embed"}, nil

	case "freeze", "unfreeze":
		fs := flag.NewFlagSet("memory "+args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprintf(stderr, "用法：fishctl memory %s\n", args[0]) }
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return memoryCommand{Action: "help"}, nil
			}
			return memoryCommand{}, errMemoryUsage
		}
		if fs.NArg() != 0 {
			return memoryCommand{}, errMemoryUsage
		}
		return memoryCommand{Action: args[0]}, nil

	default:
		return memoryCommand{}, errMemoryUsage
	}
}

// memoryEmbedOutput 是给运维看的单行 JSON 摘要。
type memoryEmbedOutput struct {
	Total    int `json:"total"`
	Skipped  int `json:"skipped"`
	Pending  int `json:"pending"`
	Batches  int `json:"batches"`
	Embedded int `json:"embedded"`
	// AllowPrivate 回显本次是否获准外发私密记忆摘要（对应 [embedding] memory_allow_private）。
	AllowPrivate bool `json:"allow_private"`
}

// runMemoryEmbed 装配 embedding 客户端与仓储，执行一次增量向量化。
//
// 密钥只来自配置层已解析的环境变量，本函数不读取也不打印。
func runMemoryEmbed(ctx context.Context, configPath, envPath string, out, stderr io.Writer) (result error) {
	cfg, err := config.LoadForCLI(configPath, envPath)
	if err != nil {
		return err
	}
	if !cfg.Embedding.Enabled {
		return errors.New("embedding 未启用：请先在 [embedding] 设置 enabled=true")
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
	repo, err := database.NewMemoryRepo(resources.DB)
	if err != nil {
		return err
	}
	return executeMemoryEmbed(probe, repo, client, cfg.Embedding.BatchSize, cfg.Embedding.MemoryAllowPrivate, out, stderr)
}

// executeMemoryEmbed 执行增量入库并输出摘要；进度逐批写 stderr。
//
// allowPrivate 来自 [embedding] memory_allow_private：默认 false 时私密记忆
// 不进入外发请求，归档/失效记录在任何情况下都不外发。
func executeMemoryEmbed(ctx context.Context, store memory.EmbeddingStore, embedder memory.Embedder, batchSize int, allowPrivate bool, out, progressOut io.Writer) error {
	if store == nil || embedder == nil {
		return errors.New("记忆入库依赖不能为空")
	}
	doneBatches := 0
	stats, err := memory.RunEmbedding(ctx, store, embedder, memory.EmbedOptions{
		BatchSize:    batchSize,
		AllowPrivate: allowPrivate,
		Progress: func(done, total, batches int) {
			doneBatches++
			fmt.Fprintf(progressOut, "memory embed: 已处理 %d/%d 条（%d/%d 批）\n", done, total, doneBatches, batches)
		},
	})
	if err != nil {
		return fmt.Errorf("记忆入库失败：%w", err)
	}
	summary := memoryEmbedOutput{
		Total:        stats.Total,
		Skipped:      stats.Skipped,
		Pending:      stats.Pending,
		Batches:      stats.Batches,
		Embedded:     stats.Embedded,
		AllowPrivate: allowPrivate,
	}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出记忆入库结果：%w", err)
	}
	return nil
}

// executeMemoryFreeze 执行冻结/解冻并输出最终状态；重复调用保持幂等。
func executeMemoryFreeze(spec memoryCommand, out io.Writer) error {
	path := memoryFreezeFile
	switch spec.Action {
	case "freeze":
		if err := memory.Freeze(path); err != nil {
			return err
		}
	case "unfreeze":
		if err := memory.Unfreeze(path); err != nil {
			return err
		}
	default:
		return errMemoryUsage
	}
	summary := struct {
		Frozen bool   `json:"frozen"`
		Path   string `json:"path"`
	}{Frozen: spec.Action == "freeze", Path: path}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出记忆冻结状态：%w", err)
	}
	return nil
}

// executeMemory 执行已解析命令，所有查询错误向上抛，不吞成空数据。
func executeMemory(ctx context.Context, repo memoryReadRepo, spec memoryCommand, out io.Writer) error {
	switch spec.Action {
	case "list":
		records, err := repo.ListRecords(ctx, spec.Limit)
		if err != nil {
			return fmt.Errorf("读取记忆列表：%w", err)
		}
		return writeMemoryRecords(out, "", records)

	case "search":
		records, err := repo.SearchRecords(ctx, spec.Query, spec.Limit)
		if err != nil {
			return fmt.Errorf("搜索记忆记录：%w", err)
		}
		return writeMemoryRecords(out, spec.Query, records)

	case "stats":
		counts, err := repo.CountByKind(ctx)
		if err != nil {
			return fmt.Errorf("统计记忆类型：%w", err)
		}
		if counts == nil {
			counts = map[string]int{}
		}
		total := 0
		for _, n := range counts {
			total += n
		}
		summary := struct {
			Counts map[string]int `json:"counts"`
			Total  int            `json:"total"`
		}{counts, total}
		if err := json.NewEncoder(out).Encode(summary); err != nil {
			return fmt.Errorf("输出记忆统计：%w", err)
		}
		return nil

	default:
		return errMemoryUsage
	}
}

// writeMemoryRecords 输出记录列表；空结果输出 []，不输出 null。
func writeMemoryRecords(out io.Writer, query string, records []store.MemoryRecord) error {
	items := make([]memoryItem, 0, len(records))
	for _, rec := range records {
		at := rec.CreatedAt
		if at.IsZero() {
			at = rec.ValidFrom
		}
		items = append(items, memoryItem{
			ID:         rec.ID,
			Kind:       rec.Kind,
			Title:      rec.Title,
			Importance: rec.Importance,
			At:         at,
		})
	}
	summary := struct {
		Query   string       `json:"query,omitempty"`
		Records []memoryItem `json:"records"`
		Count   int          `json:"count"`
	}{query, items, len(items)}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出记忆记录：%w", err)
	}
	return nil
}
