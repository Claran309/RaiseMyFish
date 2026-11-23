package main

// 本文件实现 B9 的 Prompt 缓存 A/B 实验（真实模型调用）。
//
// 对照方式：
//   stable   —— 当前线上顺序：稳定人格/协议前缀在前，动态资料在后；
//   unstable —— 在 system 最前面插入随机 nonce，使前缀无法命中缓存。
//
// 每侧先发一次“热身”，再发一次计入结果；只比较第二轮，避免首请求必然 miss。
// 如果 Provider 不返回任何缓存字段，报告明确写「不可判定」，不伪造 0 命中。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/personaeval"
)

type cacheABSide struct {
	Calls        int     `json:"calls"`
	CachedTokens int     `json:"cached_tokens"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	CostKnown    bool    `json:"cost_known"`
}

type cacheABReport struct {
	Cases      int         `json:"cases"`
	Model      string      `json:"model"`
	Stable     cacheABSide `json:"stable"`
	Unstable   cacheABSide `json:"unstable"`
	CacheDelta float64     `json:"cache_hit_delta"`
	CostDelta  float64     `json:"cost_delta_usd"`
	Conclusive bool        `json:"conclusive"`
	Conclusion string      `json:"conclusion"`
}

func runCost(ctx context.Context, configPath, envPath string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "cache-ab" {
		return errors.New("用法：fishctl cost cache-ab --output <新目录> [--limit N]")
	}
	fs := flag.NewFlagSet("cost cache-ab", flag.ContinueOnError)
	fs.SetOutput(stderr)
	packFlag := fs.String("pack", "", "人格目录或 personas 下包名")
	casesFlag := fs.String("cases", "", "用例 JSONL；默认包内 eval/cases.jsonl")
	route := fs.String("route", "default", "模型配置路由")
	output := fs.String("output", "", "新建证据目录（必须不存在）")
	limit := fs.Int("limit", 20, "最多跑多少条（控制费用）")
	timeout := fs.Duration("timeout", 20*time.Minute, "整轮时间上限")
	if err := fs.Parse(args[1:]); err != nil {
		return errors.New("缓存实验参数无效")
	}
	if fs.NArg() != 0 || *output == "" || *limit < 1 || *limit > 100 || *timeout <= 0 || *timeout > time.Hour {
		return errors.New("用法：fishctl cost cache-ab --output <新目录> [--limit 20]")
	}
	cfg, err := config.Load(configPath, envPath)
	if err != nil {
		return err
	}
	if !cfg.LLM.Enabled {
		return errors.New("缓存实验需要启用 llm")
	}
	if *packFlag != "" {
		cfg.LLM.PersonaDir = *packFlag
		if !strings.ContainsAny(*packFlag, `/\`) {
			cfg.LLM.PersonaDir = filepath.Join("personas", *packFlag)
		}
	}
	casesPath := *casesFlag
	if casesPath == "" {
		casesPath = filepath.Join(cfg.LLM.PersonaDir, "eval", "cases.jsonl")
	}
	scorer, err := personaeval.NewScorerVersion("persona-lexical-v5")
	if err != nil {
		return err
	}
	fixture, err := personaeval.Prepare(ctx, cfg.LLM.PersonaDir, casesPath, scorer)
	if err != nil {
		return err
	}
	if len(fixture.Cases) < *limit {
		*limit = len(fixture.Cases)
	}
	name, p, model, err := cfg.LLM.Resolve(*route)
	if err != nil {
		return err
	}
	client, err := provider.NewOpenAI(name, p.BaseURL, p.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	var price *config.Price
	if v, ok := p.Prices[model]; ok {
		price = &v
	}
	if err := os.Mkdir(*output, 0o700); err != nil {
		return fmt.Errorf("必须使用新的输出目录：%w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report := cacheABReport{Cases: *limit, Model: name + ":" + model}
	// 前缀用足够长的随机串：只改几个字符可能被按块缓存吞掉，测不出前缀效应。
	nonce, err := randomHex(256)
	if err != nil {
		return err
	}
	for i := 0; i < *limit; i++ {
		c := fixture.Cases[i]
		messages, err := cacheABMessages(runCtx, fixture.Pack, c, cfg.LLM.MaxPromptTokens)
		if err != nil {
			return fmt.Errorf("用例 %s 装配失败：%w", c.ID, err)
		}
		// 热身一次后测量；unstable 的 nonce 每条用例不同，确保前缀无效。
		if _, err := client.Chat(runCtx, cacheABRequest(model, messages, 0)); err != nil {
			return fmt.Errorf("用例 %s 稳定侧热身失败：%w", c.ID, err)
		}
		stableResp, err := client.Chat(runCtx, cacheABRequest(model, messages, 0))
		if err != nil {
			return fmt.Errorf("用例 %s 稳定侧失败：%w", c.ID, err)
		}
		unstable := cacheABUnstable(messages, nonce, c.ID)
		if _, err := client.Chat(runCtx, cacheABRequest(model, unstable, 0)); err != nil {
			return fmt.Errorf("用例 %s 对照侧热身失败：%w", c.ID, err)
		}
		unstableResp, err := client.Chat(runCtx, cacheABRequest(model, unstable, 0))
		if err != nil {
			return fmt.Errorf("用例 %s 对照侧失败：%w", c.ID, err)
		}
		accumulateCacheSide(&report.Stable, stableResp, price)
		accumulateCacheSide(&report.Unstable, unstableResp, price)
		fmt.Fprintf(stderr, "[%d/%d] %s\n", i+1, *limit, c.ID)
	}
	report.CacheDelta = float64(report.Stable.CachedTokens-report.Unstable.CachedTokens) / float64(maxInt(report.Cases, 1))
	report.CostDelta = report.Stable.CostUSD - report.Unstable.CostUSD
	switch {
	case report.Stable.CachedTokens == 0 && report.Unstable.CachedTokens == 0:
		report.Conclusion = "Provider 未返回缓存命中字段：A/B 不可判定"
	case report.Stable.CachedTokens > report.Unstable.CachedTokens:
		report.Conclusive = true
		report.Conclusion = fmt.Sprintf("稳定前缀有效：稳定侧多命中 %d tokens（%.0f/条），费用差 %.6f USD",
			report.Stable.CachedTokens-report.Unstable.CachedTokens, report.CacheDelta, -report.CostDelta)
	default:
		report.Conclusive = true
		report.Conclusion = "未观察到稳定前缀优势：缓存命中没有提升"
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "report.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	out, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(out))
	return nil
}

func cacheABMessages(ctx context.Context, pack persona.Pack, c personaeval.Case, maxPrompt int) ([]provider.Message, error) {
	channel := c.Channel
	private := channel == event.ChDM
	scope := memory.Scope{Channel: channel, Session: "cache-ab-" + c.ID, Private: private}
	ev := event.Event{
		ID: string(channel) + ":cache-ab:" + c.ID, Channel: channel, Dir: event.DirIn,
		Session: scope.Session, FromID: "eval-user", FromName: "回归用户",
		Text: c.Input, Private: private, At: time.Now().UTC(),
	}
	task := brain.Task{ID: "cache-ab:" + c.ID, EventID: ev.ID, Channel: channel, Session: scope.Session}
	input := brain.ContextInput{Event: ev, Persona: pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope}}
	prepared, err := brain.Prepare(ctx, task, input, brain.Budget{MaxPromptTokens: maxPrompt})
	if err != nil {
		return nil, err
	}
	return prepared.Messages, nil
}

func cacheABRequest(model string, messages []provider.Message, temperature float64) provider.Request {
	return provider.Request{Model: model, Messages: messages, MaxTokens: 64, Temperature: temperature}
}

func cacheABUnstable(messages []provider.Message, nonce, caseID string) []provider.Message {
	out := append([]provider.Message(nil), messages...)
	if len(out) == 0 || out[0].Role != "system" {
		return out
	}
	out[0].Content = fmt.Sprintf("实验随机前缀[%s-%s]\n%s", nonce, caseID, out[0].Content)
	return out
}

func accumulateCacheSide(side *cacheABSide, resp provider.Response, price *config.Price) {
	side.Calls++
	beforeIn, beforeOut := side.InputTokens, side.OutputTokens
	if resp.Usage.InputTokens != nil {
		side.InputTokens += *resp.Usage.InputTokens
	}
	if resp.Usage.OutputTokens != nil {
		side.OutputTokens += *resp.Usage.OutputTokens
	}
	if resp.Usage.CachedInputTokens != nil {
		side.CachedTokens += *resp.Usage.CachedInputTokens
	}
	if price != nil && price.InputUSDPerMillion != nil && price.OutputUSDPerMillion != nil {
		side.CostKnown = true
		side.CostUSD += float64(side.InputTokens-beforeIn)*(*price.InputUSDPerMillion)/1e6 +
			float64(side.OutputTokens-beforeOut)*(*price.OutputUSDPerMillion)/1e6
	}
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
