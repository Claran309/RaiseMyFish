// fishmcp 把 ReAct 工具箱暴露为 MCP stdio 服务器。
//
// 边界：
//   - 只做协议转发，不复制 ReAct 的思考链；工具护栏（白名单/额度/限频/副作用开关）沿用 reasoning；
//   - stdout 只写 JSON-RPC 响应，日志走 infra.NewLogger 的 stderr/文件双输出；
//   - 凭据只来自配置解析后的环境值，不打印 cookie/token。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/gift"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/mcp"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/proactive"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/viewer"
	"RaiseMyFish/internal/watch"
	"RaiseMyFish/internal/webtools"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// defaultOwnerMID 是主人的 B站 UID（与 fishd 的派发单一致）：永不取关、私信默认允许。
const defaultOwnerMID = "313591854"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (result error) {
	fs := flag.NewFlagSet("fishmcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "config/fishd.toml", "TOML配置文件")
	envPath := fs.String("env-file", ".env", "本地环境文件")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("命令参数无效")
	}
	if fs.NArg() > 0 {
		return errors.New("用法：fishmcp [--config 路径] [--env-file 路径]")
	}
	// MCP 不采集直播，不需要 collector.live 的运行期字段齐全。
	cfg, err := config.LoadForCLI(*path, *envPath)
	if err != nil {
		return err
	}
	if !cfg.MCP.Enabled {
		return errors.New("MCP 未启用（[mcp] enabled=false）")
	}
	logger, closeLog, err := infra.NewLogger(cfg.Log.Level, cfg.Log.Dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeLog()) }()

	resources, err := infra.Setup(ctx, cfg)
	if err != nil {
		logger.Error("基础设施初始化失败", zap.Error(err))
		return err
	}
	defer func() { result = errors.Join(result, resources.Close(ctx)) }()

	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.TimeoutSeconds)*time.Second)
	err = database.Ready(readyCtx, resources.DB)
	cancel()
	if err != nil {
		return err
	}

	box, err := buildToolbox(ctx, cfg, resources.DB, logger)
	if err != nil {
		return err
	}
	server, err := mcp.NewServer(box, cfg.MCP.AllowedTools, logger)
	if err != nil {
		return err
	}
	logger.Info("MCP 服务器就绪",
		zap.String("action", "mcp_start"),
		zap.Int("tools", len(server.Tools())),
		zap.Bool("side_effect_tools", cfg.Reasoning.AllowSideEffectTools),
		zap.String("room", strings.TrimSpace(cfg.Collector.Live.RoomID)),
	)
	return server.Serve(ctx, stdin, stdout)
}

// buildToolbox 按 fishd 的同一口径拼装工具箱；MCP 没有事件，Scope 固定为直播房间。
func buildToolbox(ctx context.Context, cfg config.Config, db *gorm.DB, log *zap.Logger) (*reasoning.Toolbox, error) {
	room := strings.TrimSpace(cfg.Collector.Live.RoomID)
	scope := memory.Scope{Channel: event.ChLive, Session: room}
	if room == "" {
		log.Warn("MCP 会话 Scope 没有房间号，记忆/历史工具按空会话查询",
			zap.String("action", "mcp_scope"))
	}

	memRepo, err := database.NewMemoryRepo(db)
	if err != nil {
		return nil, fmt.Errorf("装配记忆仓储失败：%w", err)
	}
	memReader, err := memory.NewReader(memRepo, memRepo, memory.ReaderOptions{})
	if err != nil {
		return nil, fmt.Errorf("装配记忆 Reader 失败：%w", err)
	}

	// 知识是增强能力：加载失败只降级，knowledge_search 不注册。
	var know knowledge.Reader
	if cfg.Knowledge.Enabled {
		reader, err := knowledge.NewFileReader(ctx, cfg.Knowledge.Dir)
		if err != nil {
			log.Warn("知识快照加载失败，knowledge_search 不注册",
				zap.String("action", "mcp_knowledge"), zap.Error(err))
		} else {
			know = reader
		}
	}

	events, err := database.NewEventRepo(db)
	if err != nil {
		return nil, fmt.Errorf("装配事件仓储失败：%w", err)
	}

	var viewerSearch reasoning.ViewerProfileSearcher
	if cfg.Viewer.Enabled {
		repo, err := database.NewViewerRepo(db)
		if err != nil {
			log.Warn("观众档案仓储装配失败，viewer_search 不注册",
				zap.String("action", "mcp_viewer"), zap.Error(err))
		} else if searcher, err := viewer.NewSearcher(repo, nil, viewer.SearchOptions{
			Alpha:         cfg.Embedding.Alpha,
			MinSimilarity: cfg.Embedding.MinSimilarity,
		}); err != nil {
			log.Warn("观众档案检索装配失败，viewer_search 不注册",
				zap.String("action", "mcp_viewer"), zap.Error(err))
		} else {
			viewerSearch = searcher
		}
	}

	quota := buildGiftQuota(cfg, log)
	execs := buildToolExecutors(cfg, log)

	recall, err := reasoning.BuildTools(reasoning.ToolDeps{
		Memory:    memReader,
		Knowledge: know,
		Presence:  presence.NewTable().Summary,
		History:   events,
		Scope:     scope,
	})
	if err != nil {
		return nil, fmt.Errorf("装配基础工具失败：%w", err)
	}
	readonly, err := reasoning.BuildReadOnlyTools(reasoning.ReadOnlyDeps{
		Viewer:       events,
		ViewerSearch: viewerSearch,
		Replies:      events,
		Live:         execs.Live,
		Quota:        quota,
		QuotaLimit:   cfg.Gift.DailyLimit,
		Frame:        execs.Frame,
		FrameRead:    execs.FrameRead,
		Search:       execs.Search,
		Room:         room,
	})
	if err != nil {
		return nil, fmt.Errorf("装配只读工具失败：%w", err)
	}
	boxes := []*reasoning.Toolbox{recall, readonly}
	if cfg.Reasoning.AllowSideEffectTools {
		side, err := reasoning.BuildSideEffectTools(sideEffectDeps(cfg, quota, execs, room, nil, log))
		if err != nil {
			return nil, fmt.Errorf("装配副作用工具失败：%w", err)
		}
		boxes = append(boxes, side)
	}
	box, err := reasoning.MergeToolboxes(boxes...)
	if err != nil {
		return nil, fmt.Errorf("合并工具箱失败：%w", err)
	}
	return box, nil
}

// toolExecutors 与 fishd 的同名结构保持一致；缺凭据时字段为 nil（工具回「未配置」）。
type toolExecutors struct {
	Live      reasoning.LiveStatusSource
	Social    reasoning.SocialExecutor
	Gift      reasoning.GiftExecutor
	Frame     reasoning.FrameSource
	FrameRead reasoning.FrameReader
	Search    reasoning.WebSearcher
}

// buildToolExecutors 按环境装配执行器；任何一项失败只降级该项。
func buildToolExecutors(cfg config.Config, log *zap.Logger) toolExecutors {
	var execs toolExecutors
	if cfg.Web.SearchURL != "" {
		searcher, err := webtools.NewHTTP(webtools.Options{
			URL:            cfg.Web.SearchURL,
			Key:            cfg.Web.ResolveKey(),
			Protocol:       cfg.Web.Protocol,
			AllowedDomains: cfg.Web.AllowedDomains,
		})
		if err != nil {
			log.Warn("搜索执行器未装配",
				zap.String("action", "tool_executor_setup"),
				zap.String("reason", "bad_search_config"),
				zap.Error(err),
			)
		} else {
			execs.Search = searcher
		}
	}

	cookie := cfg.Collector.Live.Cookie
	if cookie == "" && strings.TrimSpace(cfg.Collector.Live.CookieFile) == "" {
		log.Info("工具执行器未装配：未配置凭据",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", "no_cookie"),
			zap.Bool("web_search", execs.Search != nil),
		)
		return execs
	}

	client, err := bili.New(bili.Options{
		Cookie:     cookie,
		CookieFile: cfg.Collector.Live.CookieFile,
		SelfMID:    selfMID(cfg),
		Timeout:    10 * time.Second,
	})
	if err != nil {
		log.Warn("工具执行器未装配：凭据不可用",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", "bad_cookie"),
			zap.Error(err),
		)
		return execs
	}
	execs.Live = client
	if client.HasCSRF() {
		execs.Social = client
	} else {
		log.Warn("工具执行器缺少 bili_jct，写操作不可用",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", "no_csrf"),
		)
	}

	if sender, err := buildGiftSender(cfg); err == nil {
		execs.Gift = giftExecutor{sender: sender, log: log}
	} else {
		log.Info("送礼执行器未装配",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", err.Error()))
	}

	recorder, err := watch.NewStreamRecorder(watch.StreamRecorderOptions{
		RoomID:     cfg.Collector.Live.RoomID,
		Cookie:     cfg.Watch.Cookie,
		Quality:    cfg.Watch.StreamQuality,
		FFmpegPath: cfg.Watch.FFmpegPath,
		TempDir:    cfg.Watch.TempDir,
	})
	if err == nil {
		execs.Frame = recorder
		if cfg.LLM.Enabled {
			if _, prov, model, err := cfg.LLM.Resolve(cfg.Watch.LLMRoute); err == nil {
				if describer, err := watch.NewOpenAIDescriber(prov.BaseURL, prov.APIKey, model, nil, 0); err == nil {
					execs.FrameRead = frameReader{inner: describer}
				}
			}
		}
	}

	log.Info("工具执行器装配完成",
		zap.String("action", "tool_executor_setup"),
		zap.Bool("live_status", execs.Live != nil),
		zap.Bool("social", execs.Social != nil),
		zap.Bool("gift", execs.Gift != nil),
		zap.Bool("vision", execs.Frame != nil && execs.FrameRead != nil),
		zap.Bool("web_search", execs.Search != nil),
	)
	return execs
}

// buildGiftSender 用 [gift] 配置装配 NodeSender；账本与 fishd 同目录。
func buildGiftSender(cfg config.Config) (*gift.NodeSender, error) {
	quota, err := gift.NewFileQuota(filepath.Join(filepath.Dir(cfg.Runtime.StateFile), "gift-ledger.json"))
	if err != nil {
		return nil, err
	}
	return gift.New(gift.Options{
		NodePath:     cfg.Gift.NodePath,
		ScriptPath:   cfg.Gift.ScriptPath,
		Cookie:       cfg.Gift.Cookie,
		CookieFile:   cfg.Gift.CookieFile,
		Timeout:      time.Duration(cfg.Gift.TimeoutMS) * time.Millisecond,
		DryRun:       !cfg.Gift.Real,
		DailyLimit:   cfg.Gift.DailyLimit,
		MaxPrice:     cfg.Gift.MaxPrice,
		MaxNum:       cfg.Gift.MaxNum,
		AllowedRooms: cfg.Gift.AllowedRooms,
		Quota:        quota,
	})
}

// buildGiftQuota 装配只读额度源；失败只降级为 nil。
func buildGiftQuota(cfg config.Config, log *zap.Logger) reasoning.QuotaSource {
	quota, err := gift.NewFileQuota(filepath.Join(filepath.Dir(cfg.Runtime.StateFile), "gift-ledger.json"))
	if err != nil {
		log.Warn("送礼额度账本装配失败，quota 工具降级",
			zap.String("action", "quota_setup"), zap.Error(err))
		return nil
	}
	return quota
}

// sideEffectDeps 把配置投影成副作用工具的护栏参数（与 fishd 同口径）。
func sideEffectDeps(cfg config.Config, quota reasoning.QuotaSource, exec toolExecutors, room string, giftGate func() bool, log *zap.Logger) reasoning.SideEffectDeps {
	giftEnabled := cfg.Gift.Enabled
	if giftGate != nil {
		giftEnabled = giftGate()
	}
	return reasoning.SideEffectDeps{
		Enabled:          cfg.Reasoning.AllowSideEffectTools,
		Gift:             exec.Gift,
		Social:           exec.Social,
		QuotaView:        quota,
		GiftEnabled:      giftEnabled,
		GiftReal:         cfg.Gift.Real,
		GiftID:           cfg.Gift.GiftID,
		MaxNum:           cfg.Gift.MaxNum,
		DailyLimit:       cfg.Gift.DailyLimit,
		AllowedRooms:     cfg.Gift.AllowedRooms,
		AllowPostDynamic: false,
		OwnerMID:         ownerMID(cfg),
		SelfMID:          selfMID(cfg),
		Room:             room,
		Log:              log,
	}
}

// ownerMID 优先用调度配置里的 owner_ids，缺省回落主人常量。
func ownerMID(cfg config.Config) string {
	if cfg.Scheduler != nil {
		if live, ok := cfg.Scheduler.Channels[event.ChLive]; ok && len(live.OwnerIDs) > 0 {
			if mid := strings.TrimSpace(live.OwnerIDs[0]); mid != "" {
				return mid
			}
		}
	}
	return defaultOwnerMID
}

// selfMID 优先用发送器自报的 UID，其次采集侧 self_id。
func selfMID(cfg config.Config) string {
	if cfg.Sender != nil {
		if mid := strings.TrimSpace(cfg.Sender.SelfID); mid != "" {
			return mid
		}
	}
	return strings.TrimSpace(cfg.Collector.Live.SelfID)
}

// giftExecutor 把 gift.NodeSender 适配成 reasoning.GiftExecutor。
type giftExecutor struct {
	sender *gift.NodeSender
	log    *zap.Logger
}

func (g giftExecutor) SendGift(ctx context.Context, req reasoning.GiftRequest) (string, error) {
	res, err := g.sender.Send(ctx, req.Room, req.GiftID, req.Num)
	if res.PendingReview && g.log != nil {
		g.log.Warn("送礼结果未知，额度已保留待核对",
			zap.String("action", "gift_pending_review"),
			zap.String("room", req.Room),
			zap.Int("num", req.Num),
		)
	}
	if err != nil {
		return "", err
	}
	return res.GiftID + "×" + strconv.Itoa(res.Num), nil
}

// frameReader 把 watch 的多帧描述器适配成 reasoning.FrameReader（单帧 + 详细识图词）。
type frameReader struct{ inner *watch.OpenAIDescriber }

func (f frameReader) Describe(ctx context.Context, framePath string) (string, error) {
	return f.inner.Describe(ctx, []string{framePath}, proactive.VisionPrompt)
}
